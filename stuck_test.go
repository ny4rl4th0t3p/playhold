package playhold

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const msgBlock uint32 = 9

// stuckRoom enters a room on the server and sends it a message its blocker module doesn't return from until released.
func stuckRoom(t *testing.T, server *Server, code string) {
	t.Helper()
	room, _, err := server.lobby.enter(code)
	require.NoError(t, err)
	require.NoError(t, room.dispatch("", "blocker", msgBlock, mustMarshal(t, &addMsg{}), nil))
}

func TestRestartOnlyWhenAStuckGoroutineKeepsRunningWhileTheServerIsOverloaded(t *testing.T) {
	tests := []struct {
		name         string
		restart      bool
		admissionOff bool
		overloaded   bool
		finishes     bool // the stuck turn ends on its own, after 10 seconds
		wantRestart  bool
		wantWarning  bool
	}{
		{name: "spinning, with restarts on", restart: true, overloaded: true, wantRestart: true},
		{name: "spinning, with restarts off, says so every minute", overloaded: true, wantWarning: true},
		{name: "only slow: it ends before the grace", restart: true, overloaded: true, finishes: true},
		{name: "blocked: it runs on, but the server is fine", restart: true},
		{name: "restarts need admission control", restart: true, admissionOff: true, overloaded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := &fakeProcess{}
				saved := readProcess
				readProcess = fake
				t.Cleanup(func() { readProcess = saved })
				release := make(chan struct{}) // never reassigned: the stuck handlers read it from their own goroutines
				var releaseOnce sync.Once
				releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
				blocker := registerFunc{name: "blocker", register: func(r *Registry) {
					Handle(r, msgBlock, func(_ *RoomContext, _ *addMsg) error {
						<-release
						return nil
					})
				}}
				logs := &lockedBuffer{}
				rt := NewRuntime(Config{Logger: slog.New(slog.DiscardHandler), StuckTimeout: time.Second})
				server, err := NewServer(rt, &ServerConfig{
					RoomType:         newTestRoomType(t, RoomConfig{Modules: []Module{blocker}}),
					MemoryLimit:      -1,
					Logger:           slog.New(slog.NewTextHandler(logs, nil)),
					Development:      true,
					AllowClientCodes: true,
					RestartWhenStuck: tt.restart,
					Admission:        Admission{Off: tt.admissionOff},
				})
				require.NoError(t, err)
				t.Cleanup(func() {
					releaseAll() // so the stuck goroutines end with the test
					server.Close()
				})

				// Two stuck rooms, which still make one restart.
				stuckRoom(t, server, "one")
				stuckRoom(t, server, "two")
				if tt.overloaded {
					fake.setMemory(1)
				}
				if tt.finishes {
					time.Sleep(10 * time.Second)
					releaseAll()
				}

				sleepAndSettle(stuckGrace + stuckOverload + 2*stuckCheckEvery + drainBeforeRestart)

				restarted := false
				select {
				case <-server.RestartNeeded():
					restarted = true
				default:
				}
				assert.Equal(t, tt.wantRestart, restarted)
				assert.Equal(t, tt.wantRestart, server.lobby.isDraining(), "a restart drains first")
				assert.Equal(t, tt.wantWarning, strings.Contains(logs.String(), "restart the server, or set RestartWhenStuck"))
			})
		})
	}
}

func TestNothingIsWatchedBeforeTheGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeProcess{}
		saved := readProcess
		readProcess = fake
		t.Cleanup(func() { readProcess = saved })
		release := make(chan struct{})
		blocker := registerFunc{name: "blocker", register: func(r *Registry) {
			Handle(r, msgBlock, func(_ *RoomContext, _ *addMsg) error {
				<-release
				return nil
			})
		}}
		rt := NewRuntime(Config{Logger: slog.New(slog.DiscardHandler), StuckTimeout: time.Second})
		server, err := NewServer(rt, &ServerConfig{
			RoomType:         newTestRoomType(t, RoomConfig{Modules: []Module{blocker}}),
			MemoryLimit:      -1,
			Logger:           slog.New(slog.DiscardHandler),
			Development:      true,
			AllowClientCodes: true,
			RestartWhenStuck: true,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			close(release)
			server.Close()
		})
		stuckRoom(t, server, "one")
		fake.setMemory(1)

		sleepAndSettle(stuckGrace)

		assert.Zero(t, server.stuck.running.Load(), "not counted until the grace has passed")
		sleepAndSettle(2 * time.Second)
		assert.Equal(t, int32(1), server.stuck.running.Load())
		assert.False(t, server.lobby.isDraining(), "and not overloaded for a minute yet")
	})
}

func TestListenAndServeReturnsErrRestartNeededAfterAShutdown(t *testing.T) {
	server, err := newQuietServer(t, &ServerConfig{Development: true})
	require.NoError(t, err)
	var listen net.ListenConfig
	listener, err := listen.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- server.serve(context.Background(), listener, nil, &ListenConfig{}, nil) }()

	close(server.stuck.restartNeeded) // what restartSoon does after draining

	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrRestartNeeded)
		assert.True(t, server.lobby.isDraining(), "it shut down like a deploy")
	case <-time.After(testTimeout):
		require.FailNow(t, "serve didn't return")
	}
}
