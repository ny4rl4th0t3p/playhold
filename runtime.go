package playhold

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// ErrRuntimeClosed means the runtime has been shut down.
var ErrRuntimeClosed = errors.New("playhold: runtime closed")

// Config configures a Runtime.
type Config struct {
	// Logger receives runtime logs. Nil uses slog.Default.
	Logger *slog.Logger
}

// Runtime creates rooms and keeps track of the ones still running.
type Runtime struct {
	logger *slog.Logger

	mu     sync.Mutex
	rooms  map[RoomID]*Room
	closed bool
}

// NewRuntime returns a Runtime with no rooms.
func NewRuntime(cfg Config) *Runtime {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{
		logger: logger,
		rooms:  make(map[RoomID]*Room),
	}
}

// CreateRoom gives the room a new ID, runs the type's Init hook and starts the room. It returns an error,
// with no room running, if the runtime is closed or Init fails.
func (rt *Runtime) CreateRoom(typ *RoomType) (*Room, error) {
	room := newRoom(newRoomID(), typ, rt.logger, rt.forget)

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

// forget removes a finished room.
func (rt *Runtime) forget(room *Room) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.rooms, room.id)
}
