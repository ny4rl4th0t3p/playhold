package playhold

import (
	"errors"
	"sync"
	"time"
)

// ErrTooManyRooms means the server won't create another room. Rooms that already exist can still be joined.
var ErrTooManyRooms = errors.New("playhold: too many rooms")

// lobby maps room codes to rooms. It creates a room when a code is first used, up to a limit, since anyone can ask for
// one with a new code. It closes a room once nobody has been connected to it for the idle time, which frees its code.
type lobby struct {
	rt       *Runtime
	typ      *RoomType
	idle     time.Duration
	maxRooms int
	writer   *snapshotWriter // nil if rooms aren't saved

	mu    sync.Mutex
	rooms map[string]*lobbyRoom
}

type lobbyRoom struct {
	room  *Room
	conns int
	timer *time.Timer // running while conns is zero
}

func newLobby(rt *Runtime, typ *RoomType, idle time.Duration, maxRooms int, writer *snapshotWriter) *lobby {
	return &lobby{rt: rt, typ: typ, idle: idle, maxRooms: maxRooms, writer: writer, rooms: make(map[string]*lobbyRoom)}
}

// enter counts a new connection to the room with this code, creating the room if needed. Each successful call
// needs a matching leave. Creating a room holds the lobby's lock, so Init hooks should be quick.
func (l *lobby) enter(code string) (*Room, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.rooms[code]
	if !ok && len(l.rooms) >= l.maxRooms {
		return nil, ErrTooManyRooms
	}
	if !ok || entry.room.closed() {
		room, err := l.rt.createRoom(l.typ, code, l.writer)
		if err != nil {
			return nil, err
		}
		entry = &lobbyRoom{room: room}
		l.rooms[code] = entry
		go l.watch(code, entry)
	}
	entry.conns++
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
	return entry.room, nil
}

// leave ends a connection's count on the room.
func (l *lobby) leave(code string, room *Room) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.rooms[code]
	if !ok || entry.room != room {
		return // the room has already closed
	}
	entry.conns--
	if entry.conns == 0 {
		entry.timer = time.AfterFunc(l.idle, func() { l.closeIfEmpty(entry) })
	}
}

// closeIfEmpty closes the room unless someone came back just as the timer fired.
func (l *lobby) closeIfEmpty(entry *lobbyRoom) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry.conns == 0 {
		entry.room.Close()
	}
}

// watch frees the code when the room closes, for any reason.
func (l *lobby) watch(code string, entry *lobbyRoom) {
	<-entry.room.Done()

	l.mu.Lock()
	defer l.mu.Unlock()
	if entry.timer != nil {
		entry.timer.Stop()
	}
	if l.rooms[code] == entry {
		delete(l.rooms, code)
	}
}
