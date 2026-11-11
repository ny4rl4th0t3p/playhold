package playhold

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
)

// maxEventDepth limits how many times events can trigger further events in one turn, so a loop fails loudly.
const maxEventDepth = 16

// Errors returned when a message reaches a room.
var (
	// ErrUnknownMessage means no module in the room handles the message.
	ErrUnknownMessage = errors.New("playhold: unknown message")
	// ErrBadMessage means the payload can't be decoded as the message its handler expects.
	ErrBadMessage = errors.New("playhold: bad message type")
)

// Module is a reusable piece of game logic. A room type is built from a list of modules.
//
// Modules never import each other's main packages. A module that calls another one declares the keeper
// interface it needs, and the application wires in the provider.
type Module interface {
	// Name identifies the module. It must be unique within a room type.
	Name() string
	// Register declares the module's state, handlers, events and needs. It runs when a room type is built.
	Register(r *Registry)
}

// Key gives a module typed access to its state in the current room. A module keeps its key unexported, so
// only that module can reach the state.
type Key[T any] struct {
	name  string
	index int
}

// keyCount hands out each key's position in a room's slots.
var keyCount atomic.Int64

// NewKey returns a key for a module's state. Create it once, in a package-level variable.
func NewKey[T any](name string) *Key[T] {
	return &Key[T]{name: name, index: int(keyCount.Add(1) - 1)}
}

// Get returns the module's state in the current room. It panics, closing the room, if the module isn't part
// of the room type, which means a dependency was not declared.
func (k *Key[T]) Get(rc *RoomContext) *T {
	slots := rc.room.slots
	if k.index >= len(slots) || slots[k.index] == nil {
		panic(fmt.Sprintf("playhold: state %q is not part of room type %q", k.name, rc.room.typ.cfg.Name))
	}
	state, _ := slots[k.index].(*T) // always *T: State is the only writer of this slot
	return state
}

// Registry collects what a module declares. Mistakes are reported together by NewRoomType.
type Registry struct {
	typ     *RoomType
	module  string
	errs    []error
	needs   []string
	hasView bool

	hasSnapshot     bool
	hasClientSchema bool
}

func (r *Registry) errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Errorf("playhold: module %q: "+format, append([]any{r.module}, args...)...))
}

// Needs declares that this module requires another module's keeper. Building the room type fails if the
// keeper is nil or the providing module isn't in the room type. Optional keepers aren't declared: the module
// just checks for nil before calling them.
func (r *Registry) Needs(module string, keeper any) {
	if keeper == nil {
		r.errorf("needs a keeper from %q, but none was wired in", module)
	}
	r.needs = append(r.needs, module)
}

// State declares the module's state. Each room gets its own copy from init when it is created.
func State[T any](r *Registry, key *Key[T], init func() *T) {
	if init == nil {
		r.errorf("state %q has no initializer", key.name)
		return
	}
	for _, slot := range r.typ.slots {
		if slot.index == key.index {
			r.errorf("state %q is registered twice", key.name)
			return
		}
	}
	r.typ.slots = append(r.typ.slots, slotInit{module: r.module, index: key.index, init: func() any { return init() }})
	r.typ.slotCount = max(r.typ.slotCount, key.index+1)
}

// Snapshot declares how a module's state is saved, so a room can come back after a restart. The state stays a plain
// struct, and save and load convert it to and from a Protobuf message at this schema version, which starts at 1. A
// module has at most one snapshot, and a server with a store needs one for every state.
func Snapshot[T, M any, PM interface {
	*M
	proto.Message
}](r *Registry, key *Key[T], version int, save func(state *T) PM, load func(msg PM) *T) {
	switch {
	case save == nil || load == nil:
		r.errorf("snapshot of %q needs both save and load", key.name)
		return
	case version < 1:
		r.errorf("snapshot of %q has version %d, but versions start at 1", key.name, version)
		return
	case r.hasSnapshot:
		r.errorf("snapshot is registered twice")
		return
	}
	r.hasSnapshot = true
	r.typ.snapshots = append(r.typ.snapshots, moduleSnapshot{
		module:  r.module,
		index:   key.index,
		version: version,
		steps:   make(map[int]func(old []byte) ([]byte, error)),
		encode: func(state any, opts proto.MarshalOptions) ([]byte, error) {
			typed, _ := state.(*T) // always *T: State is the only writer of this slot
			return opts.Marshal(save(typed))
		},
		decode: func(data []byte) (any, error) {
			msg := PM(new(M))
			if err := proto.Unmarshal(data, msg); err != nil {
				return nil, err
			}
			return load(msg), nil
		},
	})
}

// Migrate declares how a saved state moves from schema version from to the next one. A room saved at an older version
// goes through each step in order when it's restored. Every version needs its step, from 1 up to the
// current one, so a server can read any match saved by an earlier release. Building the room type fails if one is
// missing. If a step returns an error or panics, the room isn't restored, and its saved state stays as it was.
func Migrate[T any](r *Registry, key *Key[T], from int, migrate func(old []byte) ([]byte, error)) {
	if migrate == nil {
		r.errorf("migration of %q from version %d has no function", key.name, from)
		return
	}
	r.typ.migrations = append(r.typ.migrations, moduleMigration{module: r.module, index: key.index, from: from, migrate: migrate})
}

// ClientSchema declares which versions of the module's messages and views the server can talk to: current is what this
// build uses, and oldest is the oldest client it still accepts. A client below that is told to update. Keep at least the
// previous version, because a page and its server don't change at the same moment, and an open tab can stay old for days.
//
// Inside that window, messages can only change by adding to them, which a client or server that doesn't know the new
// field ignores. A client newer than the server, while a deploy rolls out, is accepted for the same reason.
//
// A module that doesn't declare this accepts every client. These versions are about messages, and have nothing to do
// with Snapshot's, which are about saved state.
func (r *Registry) ClientSchema(current, oldest int) {
	switch {
	case oldest < 1 || oldest > current:
		r.errorf("client schema has oldest %d and current %d, but needs 1 <= oldest <= current", oldest, current)
		return
	case r.hasClientSchema:
		r.errorf("client schema is declared twice")
		return
	}
	r.hasClientSchema = true
	r.typ.clientSchemas = append(r.typ.clientSchemas, moduleClientSchema{module: r.module, oldest: oldest})
}

// RateLimit limits how often one connection may send this message, on top of ServerConfig.MessageRate. Over it, the
// message gets a rate-limited error and isn't handled. Use it for messages that cost the room or other players
// something, such as chat. The message needs a handler, and the limit needs both Every and Burst.
func (r *Registry) RateLimit(id uint32, limit RateLimit) {
	if limit.Off || limit.Every <= 0 || limit.Burst <= 0 {
		r.errorf("rate limit of message %d needs a positive Every and Burst", id)
		return
	}
	key := handlerKey{module: r.module, id: id}
	if _, taken := r.typ.limits[key]; taken {
		r.errorf("rate limit of message %d is declared twice", id)
		return
	}
	r.typ.limits[key] = limit
}

// Handle declares the handler for one of the module's messages. IDs are local to the module. RoomContext.Player
// says who sent the message.
func Handle[M any, PM interface {
	*M
	proto.Message
}](r *Registry, id uint32, handler func(rc *RoomContext, msg PM) error) {
	if handler == nil {
		r.errorf("message %d has no handler", id)
		return
	}
	key := handlerKey{module: r.module, id: id}
	if _, taken := r.typ.handlers[key]; taken {
		r.errorf("message %d is registered twice", id)
		return
	}
	r.typ.handlers[key] = func(rc *RoomContext, payload []byte) error {
		msg := PM(new(M))
		if err := proto.Unmarshal(payload, msg); err != nil {
			return fmt.Errorf("%w: module %q message %d", ErrBadMessage, key.module, id)
		}
		return handler(rc, msg)
	}
}

// View declares what the module shows a player, who is RoomContext.Player inside the function. It runs at the end
// of every turn, once per connected player, and its result is sent as is. Copy across only what that player may
// see, and never send the module's state directly: a new state field stays private until it's added to the view.
// Returning nil shows nothing. A view must not change state or emit events. A module has at most one view.
func View[V any, PV interface {
	*V
	proto.Message
}](r *Registry, view func(rc *RoomContext) PV) {
	if view == nil {
		r.errorf("view has no function")
		return
	}
	if r.hasView {
		r.errorf("view is registered twice")
		return
	}
	r.hasView = true
	module := r.module
	r.typ.views = append(r.typ.views, moduleView{module: module, build: func(rc *RoomContext) ([]byte, bool) {
		msg := view(rc)
		if msg == nil {
			return nil, false
		}
		data, err := proto.Marshal(msg)
		if err != nil {
			panic(fmt.Sprintf("playhold: module %q view can't be encoded: %v", module, err))
		}
		return data, true
	}})
}

// On declares a handler for an event type emitted with RoomContext.Emit. Events are matched by their exact
// type, so E can't be an interface. Subscribing creates no dependency on the emitting module.
func On[E any](r *Registry, handler func(rc *RoomContext, event E)) {
	eventType := reflect.TypeFor[E]()
	if eventType.Kind() == reflect.Interface {
		r.errorf("event type %s is an interface", eventType)
		return
	}
	if handler == nil {
		r.errorf("event %s has no handler", eventType)
		return
	}
	r.typ.events[eventType] = append(r.typ.events[eventType], func(rc *RoomContext, event any) {
		typed, _ := event.(E) // always E: events are looked up by their exact type
		handler(rc, typed)
	})
}

// RoomConfig describes a kind of room.
type RoomConfig struct {
	// Name identifies the room type in logs and in saved rooms. A server only restores saved rooms of its own room type,
	// so renaming one leaves its saved rooms unrestored.
	Name string
	// QueueSize is the inbound queue capacity. Zero or less uses the default.
	QueueSize int
	// MaxPlayers is how many seats a room has. Zero or less means no limit.
	MaxPlayers int
	// Modules are the modules in this room type. Events reach them in this order.
	Modules []Module
	// Init runs once before the room starts, after module state is created. An error or panic stops the
	// room from being created.
	Init func(rc *RoomContext) error
	// OnJoinAttempt decides whether a player may take a free seat, on the room's goroutine, so it can read the game's
	// state: a game refuses new players once it has started, for example, which locks the room. An error refuses the
	// seat and is the player's answer: ErrRoomFull answers that the room is full, and any other error that the join was
	// rejected. A player who already has a seat is never asked again, so they can always come back. Nil lets anyone take
	// a free seat, up to MaxPlayers, which is checked first. It must decide from what's in memory: waiting on a database
	// or a service would hold up the whole room.
	OnJoinAttempt func(rc *RoomContext, attempt *JoinAttempt) error
	// Close runs once on the room's goroutine after its last turn, unless Init failed.
	Close func(rc *RoomContext, reason CloseReason)
	// Record records every turn of this type's rooms, on a server with RecordingConfig.Dir set, so a game can be replayed
	// with playholdtest.Replay. Every module's state needs a snapshot, and Name has to be a folder name: letters, digits,
	// - and _. A recorded room encodes its state after every turn, to hash it.
	Record bool
}

// JoinAttempt is a player asking for a free seat. It gains fields as playhold learns more about players, such as their
// account's claims, so a game's OnJoinAttempt keeps compiling.
type JoinAttempt struct {
	Player PlayerID
}

// RoomType is a validated room configuration, ready to create rooms from. Build room types at startup, so
// wiring mistakes stop the server instead of failing mid-match.
type RoomType struct {
	cfg       RoomConfig
	slots     []slotInit
	slotCount int
	handlers  map[handlerKey]messageHandler
	limits    map[handlerKey]RateLimit
	events    map[reflect.Type][]eventHandler
	views     []moduleView
	snapshots []moduleSnapshot

	migrations []moduleMigration // checked and moved into snapshots by NewRoomType

	clientSchemas []moduleClientSchema
}

type moduleClientSchema struct {
	module string
	oldest int
}

// outdatedModule returns the first module that this client is too old for, or an empty string if it's fine. A client
// that doesn't mention a module counts as older than all of its versions.
func (t *RoomType) outdatedModule(versions map[string]uint32) string {
	for _, schema := range t.clientSchemas {
		if int(versions[schema.module]) < schema.oldest {
			return schema.module
		}
	}
	return ""
}

type moduleSnapshot struct {
	module  string
	index   int
	version int
	encode  func(state any, opts proto.MarshalOptions) ([]byte, error)
	decode  func(data []byte) (any, error)

	steps map[int]func(old []byte) ([]byte, error) // from a version to the next
}

type moduleMigration struct {
	module  string
	index   int
	from    int
	migrate func(old []byte) ([]byte, error)
}

// load turns saved state at any version this server knows into the module's state.
func (s *moduleSnapshot) load(version int, data []byte) (any, error) {
	if version < 1 || version > s.version {
		return nil, fmt.Errorf("saved at version %d, but this server only knows versions 1 to %d", version, s.version)
	}
	for ; version < s.version; version++ {
		next, err := s.steps[version](data)
		if err != nil {
			return nil, fmt.Errorf("migrating from version %d: %w", version, err)
		}
		data = next
	}
	return s.decode(data)
}

// attachMigrations gives each snapshot its steps, and reports every step that's wrong or missing.
func (t *RoomType) attachMigrations() error {
	var errs []error
	for i := range t.migrations {
		migration := &t.migrations[i]
		at := slices.IndexFunc(t.snapshots, func(s moduleSnapshot) bool { return s.index == migration.index })
		if at < 0 {
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %q has a migration but no snapshot", t.cfg.Name, migration.module))
			continue
		}
		snapshot := &t.snapshots[at]
		if migration.from < 1 || migration.from >= snapshot.version {
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %q has a migration from version %d, but its version is %d",
				t.cfg.Name, migration.module, migration.from, snapshot.version))
			continue
		}
		if _, taken := snapshot.steps[migration.from]; taken {
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %q has two migrations from version %d",
				t.cfg.Name, migration.module, migration.from))
			continue
		}
		snapshot.steps[migration.from] = migration.migrate
	}
	for i := range t.snapshots {
		snapshot := &t.snapshots[i]
		for from := 1; from < snapshot.version; from++ {
			if _, ok := snapshot.steps[from]; !ok {
				errs = append(errs, fmt.Errorf("playhold: room type %q: module %q is at version %d, but has no migration from version %d",
					t.cfg.Name, snapshot.module, snapshot.version, from))
			}
		}
	}
	return errors.Join(errs...)
}

// checkRecording refuses a room type that records but can't: its name names its recordings' folder, and its state's hash
// needs every module's state.
func (t *RoomType) checkRecording() error {
	if !t.cfg.Record {
		return nil
	}
	var errs []error
	if !validRecordingName(t.cfg.Name) {
		errs = append(errs, fmt.Errorf("playhold: room type %q records, so its name has to be a folder name: letters, "+
			"digits, - and _", t.cfg.Name))
	}
	if err := t.requireSnapshots(); err != nil {
		errs = append(errs, fmt.Errorf("playhold: room type %q records, so every module's state needs a snapshot: %w",
			t.cfg.Name, err))
	}
	return errors.Join(errs...)
}

// requireSnapshots reports every state that couldn't be saved. A server with a store checks this at startup, so a
// missing snapshot stops the server instead of quietly losing a match.
func (t *RoomType) requireSnapshots() error {
	var errs []error
	for _, slot := range t.slots {
		saved := slices.ContainsFunc(t.snapshots, func(snapshot moduleSnapshot) bool { return snapshot.index == slot.index })
		if !saved {
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %q has state but no snapshot", t.cfg.Name, slot.module))
		}
	}
	return errors.Join(errs...)
}

type moduleView struct {
	module string
	build  func(rc *RoomContext) (data []byte, ok bool)
}

type slotInit struct {
	module string
	index  int
	init   func() any
}

type handlerKey struct {
	module string
	id     uint32
}

type (
	messageHandler func(rc *RoomContext, payload []byte) error
	eventHandler   func(rc *RoomContext, event any)
)

// NewRoomType registers the config's modules and checks how they fit together. It reports every problem it
// finds: a nil or unnamed module, a duplicate module name, a duplicate state or message, or a missing keeper.
func NewRoomType(cfg RoomConfig) (*RoomType, error) {
	typ := &RoomType{
		cfg:      cfg,
		handlers: make(map[handlerKey]messageHandler),
		limits:   make(map[handlerKey]RateLimit),
		events:   make(map[reflect.Type][]eventHandler),
	}

	var errs []error
	present := make(map[string]bool, len(cfg.Modules))
	needs := make(map[string][]string)
	for i, module := range cfg.Modules {
		if module == nil {
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %d is nil", cfg.Name, i))
			continue
		}
		name := module.Name()
		switch {
		case name == "":
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %d has no name", cfg.Name, i))
			continue
		case present[name]:
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %q appears twice", cfg.Name, name))
			continue
		}
		present[name] = true

		registry := &Registry{typ: typ, module: name}
		module.Register(registry)
		errs = append(errs, registry.errs...)
		needs[name] = registry.needs
	}

	for i := range typ.snapshots {
		snapshot := &typ.snapshots[i]
		registered := slices.ContainsFunc(typ.slots, func(slot slotInit) bool { return slot.index == snapshot.index })
		if !registered {
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %q has a snapshot of state it never registered",
				cfg.Name, snapshot.module))
		}
	}

	if err := typ.attachMigrations(); err != nil {
		errs = append(errs, err)
	}

	// A limit can be declared before its handler, so this runs after all of them have registered.
	for key := range typ.limits {
		if _, ok := typ.handlers[key]; !ok {
			errs = append(errs, fmt.Errorf("playhold: room type %q: module %q limits message %d, which has no handler",
				cfg.Name, key.module, key.id))
		}
	}

	// Modules can need each other in any order, so this runs after all of them have registered.
	for _, module := range cfg.Modules {
		if module == nil {
			continue
		}
		for _, provider := range needs[module.Name()] {
			if !present[provider] {
				errs = append(errs, fmt.Errorf("playhold: room type %q: module %q needs %q, which is not in the room type",
					cfg.Name, module.Name(), provider))
			}
		}
	}

	if err := typ.checkRecording(); err != nil {
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	// The modules have registered their state, handlers, views and events, which is all rooms use from here on. The list
	// itself is dropped: it shares its array with the caller's slice, so keeping it could only let a later change leak in.
	typ.cfg.Modules = nil
	return typ, nil
}
