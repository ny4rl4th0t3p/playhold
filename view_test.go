package playhold

import (
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// fakeSink records the states a player receives.
type fakeSink struct {
	mu     sync.Mutex
	states []*wire.RoomState
}

func (s *fakeSink) SendState(state *wire.RoomState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = append(s.states, state)
}

func (s *fakeSink) received() []*wire.RoomState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.states)
}

// viewText decodes a view made by a test module, which shows a string.
func viewText(t *testing.T, view *wire.ModuleView) string {
	t.Helper()
	text := &wrapperspb.StringValue{}
	require.NoError(t, proto.Unmarshal(view.GetView(), text))
	return text.GetValue()
}

// seatViewer seats the player and waits for the turn to finish, including its views.
func seatViewer(t *testing.T, room *Room, player PlayerID, sink viewSink) {
	t.Helper()
	results := make(chan error, 1)
	require.NoError(t, room.seat(player, sink, func(err error) { results <- err }))
	require.NoError(t, <-results)
	synctest.Wait()
}

// showsPlayer is a module whose view shows who it's for.
func showsPlayer(name string) registerFunc {
	return registerFunc{name: name, register: func(r *Registry) {
		Handle(r, msgAdd, func(_ *RoomContext, _ *addMsg) error { return nil })
		View(r, func(rc *RoomContext) *wrapperspb.StringValue {
			return wrapperspb.String(name + " for " + string(rc.Player()))
		})
	}}
}

// viewOfSaved is a module whose view shows the number in savedModule's state.
var viewOfSaved = registerFunc{name: "view-of-saved", register: func(r *Registry) {
	View(r, func(rc *RoomContext) *wrapperspb.StringValue {
		return wrapperspb.String(strconv.Itoa(savedKey.Get(rc).value))
	})
}}

// savedView reads viewOfSaved from a state frame.
func savedView(t *testing.T, frame *wire.ServerFrame) string {
	t.Helper()
	views := frame.GetState().GetViews()
	require.Len(t, views, 1, "got %v", frame)
	return viewText(t, views[0])
}

func TestEachPlayerGetsTheirOwnView(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{showsPlayer("cards")}})
		alice, bob := &fakeSink{}, &fakeSink{}
		seatViewer(t, room, "alice", alice)
		seatViewer(t, room, "bob", bob)

		require.NoError(t, dispatchAndWait(t, room, "cards", msgAdd, &addMsg{}))
		synctest.Wait()

		last := func(s *fakeSink) *wire.ModuleView { return s.received()[len(s.received())-1].GetViews()[0] }
		assert.Equal(t, "cards for alice", viewText(t, last(alice)))
		assert.Equal(t, "cards for bob", viewText(t, last(bob)))
	})
}

func TestOneFramePerTurnWithEveryModulesView(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hidden := registerFunc{name: "hidden", register: func(r *Registry) {
			View(r, func(_ *RoomContext) *wrapperspb.StringValue { return nil })
		}}
		silent := registerFunc{name: "silent", register: func(_ *Registry) {}}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{
			Modules: []Module{showsPlayer("first"), hidden, silent, showsPlayer("second")},
		})
		alice := &fakeSink{}
		seatViewer(t, room, "alice", alice)
		require.Len(t, alice.received(), 1, "joining sends a full view")

		require.NoError(t, dispatchAndWait(t, room, "first", msgAdd, &addMsg{}))
		synctest.Wait()

		states := alice.received()
		require.Len(t, states, 2, "one frame per turn")
		views := states[1].GetViews()
		require.Len(t, views, 2)
		assert.Equal(t, "first", views[0].GetModule())
		assert.Equal(t, "second", views[1].GetModule())
	})
}

func TestViewsAreBuiltAfterEventsAreDelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		text := "before"
		module := registerFunc{name: "m", register: func(r *Registry) {
			Handle(r, msgAdd, func(rc *RoomContext, _ *addMsg) error {
				rc.Emit(firstEvent{})
				return nil
			})
			On(r, func(_ *RoomContext, _ firstEvent) { text = "after" })
			View(r, func(_ *RoomContext) *wrapperspb.StringValue { return wrapperspb.String(text) })
		}}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{module}})
		alice := &fakeSink{}
		seatViewer(t, room, "alice", alice)

		require.NoError(t, dispatchAndWait(t, room, "m", msgAdd, &addMsg{}))
		synctest.Wait()

		states := alice.received()
		assert.Equal(t, "after", viewText(t, states[len(states)-1].GetViews()[0]))
	})
}

func TestNewSinkReplacesTheOldOneAndGetsAFullView(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{showsPlayer("cards")}})
		old, newest := &fakeSink{}, &fakeSink{}
		seatViewer(t, room, "alice", old)

		seatViewer(t, room, "alice", newest)
		require.Len(t, newest.received(), 1)
		sentToOld := len(old.received())

		// The replaced connection ending must not stop views to the new one.
		room.removeViewer("alice", old)
		require.NoError(t, dispatchAndWait(t, room, "cards", msgAdd, &addMsg{}))
		synctest.Wait()

		assert.Len(t, old.received(), sentToOld)
		assert.Len(t, newest.received(), 2)

		room.removeViewer("alice", newest)
		require.NoError(t, dispatchAndWait(t, room, "cards", msgAdd, &addMsg{}))
		synctest.Wait()

		assert.Len(t, newest.received(), 2, "a removed sink gets nothing, and the player keeps the seat")
	})
}

func TestViewMistakesCloseOnlyThatRoom(t *testing.T) {
	tests := []struct {
		name string
		view func(rc *RoomContext) *wrapperspb.StringValue
	}{
		{name: "panics", view: func(_ *RoomContext) *wrapperspb.StringValue { panic("boom") }},
		{
			name: "emits an event",
			view: func(rc *RoomContext) *wrapperspb.StringValue {
				rc.Emit(firstEvent{})
				return nil
			},
		},
		{
			name: "can't be encoded",
			view: func(_ *RoomContext) *wrapperspb.StringValue { return wrapperspb.String("\xff") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				module := registerFunc{name: "m", register: func(r *Registry) { View(r, tt.view) }}
				rt := newTestRuntime(t)
				room := newTestRoom(t, rt, RoomConfig{Modules: []Module{module}})
				other := newTestRoom(t, rt, RoomConfig{})

				require.NoError(t, room.seat("alice", &fakeSink{}, nil))
				waitFor(t, room.Done())

				assert.Equal(t, ClosePanic, room.Wait())
				runInRoom(t, other, func(_ *RoomContext) {})
			})
		})
	}
}

func TestViewWiringMistakes(t *testing.T) {
	show := func(_ *RoomContext) *wrapperspb.StringValue { return nil }

	tests := []struct {
		name     string
		register func(r *Registry)
		want     string
	}{
		{
			name:     "nil view",
			register: func(r *Registry) { View[wrapperspb.StringValue](r, nil) },
			want:     "no function",
		},
		{
			name: "two views",
			register: func(r *Registry) {
				View(r, show)
				View(r, show)
			},
			want: "registered twice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRoomType(RoomConfig{Modules: []Module{registerFunc{name: "m", register: tt.register}}})

			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestSameSeedGivesTheSameGame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const draws = 20
		seed := [32]byte{1, 2, 3}
		deal := func(rt *Runtime) []int {
			room := newTestRoom(t, rt, RoomConfig{})
			var got []int
			runInRoom(t, room, func(rc *RoomContext) { got = rc.Rand().Perm(draws) })
			return got
		}

		first := deal(newConfiguredRuntime(t, Config{NewSeed: func() [32]byte { return seed }}))
		second := deal(newConfiguredRuntime(t, Config{NewSeed: func() [32]byte { return seed }}))
		other := deal(newConfiguredRuntime(t, Config{NewSeed: func() [32]byte { return [32]byte{9} }}))

		assert.Equal(t, first, second)
		assert.NotEqual(t, first, other)
	})
}

func TestRoomsGetDifferentSeedsByDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newTestRuntime(t)
		draw := func() uint64 {
			var got uint64
			runInRoom(t, newTestRoom(t, rt, RoomConfig{}), func(rc *RoomContext) { got = rc.Rand().Uint64() })
			return got
		}

		assert.NotEqual(t, draw(), draw())
	})
}

func TestNowIsTheRoomClockAndStaysFixedForATurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
		var mu sync.Mutex
		ticks := 0
		clock := func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			ticks++
			return start.Add(time.Duration(ticks) * time.Hour)
		}
		var inInit time.Time
		room := newTestRoom(t, newConfiguredRuntime(t, Config{Now: clock}), RoomConfig{
			Init: func(rc *RoomContext) error {
				inInit = rc.Now()
				return nil
			},
		})
		assert.Equal(t, start.Add(time.Hour), inInit)

		var first, again, next time.Time
		runInRoom(t, room, func(rc *RoomContext) {
			first = rc.Now()
			clock()
			again = rc.Now()
		})
		runInRoom(t, room, func(rc *RoomContext) { next = rc.Now() })

		assert.Equal(t, first, again, "the clock only moves between turns")
		assert.True(t, next.After(first))
	})
}

func TestStuckRoomIsClosedAndOthersKeepRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		started := make(chan struct{})
		release := make(chan struct{})
		closeHookRan := false
		module := registerFunc{name: "m", register: func(r *Registry) {
			View(r, func(_ *RoomContext) *wrapperspb.StringValue { return wrapperspb.String("view") })
		}}
		rt := newConfiguredRuntime(t, Config{StuckTimeout: timeout})
		room := newTestRoom(t, rt, RoomConfig{
			Modules: []Module{module},
			Close:   func(_ *RoomContext, _ CloseReason) { closeHookRan = true },
		})
		other := newTestRoom(t, rt, RoomConfig{})
		alice := &fakeSink{}
		seatViewer(t, room, "alice", alice)
		sent := len(alice.received())

		resultCalled := false
		require.NoError(t, room.submit(input{
			run: func(_ *RoomContext) error {
				close(started)
				<-release
				return nil
			},
			result: func(_ error) { resultCalled = true },
		}))
		waitFor(t, started)

		time.Sleep(timeout - time.Millisecond)
		synctest.Wait()
		assert.False(t, room.closed(), "not stuck yet")

		time.Sleep(time.Millisecond)
		synctest.Wait()
		assert.True(t, room.closed())
		assert.Equal(t, CloseStuck, room.Wait())
		_, listed := rt.Room(room.ID())
		assert.False(t, listed)
		require.ErrorIs(t, room.submit(input{run: func(_ *RoomContext) error { return nil }}), ErrRoomClosed)
		runInRoom(t, other, func(_ *RoomContext) {})

		// If the stuck goroutine ever wakes, it ends without reaching anything outside the room.
		close(release)
		synctest.Wait()
		assert.False(t, resultCalled)
		assert.False(t, closeHookRan)
		assert.Len(t, alice.received(), sent)
		assert.Equal(t, CloseStuck, room.Wait())
	})
}

func TestSlowTurnsThatFinishInTimeAreNotStuck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		room := newTestRoom(t, newConfiguredRuntime(t, Config{StuckTimeout: timeout}), RoomConfig{})

		for range 3 {
			runInRoom(t, room, func(_ *RoomContext) { time.Sleep(timeout - time.Millisecond) })
		}
		time.Sleep(2 * timeout) // an idle room is never stuck
		synctest.Wait()

		assert.False(t, room.closed())
	})
}

func TestStuckCloseHookDoesNotHoldTheRoomOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		release := make(chan struct{})
		room := newTestRoom(t, newConfiguredRuntime(t, Config{StuckTimeout: timeout}), RoomConfig{
			Close: func(_ *RoomContext, _ CloseReason) { <-release },
		})
		defer close(release)

		room.Close()
		waitFor(t, room.Done())

		assert.Equal(t, CloseStuck, room.Wait())
	})
}
