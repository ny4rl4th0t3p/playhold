package playhold

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	defaultStuckTimeout = 5 * time.Second
	defaultSlowTurn     = 100 * time.Millisecond
)

// ErrRuntimeClosed means the runtime has been shut down.
var ErrRuntimeClosed = errors.New("playhold: runtime closed")

// Config configures a Runtime.
type Config struct {
	// Logger receives runtime logs. Nil uses slog.Default.
	Logger *slog.Logger
	// StuckTimeout is how long one turn can run before its room is closed as stuck. Zero or less uses 5 seconds.
	StuckTimeout time.Duration
	// SlowTurn is the time budget of one turn: a turn longer than this is logged as slow, with the message or timer that
	// started it, and the room carries on. The default is 100 milliseconds. It's measured in real time, whatever Now says.
	SlowTurn Timeout
	// NewSeed returns the seed of a new room's random numbers. Nil uses a random seed. Set it to reproduce a game.
	NewSeed func() [32]byte
	// Now is the clock rooms read at the start of each turn. Nil uses time.Now. Set it to reproduce a game.
	Now func() time.Time
}

// Runtime creates rooms and keeps track of the ones still running.
type Runtime struct {
	cfg    Config
	logger *slog.Logger

	mu      sync.Mutex
	rooms   map[RoomID]*Room
	closed  bool
	onStuck func(*Room) // set by a Server, to watch what an abandoned room's goroutine does next
}

// NewRuntime returns a Runtime with no rooms.
func NewRuntime(cfg Config) *Runtime {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.StuckTimeout <= 0 {
		cfg.StuckTimeout = defaultStuckTimeout
	}
	cfg.SlowTurn = cfg.SlowTurn.orDefault(defaultSlowTurn)
	if cfg.NewSeed == nil {
		cfg.NewSeed = randomSeed
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Runtime{
		cfg:    cfg,
		logger: cfg.Logger,
		rooms:  make(map[RoomID]*Room),
	}
}

func randomSeed() [32]byte {
	var seed [32]byte
	_, _ = rand.Read(seed[:]) // documented to never return an error
	return seed
}

// CreateRoom gives the room a new ID, runs the type's Init hook and starts the room. It returns an error,
// with no room running, if the runtime is closed or Init fails.
func (rt *Runtime) CreateRoom(typ *RoomType) (*Room, error) {
	return rt.createRoom(typ, "", nil, nil)
}

// restoreRoom brings a room back from a snapshot, keeping its ID. Init doesn't run again, because the room already
// exists: it's the same match.
func (rt *Runtime) restoreRoom(
	typ *RoomType,
	id RoomID,
	code string,
	data []byte,
	writer *snapshotWriter,
	restart func(*Room) bool,
	policy TimersAfterRestart,
) (*Room, error) {
	room := newRoom(id, typ, &rt.cfg, rt.forget)
	room.onStuck = rt.reportStuck
	room.code = code
	room.writer = writer
	room.restart = restart
	if err := room.applySnapshot(data, policy); err != nil {
		return nil, err
	}
	if err := rt.register(room); err != nil {
		return nil, err
	}
	go room.run()
	return room, nil
}

// createRoom also gives the room its code, what saves it and what restores it after a crash, if anything does.
func (rt *Runtime) createRoom(typ *RoomType, code string, writer *snapshotWriter, restart func(*Room) bool) (*Room, error) {
	room := newRoom(newRoomID(), typ, &rt.cfg, rt.forget)
	room.onStuck = rt.reportStuck
	room.code = code
	room.writer = writer
	room.restart = restart

	// Register first, so a Shutdown during Init also stops this room.
	if err := rt.register(room); err != nil {
		return nil, err
	}
	if err := room.runInit(); err != nil {
		room.finish(CloseInitFailed)
		return nil, err
	}
	go room.run()
	return room, nil
}

// Room returns a running room.
func (rt *Runtime) Room(id RoomID) (*Room, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	room, ok := rt.rooms[id]
	return room, ok
}

// Shutdown closes every room and waits for them to finish. New rooms are refused from this point on.
// It returns the context's error if a room has not finished in time, for example because a handler is stuck.
func (rt *Runtime) Shutdown(ctx context.Context) error {
	rt.mu.Lock()
	rt.closed = true
	rooms := make([]*Room, 0, len(rt.rooms))
	for _, room := range rt.rooms {
		rooms = append(rooms, room)
	}
	rt.mu.Unlock()

	for _, room := range rooms {
		room.requestStop(CloseShutdown)
	}
	for _, room := range rooms {
		select {
		case <-room.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (rt *Runtime) register(room *Room) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return ErrRuntimeClosed
	}
	rt.rooms[room.id] = room
	return nil
}

// reportStuck passes a room the watchdog gave up on to whoever watches for it.
func (rt *Runtime) reportStuck(room *Room) {
	rt.mu.Lock()
	onStuck := rt.onStuck
	rt.mu.Unlock()
	if onStuck != nil {
		onStuck(room)
	}
}

func (rt *Runtime) watchStuck(fn func(*Room)) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.onStuck = fn
}

// queueFill returns the share of all running rooms' inbound queues in use, zero with no rooms.
func (rt *Runtime) queueFill() float64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var used, capacity int
	for _, room := range rt.rooms {
		used += len(room.inbox)
		capacity += cap(room.inbox)
	}
	if capacity == 0 {
		return 0
	}
	return float64(used) / float64(capacity)
}

// forget removes a finished room.
func (rt *Runtime) forget(room *Room) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.rooms, room.id)
}
