package playhold

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

const testGrace = 20 * time.Second // shorter than testIdle

// presenceLog records what a game hears about players coming and going. It's written on the room's goroutine.
type presenceLog struct {
	mu     sync.Mutex
	events []string
}

func (p *presenceLog) add(event string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

func (p *presenceLog) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.events)
}

func (p *presenceLog) module() Module {
	return registerFunc{name: "presence", register: func(r *Registry) {
		On(r, func(rc *RoomContext, event PlayerDisconnected) {
			if rc.Player() != event.Player {
				p.add("wrong player")
			}
			p.add("away:" + string(event.Player))
		})
		On(r, func(rc *RoomContext, event PlayerReturned) {
			if rc.Player() != event.Player {
				p.add("wrong player")
			}
			p.add("back:" + string(event.Player))
		})
		On(r, func(_ *RoomContext, _ *wrapperspb.BoolValue) { p.add("timer") })
	}}
}

// graceLobby is a lobby with the grace period on, like the one a Server makes.
func graceLobby(t *testing.T, records store.Store, cfg RoomConfig) (*lobby, *Runtime) {
	t.Helper()
	l, rt := restoreLobby(t, records, cfg, Config{})
	l.grace = testGrace
	return l, rt
}

func sleepAndSettle(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func TestPlayerDisconnectedFiresWhenTheGraceRunsOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &presenceLog{}
		l, _ := graceLobby(t, memory.New(), RoomConfig{Modules: []Module{log.module()}})
		room := enterRoom(t, l, testCode)
		sink := &fakeSink{}
		seatViewer(t, room, "alice", sink)

		room.removeViewer("alice", sink)

		sleepAndSettle(testGrace - time.Second)
		assert.Empty(t, log.seen())

		sleepAndSettle(time.Second)
		assert.Equal(t, []string{"away:alice"}, log.seen())

		sleepAndSettle(3 * testGrace)
		assert.Equal(t, []string{"away:alice"}, log.seen(), "it only fires once")
		runInRoom(t, room, func(rc *RoomContext) { assert.Equal(t, []PlayerID{"alice"}, rc.Players()) })
	})
}

func TestComingBackInsideTheGraceIsSeamless(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &presenceLog{}
		l, _ := graceLobby(t, memory.New(), RoomConfig{Modules: []Module{log.module()}})
		room := enterRoom(t, l, testCode)
		sink := &fakeSink{}
		seatViewer(t, room, "alice", sink)

		room.removeViewer("alice", sink)
		sleepAndSettle(testGrace / 2)
		seatViewer(t, room, "alice", &fakeSink{})

		sleepAndSettle(3 * testGrace)
		assert.Empty(t, log.seen())
	})
}

func TestComingBackAfterTheGraceIsReportedAndCanHappenAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &presenceLog{}
		l, _ := graceLobby(t, memory.New(), RoomConfig{Modules: []Module{log.module()}})
		room := enterRoom(t, l, testCode)
		sink := &fakeSink{}
		seatViewer(t, room, "alice", sink)

		room.removeViewer("alice", sink)
		sleepAndSettle(testGrace)
		seatViewer(t, room, "alice", sink)
		assert.Equal(t, []string{"away:alice", "back:alice"}, log.seen())

		room.removeViewer("alice", sink)
		sleepAndSettle(testGrace)
		assert.Equal(t, []string{"away:alice", "back:alice", "away:alice"}, log.seen())
	})
}

func TestReplacedConnectionStartsNoGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &presenceLog{}
		l, _ := graceLobby(t, memory.New(), RoomConfig{Modules: []Module{log.module()}})
		room := enterRoom(t, l, testCode)
		old := &fakeSink{}
		seatViewer(t, room, "alice", old)
		seatViewer(t, room, "alice", &fakeSink{})

		// The old connection's end arrives after the new one has taken over.
		room.removeViewer("alice", old)

		sleepAndSettle(3 * testGrace)
		assert.Empty(t, log.seen())
	})
}

func TestEachPlayerHasTheirOwnGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const gap = 10 * time.Second
		log := &presenceLog{}
		l, _ := graceLobby(t, memory.New(), RoomConfig{Modules: []Module{log.module()}})
		room := enterRoom(t, l, testCode)
		alice, bob := &fakeSink{}, &fakeSink{}
		seatViewer(t, room, "alice", alice)
		seatViewer(t, room, "bob", bob)

		room.removeViewer("alice", alice)
		sleepAndSettle(gap)
		room.removeViewer("bob", bob)

		sleepAndSettle(testGrace - gap)
		assert.Equal(t, []string{"away:alice"}, log.seen())

		sleepAndSettle(gap)
		assert.Equal(t, []string{"away:alice", "away:bob"}, log.seen())
	})
}

func TestGraceAndGameTimersFireInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &presenceLog{}
		l, _ := graceLobby(t, memory.New(), RoomConfig{Modules: []Module{log.module()}})
		room := enterRoom(t, l, testCode)
		sink := &fakeSink{}
		seatViewer(t, room, "alice", sink)

		runInRoom(t, room, func(rc *RoomContext) {
			rc.After(testGrace/2, &wrapperspb.BoolValue{})
			rc.After(testGrace*2, &wrapperspb.BoolValue{})
		})
		room.removeViewer("alice", sink)

		sleepAndSettle(3 * testGrace)
		assert.Equal(t, []string{"timer", "away:alice", "timer"}, log.seen())
	})
}

func TestRestartStartsAGraceForEverySeatExceptThoseAlreadyAway(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		log := &presenceLog{}
		cfg := RoomConfig{Modules: []Module{log.module()}}
		first, firstRuntime := graceLobby(t, records, cfg)
		room := enterRoom(t, first, testCode)
		alice, bob := &fakeSink{}, &fakeSink{}
		seatViewer(t, room, "alice", alice)
		seatViewer(t, room, "bob", bob)

		room.removeViewer("bob", bob)
		sleepAndSettle(testGrace)
		require.Equal(t, []string{"away:bob"}, log.seen())
		stopProcess(t, firstRuntime)

		second, _ := graceLobby(t, records, cfg)
		require.NoError(t, second.restoreAll(t.Context(), records))
		sleepAndSettle(testGrace)
		assert.Equal(t, []string{"away:bob", "away:alice"}, log.seen())

		// Being away survived the restart, so the game hears that they came back.
		seatViewer(t, enterRoom(t, second, testCode), "bob", bob)
		assert.Equal(t, []string{"away:bob", "away:alice", "back:bob"}, log.seen())
	})
}

func TestCrashKeepsPlayersWhoFollowAndWhoAreAway(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &presenceLog{}
		l, _ := graceLobby(t, memory.New(), RoomConfig{Modules: []Module{log.module()}})
		room := enterRoom(t, l, testCode)
		alice, bob := &fakeSink{}, &fakeSink{}
		seatViewer(t, room, "alice", alice)
		seatViewer(t, room, "bob", bob)

		crash(t, room)
		next := l.replacement(testCode, room)
		require.NotNil(t, next)

		// Alice's connection follows the room, as a session does, and bob's has gone.
		seatViewer(t, next, "alice", alice)
		sleepAndSettle(testGrace)
		require.Equal(t, []string{"away:bob"}, log.seen())

		crash(t, next)
		last := l.replacement(testCode, next)
		require.NotNil(t, last)

		seatViewer(t, last, "bob", bob)
		assert.Equal(t, []string{"away:bob", "back:bob"}, log.seen())
	})
}

func TestDisconnectHandlerThatCrashesHitsTheRestartLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		crashing := registerFunc{name: "crashing", register: func(r *Registry) {
			On(r, func(_ *RoomContext, _ PlayerDisconnected) { panic("boom") })
		}}
		l, _ := graceLobby(t, records, RoomConfig{Modules: []Module{crashing}})
		room := enterRoom(t, l, testCode)
		sink := &fakeSink{}
		seatViewer(t, room, "alice", sink)
		room.removeViewer("alice", sink)

		for range maxRestarts {
			sleepAndSettle(testGrace)
			next := l.replacement(testCode, room)
			require.NotNil(t, next, "it restarts up to the limit")
			room = next
		}

		sleepAndSettle(testGrace)
		assert.Nil(t, l.replacement(testCode, room), "it closes after the limit")
		assert.Empty(t, l.rooms)
		requireNoRooms(t, records)
	})
}

func TestRoomWithoutALobbyHasNoGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &presenceLog{}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{log.module()}})
		sink := &fakeSink{}
		seatViewer(t, room, "alice", sink)

		room.removeViewer("alice", sink)

		sleepAndSettle(time.Hour)
		assert.Empty(t, log.seen())
	})
}

// fillQueue holds the room's goroutine with a full queue, until the returned function is called.
func fillQueue(t *testing.T, room *Room) (release func()) {
	t.Helper()
	held := make(chan struct{})
	require.NoError(t, room.submit(input{run: func(_ *RoomContext) error {
		<-held
		return nil
	}}))
	synctest.Wait()
	require.NoError(t, room.submit(input{noViews: true, run: func(_ *RoomContext) error { return nil }}))
	return func() { close(held) }
}

func TestLeavingAFullRoomIsNotLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log := &presenceLog{}
		l, _ := graceLobby(t, memory.New(), RoomConfig{QueueSize: 1, Modules: []Module{log.module()}})
		room := enterRoom(t, l, testCode)
		sink := &fakeSink{}
		seatViewer(t, room, "alice", sink)
		release := fillQueue(t, room)

		go room.removeViewer("alice", sink)
		synctest.Wait()
		release()
		synctest.Wait()

		sleepAndSettle(testGrace)
		assert.Equal(t, []string{"away:alice"}, log.seen())
	})
}

func TestSeatWaitsForSpaceInsteadOfRefusing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{QueueSize: 1})
		release := fillQueue(t, room)

		results := make(chan error, 1)
		go func() { _ = room.seat("alice", nil, func(err error) { results <- err }) }()
		synctest.Wait()
		assert.Empty(t, results, "it's still waiting")

		release()
		require.NoError(t, <-results)
	})
}

func TestWaitingForSpaceEndsWhenTheRoomCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{QueueSize: 1})
		release := fillQueue(t, room)
		defer release()

		results := make(chan error, 1)
		go func() { results <- room.seat("alice", nil, nil) }()
		synctest.Wait()

		room.Close()

		require.ErrorIs(t, <-results, ErrRoomClosed)
	})
}

func TestDisconnectingAndReturningOverARealConnection(t *testing.T) {
	events := make(chan string, 2)
	module := registerFunc{name: "m", register: func(r *Registry) {
		On(r, func(_ *RoomContext, _ PlayerDisconnected) { events <- "away" })
		On(r, func(_ *RoomContext, _ PlayerReturned) { events <- "back" })
	}}
	url := startServerWith(t, &ServerConfig{
		RoomType:        newTestRoomType(t, RoomConfig{Modules: []Module{module}}),
		DisconnectGrace: Timeout{After: 50 * time.Millisecond},
	})
	next := func() string {
		select {
		case event := <-events:
			return event
		case <-time.After(testTimeout):
			require.FailNow(t, "timed out waiting for an event")
			return ""
		}
	}

	first, welcome := seatedGuest(t, url)
	require.NoError(t, first.CloseNow())
	assert.Equal(t, "away", next())

	joinRoom(t, returningGuest(t, url, welcome), testCode)
	assert.Equal(t, "back", next())
}

// roomOf returns the room running under this code.
func roomOf(server *Server, code string) *Room {
	server.lobby.mu.Lock()
	defer server.lobby.mu.Unlock()
	return server.lobby.rooms[code].room
}

func TestJoiningARoomThatJustStoppedAsksTheClientToRetry(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	module := registerFunc{name: "slow", register: func(r *Registry) {
		Handle(r, msgFail, func(_ *RoomContext, _ *addMsg) error {
			close(started)
			<-release
			return nil
		})
	}}
	server, url := startSavingServer(t, memory.New(), RoomConfig{Modules: []Module{module}}, Config{StuckTimeout: time.Hour})
	alice, _ := seatedGuest(t, url)
	sendFrame(t, alice, messageFrame(t, 3, "slow", msgFail, &addMsg{}))
	waitFor(t, started)

	// It has been told to stop, but it can't finish yet, so the lobby still hands it out.
	roomOf(server, testCode).Close()
	bob, _ := newGuest(t, url)
	sendFrame(t, bob, joinFrame(testCode))

	got := readServerFrame(t, bob).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_UNAVAILABLE, got.GetCode(), "ROOM_CLOSED would make the client forget the room")
	assert.Equal(t, uint64(2), got.GetSeq())
}

func TestCrashWhileAJoinIsQueuedStillAnswersTheJoin(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	module := registerFunc{name: "crashing", register: func(r *Registry) {
		Handle(r, msgFail, func(_ *RoomContext, _ *addMsg) error {
			close(started)
			<-release
			panic("boom")
		})
	}}
	server, url := startSavingServer(t, memory.New(), RoomConfig{Modules: []Module{module}}, Config{StuckTimeout: time.Hour})
	alice, _ := seatedGuest(t, url)
	sendFrame(t, alice, messageFrame(t, 3, "crashing", msgFail, &addMsg{}))
	waitFor(t, started)

	room := roomOf(server, testCode)
	bob, _ := newGuest(t, url)
	sendFrame(t, bob, joinFrame(testCode))
	require.Eventually(t, func() bool { return len(room.inbox) == 1 }, testTimeout, time.Millisecond, "the Join never reached the queue")

	close(release)

	frame := readServerFrame(t, bob)
	require.NotNil(t, frame.GetJoined(), "got %v", frame)
	assert.Equal(t, uint64(2), frame.GetJoined().GetSeq())
	assert.Equal(t, "0", savedView(t, readServerFrame(t, bob)))
	assert.NotSame(t, room, roomOf(server, testCode))
}

// readUntilView reads until the player sees this number, or the read times out.
func readUntilView(t *testing.T, ws *websocket.Conn, want string) {
	t.Helper()
	for {
		if frame := readServerFrame(t, ws); frame.GetState() != nil && savedView(t, frame) == want {
			return
		}
	}
}

func TestReconnectStormLosesNoRoom(t *testing.T) {
	const players = 40
	records := memory.New()
	start := func() (server *Server, url string) {
		var err error
		server, err = NewServer(newTestRuntime(t), &ServerConfig{
			// A queue far smaller than the crowd, so nobody gets in unless joining waits for space.
			RoomType:      newTestRoomType(t, RoomConfig{QueueSize: 2, Modules: []Module{viewOfSaved, savedModule}}),
			Store:         records,
			MaxConnsPerIP: 2 * players,
			// Every player connects from 127.0.0.1, so the limits per address are off.
			ConnectRate: RateLimit{Off: true},
			GuestRate:   RateLimit{Off: true},
			MemoryLimit: -1,
			Logger:      slog.New(slog.DiscardHandler),
			Development: true,
		})
		require.NoError(t, err)
		httpServer := httptest.NewServer(server)
		t.Cleanup(func() {
			server.Close()
			httpServer.Close()
		})
		return server, "ws" + strings.TrimPrefix(httpServer.URL, "http")
	}

	server, url := start()
	ids := make([]string, 0, players)
	secrets := make([]string, 0, players)
	var firstPlayer *websocket.Conn
	for range players {
		ws, welcome := seatedGuest(t, url)
		if firstPlayer == nil {
			firstPlayer = ws
		}
		ids = append(ids, welcome.GetPlayerId())
		secrets = append(secrets, welcome.GetSecret())
	}
	sendFrame(t, firstPlayer, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 5}))
	readUntilView(t, firstPlayer, "5")

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	require.NoError(t, server.Shutdown(ctx))

	_, nextURL := start()
	conns := make([]*websocket.Conn, 0, players)
	for i := range players {
		ws := dialServer(t, nextURL)
		sendFrame(t, ws, helloFrame(protocolVersion, ids[i], secrets[i]))
		require.NotNil(t, readServerFrame(t, ws).GetWelcome())
		conns = append(conns, ws)
	}

	// Everyone asks at once, each on their own connection's goroutine on the server.
	for _, ws := range conns {
		sendFrame(t, ws, joinFrame(testCode))
	}
	for _, ws := range conns {
		frame := readServerFrame(t, ws)
		require.NotNil(t, frame.GetJoined(), "got %v", frame)
		readUntilView(t, ws, "5")
	}

	rooms, err := records.LoadRooms(t.Context())
	require.NoError(t, err)
	assert.Len(t, rooms, 1)

	sendFrame(t, conns[0], messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 1}))
	for _, ws := range conns {
		readUntilView(t, ws, "6")
	}
}
