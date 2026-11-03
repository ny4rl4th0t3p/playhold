package playhold

import (
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
)

// maxEventDepth limits how many times events can trigger further events in one turn, so a loop fails loudly.
const maxEventDepth = 16

// Errors returned when a message reaches a room.
var (
	// ErrUnknownMessage means no module in the room handles the message.
	ErrUnknownMessage = errors.New("playhold: unknown message")
	// ErrBadMessage means the message is not the type its handler expects.
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
	typ    *RoomType
	module string
	errs   []error
	needs  []string
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
	r.typ.slots = append(r.typ.slots, slotInit{index: key.index, init: func() any { return init() }})
	r.typ.slotCount = max(r.typ.slotCount, key.index+1)
}

// Handle declares the handler for one of the module's messages. IDs are local to the module. RoomContext.Player
// says who sent the message.
func Handle[M any](r *Registry, id uint32, handler func(rc *RoomContext, msg *M) error) {
	if handler == nil {
		r.errorf("message %d has no handler", id)
		return
	}
	key := handlerKey{module: r.module, id: id}
	if _, taken := r.typ.handlers[key]; taken {
		r.errorf("message %d is registered twice", id)
		return
	}
	r.typ.handlers[key] = func(rc *RoomContext, msg any) error {
		typed, ok := msg.(*M)
		if !ok {
			return fmt.Errorf("%w: module %q message %d got %T", ErrBadMessage, key.module, id, msg)
		}
		return handler(rc, typed)
	}
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
	// Name identifies the room type in logs.
	Name string
	// QueueSize is the inbound queue capacity. Zero or less uses the default.
	QueueSize int
	// Modules are the modules in this room type. Events reach them in this order.
	Modules []Module
	// Init runs once before the room starts, after module state is created. An error or panic stops the
	// room from being created.
	Init func(rc *RoomContext) error
	// Close runs once on the room's goroutine after its last turn, unless Init failed.
	Close func(rc *RoomContext, reason CloseReason)
}

// RoomType is a validated room configuration, ready to create rooms from. Build room types at startup, so
// wiring mistakes stop the server instead of failing mid-match.
type RoomType struct {
	cfg       RoomConfig
	slots     []slotInit
	slotCount int
	handlers  map[handlerKey]messageHandler
	events    map[reflect.Type][]eventHandler
}

type slotInit struct {
	index int
	init  func() any
}

type handlerKey struct {
	module string
	id     uint32
}

type (
	messageHandler func(rc *RoomContext, msg any) error
	eventHandler   func(rc *RoomContext, event any)
)

// NewRoomType registers the config's modules and checks how they fit together. It reports every problem it
// finds: a nil or unnamed module, a duplicate module name, a duplicate state or message, or a missing keeper.
func NewRoomType(cfg RoomConfig) (*RoomType, error) {
	typ := &RoomType{
		cfg:      cfg,
		handlers: make(map[handlerKey]messageHandler),
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

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return typ, nil
}
