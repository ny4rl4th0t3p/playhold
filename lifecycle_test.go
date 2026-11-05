package playhold

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// startSavingServer returns a running server, on its own runtime, that plays savedModule and shows its number.
func startSavingServer(t *testing.T, records store.Store, rooms RoomConfig, rtCfg Config) (server *Server, url string) {
	t.Helper()
	rooms.Modules = append(rooms.Modules, viewOfSaved, savedModule)
	server, err := NewServer(newConfiguredRuntime(t, rtCfg), &ServerConfig{
		RoomType:    newTestRoomType(t, rooms),
		Store:       records,
		MemoryLimit: -1,
		Logger:      slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	return server, "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

func checkStatus(handler http.HandlerFunc) int {
	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody))
	return recorder.Code
}

func TestDrainRefusesNewRoomsAndPlayersButNotThoseAlreadyPlaying(t *testing.T) {
	server, url := startSavingServer(t, memory.New(), RoomConfig{}, Config{})
	alice, welcome := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, alice).GetState())

	server.Drain()

	tests := []struct {
		name string
		code string
	}{
		{name: "a new player, to the room that exists", code: testCode},
		{name: "a new player, with a new code", code: "another-room"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, _ := newGuest(t, url)

			sendFrame(t, ws, joinFrame(tt.code))

			assert.Equal(t, wire.ErrorCode_ERROR_CODE_UNAVAILABLE, readServerFrame(t, ws).GetError().GetCode())
		})
	}

	// The player refused from this room took a turn there, like any refused message, so alice gets its view.
	require.NotNil(t, readServerFrame(t, alice).GetState())

	// The game goes on.
	sendFrame(t, alice, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 5}))
	assert.Equal(t, "5", savedView(t, readServerFrame(t, alice)))

	// A player with a seat can come back to it.
	back := returningGuest(t, url, welcome)
	joinRoom(t, back, testCode)
	assert.Equal(t, "5", savedView(t, readServerFrame(t, back)))
}

func TestRoomRestoredWhileDrainingStillRefusesNewPlayers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _ := restoreLobby(t, memory.New(), RoomConfig{}, Config{})
		room := enterRoom(t, l, testCode)
		synctest.Wait()
		l.drain()

		crash(t, room)
		next := l.replacement(testCode, room)
		require.NotNil(t, next)

		require.ErrorIs(t, seatAndWait(t, next, "bob"), ErrDraining)
	})
}

func TestHealthChecks(t *testing.T) {
	tests := []struct {
		name      string
		store     store.Store
		drain     bool
		wantReady int
	}{
		{name: "healthy", store: memory.New(), wantReady: http.StatusOK},
		{name: "no store is fine", wantReady: http.StatusOK},
		{name: "store down", store: downStore{}, wantReady: http.StatusServiceUnavailable},
		{name: "draining", store: memory.New(), drain: true, wantReady: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := NewServer(newTestRuntime(t), &ServerConfig{
				RoomType:    newTestRoomType(t, RoomConfig{}),
				Store:       tt.store,
				MemoryLimit: -1,
				Logger:      slog.New(slog.DiscardHandler),
			})
			require.NoError(t, err)
			t.Cleanup(server.Close)
			if tt.drain {
				server.Drain()
			}

			assert.Equal(t, tt.wantReady, checkStatus(server.Ready))
			assert.Equal(t, http.StatusOK, checkStatus(server.Live), "the process is up either way")
		})
	}
}

func TestShutdownSavesEveryRoomThenDisconnectsWithoutClosingTheGame(t *testing.T) {
	records := memory.New()
	server, url := startSavingServer(t, records, RoomConfig{
		Close: func(rc *RoomContext, reason CloseReason) {
			if reason == CloseShutdown {
				savedKey.Get(rc).value += 100
			}
		},
	}, Config{})
	alice, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, alice).GetState())
	sendFrame(t, alice, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 5}))
	require.Equal(t, "5", savedView(t, readServerFrame(t, alice)))

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	require.NoError(t, server.Shutdown(ctx))

	// The last thing a client hears isn't RoomClosed, which would make it forget the room.
	_, _, err := alice.Read(gatewayContext(t))
	require.Error(t, err)

	rooms, err := records.LoadRooms(t.Context())
	require.NoError(t, err)
	require.Len(t, rooms, 1)

	// The next server has the game, including what the Close hook did last.
	_, nextURL := startSavingServer(t, records, RoomConfig{}, Config{})
	back, _ := seatedGuest(t, nextURL)
	assert.Equal(t, "105", savedView(t, readServerFrame(t, back)))
}

func TestShutdownCanBeRepeatedAndRefusesNewConnections(t *testing.T) {
	server, url := startSavingServer(t, memory.New(), RoomConfig{}, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	require.NoError(t, server.Shutdown(ctx))
	require.NoError(t, server.Shutdown(ctx))

	assert.Equal(t, http.StatusServiceUnavailable, checkStatus(server.Ready))
	_, resp, err := websocket.Dial(gatewayContext(t), url, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestShutdownReportsARoomThatDoesNotFinishInTime(t *testing.T) {
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
	ws, _ := seatedGuest(t, url)
	sendFrame(t, ws, messageFrame(t, 3, "slow", msgFail, &addMsg{}))
	waitFor(t, started)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	require.ErrorIs(t, server.Shutdown(ctx), context.DeadlineExceeded)
}

func TestRoomStuckDuringShutdownKeepsItsSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		records := memory.New()
		l, rt := restoreLobby(t, records, RoomConfig{}, Config{StuckTimeout: timeout})
		room := enterRoom(t, l, testCode)
		addTo(t, room, 3)
		synctest.Wait()

		release := make(chan struct{})
		require.NoError(t, room.submit(input{run: func(_ *RoomContext) error {
			<-release
			return nil
		}}))
		synctest.Wait()

		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		require.NoError(t, rt.Shutdown(ctx), "the watchdog gives up on the stuck room in time")
		synctest.Wait()

		_, value := savedRoom(t, records, room)
		assert.Equal(t, int32(3), value, "the match is still there for the next server")

		close(release)
		synctest.Wait()
	})
}
