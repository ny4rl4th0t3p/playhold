// Package playholdtest runs a game's room in a test: the room's real code, events, views and timers, with no server,
// no network and no real time.
//
// Tests run inside Run, a testing/synctest bubble, where time only moves when the test moves it, with Room.Advance.
// Every call returns once the room has nothing left to do, so a turn's events have run and its views have been
// delivered by then:
//
//	playholdtest.Run(t, func(t *testing.T) {
//		room := playholdtest.NewRoom(t, typ, playholdtest.Options{})
//		alice := room.Join("alice")
//		err := alice.Send("hands", msgDraw, &pb.Draw{})
//		hand := playholdtest.View[pb.HandsView](alice, "hands")
//	})
//
// The room runs as on a server with the same timeouts, but nothing of the server itself: no handshake, rate limits,
// admission control or matchmaking, and a room whose turn panics closes instead of being restored.
package playholdtest

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/internal/roomtest"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

const closeTimeout = 10 * time.Second // fake time, so it never slows a test down

// Run runs f in a testing/synctest bubble, where rooms are made. It fails the test if a goroutine f started is still
// running when f returns; the rooms themselves are closed by the test's cleanup.
func Run(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	synctest.Test(t, f)
}

// Options configures a room. Zero values behave as on a server with ServerConfig's defaults.
type Options struct {
	// Seed is the seed of the room's random numbers. The zero seed picks a random one, as a server does, and a test that
	// fails logs it, so it can be set here to play the same game again.
	Seed [32]byte
	// DisconnectGrace is ServerConfig.DisconnectGrace: how long a player who disconnected has to come back before the
	// game gets a PlayerDisconnected. The default is 1 minute.
	DisconnectGrace playhold.Timeout
	// InactiveRoomTimeout is ServerConfig.InactiveRoomTimeout: the room closes once no player has sent it anything for
	// this long. The default is 30 minutes, so Advance past it closes a room nobody uses.
	InactiveRoomTimeout playhold.Timeout
	// TimersAfterRestart is ServerConfig.TimersAfterRestart, for Room.Restart. The default is TimersPause.
	TimersAfterRestart playhold.TimersAfterRestart
}

// Room is a room under test.
type Room struct {
	t   *testing.T
	typ *playhold.RoomType
	h   roomtest.Room
}

// NewRoom creates a room of typ, running its Init hook, and closes it when the test ends. Call it inside Run. A
// failure to create the room fails the test.
func NewRoom(t *testing.T, typ *playhold.RoomType, opts Options) *Room {
	t.Helper()
	h, err := roomtest.New(&roomtest.Config{
		Type:     typ,
		Seed:     opts.Seed,
		Logger:   slog.New(slog.NewTextHandler(t.Output(), nil)),
		Grace:    roomtest.Timeout(opts.DisconnectGrace),
		Inactive: roomtest.Timeout(opts.InactiveRoomTimeout),
		Timers:   int(opts.TimersAfterRestart),
	})
	if err != nil {
		t.Fatalf("playholdtest: creating the room: %v", err)
	}
	r := &Room{t: t, typ: typ, h: h}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := r.h.Close(ctx); err != nil {
			t.Errorf("playholdtest: closing the room: %v", err)
		}
		if t.Failed() {
			t.Logf("playholdtest: the room's seed was %#v; set it as Options.Seed to play the same game again", r.h.Seed())
		}
	})
	synctest.Wait()
	return r
}

// Join seats the player, or reconnects one who already has a seat, and connects them, so they get views from then on.
// A refusal fails the test. A player who joins again gets a new Player, and the earlier one stops getting views.
func (r *Room) Join(player playhold.PlayerID) *Player {
	r.t.Helper()
	p, err := r.TryJoin(player)
	if err != nil {
		r.t.Fatalf("playholdtest: %s joining: %v", player, err)
	}
	return p
}

// TryJoin is Join, returning the refusal: ErrRoomFull, an error from the game's OnJoinAttempt, or ErrRoomClosed.
func (r *Room) TryJoin(player playhold.PlayerID) (*Player, error) {
	p := &Player{room: r, id: player}
	p.sink = sink{p}
	err := r.await(func(result func(error)) error { return r.h.Seat(string(player), p.sink, result) })
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Advance moves time forward by d. Timers, grace periods and the close for inactivity that come due meanwhile fire in
// order, each in its turn, as they would on a server.
func (*Room) Advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// Do runs fn as a turn of the room, on its goroutine, so a module's own tests can read or set up its state through its
// key. Views go out after it, as after any turn. It fails the test if the room has closed.
func (r *Room) Do(fn func(rc *playhold.RoomContext)) {
	r.t.Helper()
	err := r.await(func(result func(error)) error {
		return r.h.Do(func(rc any) {
			roomContext, _ := rc.(*playhold.RoomContext) // always: playhold passes its RoomContext
			fn(roomContext)
		}, result)
	})
	if err != nil {
		r.t.Fatalf("playholdtest: running in the room: %v", err)
	}
}

// Restart saves the room as a server shutting down would, and brings it back from that save, as the next server would.
// Everyone is disconnected, and joins again to carry on. Timers follow Options.TimersAfterRestart. It fails the test if
// a module's state has no snapshot, or the room has closed.
func (r *Room) Restart() {
	r.t.Helper()
	r.RestartAs(r.typ)
}

// RestartAs is Restart onto another room type, as a deploy would: a module whose snapshot version changed runs its
// migrations.
func (r *Room) RestartAs(typ *playhold.RoomType) {
	r.t.Helper()
	if err := r.h.Restart(typ); err != nil {
		r.t.Fatalf("playholdtest: restarting the room: %v", err)
	}
	r.typ = typ
	synctest.Wait()
}

// Closed reports whether the room has closed, and why. A room closes as on a server: a turn that panicked, the game
// closing it, or InactiveRoomTimeout.
func (r *Room) Closed() (playhold.CloseReason, bool) {
	reason, closed := r.h.Closed()
	return playhold.CloseReason(reason), closed
}

// await queues work with queue, waits until the room has nothing left to do, and returns the work's result, or
// ErrRoomClosed if the room closed before doing it.
func (*Room) await(queue func(result func(error)) error) error {
	var (
		mu   sync.Mutex
		got  error
		done bool
	)
	err := queue(func(err error) {
		mu.Lock()
		defer mu.Unlock()
		got, done = err, true
	})
	if err != nil {
		return err
	}
	synctest.Wait()
	mu.Lock()
	defer mu.Unlock()
	if !done {
		return playhold.ErrRoomClosed
	}
	return got
}

// Player is a player connected to a room under test.
type Player struct {
	room *Room
	id   playhold.PlayerID
	sink sink

	mu       sync.Mutex
	views    map[string][]byte // the latest state, by module
	received [][]byte
}

// ID returns the player's ID.
func (p *Player) ID() playhold.PlayerID { return p.id }

// Send sends the module's message as the player, and returns its handler's error, so errors.Is works on it. A message
// no module handles gets ErrUnknownMessage, and one that the room closed before handling, ErrRoomClosed, as does a turn
// that panicked, which closes the room.
func (p *Player) Send(module string, id uint32, msg proto.Message) error {
	p.room.t.Helper()
	payload, err := proto.Marshal(msg)
	if err != nil {
		p.room.t.Fatalf("playholdtest: encoding %T: %v", msg, err)
	}
	return p.room.await(func(result func(error)) error {
		return p.room.h.Send(string(p.id), module, id, payload, result)
	})
}

// Disconnect drops the player's connection. Their grace period starts, as on a server, and Room.Join brings them back.
func (p *Player) Disconnect() {
	p.room.h.Leave(string(p.id), p.sink)
	synctest.Wait()
}

// Received returns every state the player has been sent, oldest first, each encoded as it's sent to a client. A test
// that looks for something a player must never see, such as another player's cards, searches these bytes.
func (p *Player) Received() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.received)
}

// View returns the module's view in the latest state the player received, decoded, or nil if the module showed them
// nothing then. It fails the test if the view doesn't decode as V.
func View[V any, PV interface {
	*V
	proto.Message
}](p *Player, module string) PV {
	p.room.t.Helper()
	p.mu.Lock()
	data, ok := p.views[module]
	p.mu.Unlock()
	if !ok {
		return nil
	}
	view := PV(new(V))
	if err := proto.Unmarshal(data, view); err != nil {
		p.room.t.Fatalf("playholdtest: decoding %s's view of %q as %T: %v", p.id, module, view, err)
	}
	return view
}

// sink receives the player's views from the room. It's its own type, so SendState isn't one of Player's methods.
type sink struct{ p *Player }

func (s sink) SendState(state *wire.RoomState) {
	data, err := proto.Marshal(state)
	if err != nil {
		panic(err) // it was built from encoded views, so it always encodes
	}
	views := make(map[string][]byte, len(state.GetViews()))
	for _, view := range state.GetViews() {
		views[view.GetModule()] = view.GetView()
	}
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.p.views = views
	s.p.received = append(s.p.received, data)
}
