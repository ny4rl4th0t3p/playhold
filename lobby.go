package playhold

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/ny4rl4th0t3p/playhold/store"
)

// A room that keeps failing is closed, so a message that always crashes it can't loop forever.
const (
	maxRestarts   = 3
	restartWindow = 10 * time.Minute
)

// ErrTooManyRooms means the server won't create another room. Rooms that already exist can still be joined.
var ErrTooManyRooms = errors.New("playhold: too many rooms")

// ErrRoomNotFound means no room has the code, and the server doesn't create rooms from codes clients choose.
var ErrRoomNotFound = errors.New("playhold: no room has this code")

// ErrDraining means the server is about to stop, and isn't taking new rooms or players. Players who already have a seat
// can still come back to it.
var ErrDraining = errors.New("playhold: the server is about to restart")

// lobby maps room codes to rooms. It creates a room, up to a limit, when asked with no code, making one, or when a code is
// first used, if it takes codes clients choose. It closes a room once nobody has been connected to it for the idle time,
// which frees its code.
type lobby struct {
	rt       *Runtime
	typ      *RoomType
	idle     time.Duration
	maxRooms int
	writer   *snapshotWriter // nil if rooms aren't saved
	timers   TimersAfterRestart
	grace    time.Duration
	inactive time.Duration // zero never closes a room for inactivity
	admit    func() bool   // whether new rooms and new players are taken now; nil always allows
	breaker  *breaker      // says which saved rooms keep crashing the server; nil restores them all
	metrics  *metrics      // nil counts nothing
	// clientCodes lets an unknown code create a room. A server sets it from ServerConfig.AllowClientCodes; the lobby's own
	// default is true, so tests that build one directly can pick their codes.
	clientCodes bool
	makeCode    func() string   // rand.Text; tests replace it to force a clash
	matchmaker  *matchmaker     // nil without ServerConfig.Matchmaking
	savedCodes  map[string]bool // the codes of every room in the store at the restore, which new rooms mustn't take
	savedRooms  map[RoomID]bool // the rooms left in the store after the restore, whose recordings go on

	mu       sync.Mutex
	rooms    map[string]*lobbyRoom
	draining bool
}

// lobbyRoom is one code's room. A room restored after a crash takes over the same entry, so its connections stay counted.
type lobbyRoom struct {
	room     *Room
	conns    int
	timer    *time.Timer // running while conns is zero
	restarts []time.Time
}

func newLobby(rt *Runtime, typ *RoomType, idle time.Duration, maxRooms int, writer *snapshotWriter) *lobby {
	return &lobby{
		rt: rt, typ: typ, idle: idle, maxRooms: maxRooms, writer: writer, clientCodes: true, makeCode: rand.Text,
		rooms: make(map[string]*lobbyRoom),
	}
}

// enter counts a new connection to the room with this code, and returns the room and its code. An empty code creates a
// room with a code the lobby makes; a code no room has creates one only if the lobby takes codes clients choose. Each
// successful call needs a matching leave. Creating a room holds the lobby's lock, so Init hooks should be quick.
func (l *lobby) enter(code string) (*Room, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	made := code == ""
	if made {
		code = l.newCode()
	}
	entry, ok := l.rooms[code]
	if !ok || entry.room.closed() {
		if !made && !l.clientCodes {
			return nil, "", ErrRoomNotFound
		}
		var err error
		if entry, err = l.create(code, ok); err != nil {
			return nil, "", err
		}
	}
	entry.conns++
	if entry.timer != nil {
		entry.timer.Stop()
		entry.timer = nil
	}
	return entry.room, code, nil
}

// match creates a room for a group the matchmaker formed, with its seats kept for them, and returns its code. Nobody is
// connected to it yet, so it closes after the idle time if they never come, unless the reservation closes it first.
func (l *lobby) match(players []PlayerID, reserveFor time.Duration, minPlayers int) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	code := l.newCode()
	entry, err := l.create(code, false)
	if err != nil {
		return "", err
	}
	entry.room.reserve(players, reserveFor, minPlayers)
	l.startIdleTimer(entry)
	return code, nil
}

// create starts a room under code, if the server takes another one now. replacing says a closed room still holds the
// code's entry, so the count of rooms doesn't grow. The caller holds mu.
func (l *lobby) create(code string, replacing bool) (*lobbyRoom, error) {
	switch {
	case !replacing && len(l.rooms) >= l.maxRooms:
		return nil, ErrTooManyRooms
	case l.draining:
		return nil, ErrDraining
	case l.admit != nil && !l.admit():
		return nil, ErrOverloaded
	}
	entry := &lobbyRoom{}
	room, err := l.rt.createRoom(l.typ, code, l.writer, l.restarter(code, entry))
	if err != nil {
		return nil, err
	}
	room.startSessions(l.sessions())
	entry.room = room
	l.rooms[code] = entry
	go l.watch(code, entry)
	return entry, nil
}

// newCode returns a code no room has, running or left in the store: 26 random letters and digits, so it can't be
// guessed, and a clash is practically impossible but checked anyway. The caller holds mu.
func (l *lobby) newCode() string {
	for {
		if code := l.makeCode(); l.rooms[code] == nil && !l.savedCodes[code] {
			return code
		}
	}
}

func (l *lobby) sessions() roomSessions {
	return roomSessions{grace: l.grace, inactive: l.inactive, admit: l.admit}
}

// drain stops new rooms and new players. Rooms already running carry on.
func (l *lobby) drain() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.draining = true
	for _, entry := range l.rooms {
		entry.room.draining.Store(true)
	}
}

func (l *lobby) isDraining() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.draining
}

// leave ends a connection's count on the room. It goes by the room's ID, because a room restored after a crash is the
// same room, even if the connection still holds the old one.
func (l *lobby) leave(code string, room *Room) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.rooms[code]
	if !ok || entry.room.id != room.id {
		return // the room has already closed
	}
	entry.conns--
	if entry.conns == 0 {
		l.startIdleTimer(entry)
	}
}

// startIdleTimer closes the room after the idle time, unless that's off.
func (l *lobby) startIdleTimer(entry *lobbyRoom) {
	if l.idle <= 0 {
		return
	}
	entry.timer = time.AfterFunc(l.idle, func() { l.closeIfEmpty(entry) })
}

// closeIfEmpty closes the room unless someone came back just as the timer fired.
func (l *lobby) closeIfEmpty(entry *lobbyRoom) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry.conns == 0 {
		entry.room.Close()
	}
}

// watch frees the code when the room closes for good. A room restored after a crash takes over before the old one
// reports being done, so this simply moves on to watching it.
func (l *lobby) watch(code string, entry *lobbyRoom) {
	l.mu.Lock()
	room := entry.room
	l.mu.Unlock()

	for {
		<-room.Done()

		l.mu.Lock()
		if entry.room != room {
			room = entry.room
			l.mu.Unlock()
			continue
		}
		if entry.timer != nil {
			entry.timer.Stop()
		}
		if l.rooms[code] == entry {
			delete(l.rooms, code)
		}
		l.mu.Unlock()
		return
	}
}

// restarter returns what a room calls when it crashes. If rooms aren't saved, there's nothing to restore it from.
func (l *lobby) restarter(code string, entry *lobbyRoom) func(*Room) bool {
	if l.writer == nil {
		return nil
	}
	return func(old *Room) bool { return l.restart(code, entry, old) }
}

// restart replaces a crashed room with a copy from its last snapshot, and reports whether it did.
func (l *lobby) restart(code string, entry *lobbyRoom, old *Room) bool {
	data := old.lastSnapshot.Load()
	if data == nil {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rooms[code] != entry || entry.room != old {
		return false
	}
	now := time.Now()
	entry.restarts = slices.DeleteFunc(entry.restarts, func(at time.Time) bool { return now.Sub(at) > restartWindow })
	if len(entry.restarts) >= maxRestarts {
		old.logger.Error("room keeps crashing, closing it", "restarts", len(entry.restarts), "window", restartWindow)
		return false
	}

	// The server stayed up, so no time was lost, whatever a restart would do with timers.
	next, err := l.rt.restoreRoom(l.typ, old.id, code, *data, l.writer, l.restarter(code, entry), TimersKeepRunning)
	if err != nil {
		old.logger.Error("restoring crashed room failed", "error", err)
		return false
	}
	next.draining.Store(l.draining)
	next.startSessions(l.sessions()) // the players still connected come back as soon as their sessions follow the room
	entry.restarts = append(entry.restarts, now)
	l.metrics.restored(old.reason)
	old.logger.Error("room crashed, restored from its last snapshot",
		"reason", old.reason.String(), "restarts", len(entry.restarts), "limit", maxRestarts, "window", restartWindow)
	entry.room = next
	return true
}

// replacement returns the room that took over from this one after a crash, or nil if it closed for good.
func (l *lobby) replacement(code string, old *Room) *Room {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.rooms[code]
	if !ok || entry.room == old || entry.room.id != old.id || entry.room.closed() {
		return nil
	}
	return entry.room
}

// restoreAll brings back every saved room, each starting with nobody connected. A room that can't be restored, or that
// kept crashing the server, is logged and left in the store, so a fixed server can try again, and doesn't stop the others.
func (l *lobby) restoreAll(ctx context.Context, records store.Store) error {
	if tracer := l.rt.currentTracer(); tracer != nil {
		var span trace.Span
		ctx, span = tracer.Start(ctx, "playhold.restore")
		defer span.End()
	}
	rooms, err := records.LoadRooms(ctx)
	if err != nil {
		return fmt.Errorf("playhold: loading rooms: %w", err)
	}
	// Room IDs sort by creation time, newest first here. If a code was reused while an older room couldn't be restored,
	// the newest room keeps the code.
	slices.SortFunc(rooms, func(a, b store.RoomRecord) int { return cmp.Compare(b.ID, a.ID) })

	l.mu.Lock()
	defer l.mu.Unlock()
	// A room left in the store keeps its code, so a new room mustn't get it: the next start would forget one of the two.
	l.savedCodes = make(map[string]bool, len(rooms))
	l.savedRooms = make(map[RoomID]bool, len(rooms))
	for i := range rooms {
		l.savedCodes[rooms[i].Code] = true
	}
	for i := range rooms {
		record := &rooms[i]
		if _, taken := l.rooms[record.Code]; taken {
			l.rt.logger.Warn("forgetting an older room with the same code", "room", record.ID, "code", record.Code)
			l.writer.forget(record.ID)
			continue
		}
		l.savedRooms[RoomID(record.ID)] = true // restored below, or left in the store for a later start
		if record.RoomType != l.typ.cfg.Name {
			l.rt.logger.Error("skipping a saved room of another type", "room", record.ID, "room_type", record.RoomType)
			continue
		}
		if l.breaker.keepsCrashing(RoomID(record.ID)) {
			l.rt.logger.Error("skipping a saved room that kept crashing the server; a start with a new build, or once its "+
				"incidents are older than the window, tries it again", "room", record.ID, "incidents", roomIncidents, "window", incidentWindow)
			continue
		}
		entry := &lobbyRoom{}
		room, err := l.rt.restoreRoom(l.typ, RoomID(record.ID), record.Code, record.Data, l.writer, l.restarter(record.Code, entry), l.timers)
		if err != nil {
			l.rt.logger.Error("skipping a saved room that can't be restored", "room", record.ID, "error", err)
			continue
		}
		room.startSessions(l.sessions())
		entry.room = room
		l.rooms[record.Code] = entry
		l.startIdleTimer(entry)
		go l.watch(record.Code, entry)
	}
	return nil
}
