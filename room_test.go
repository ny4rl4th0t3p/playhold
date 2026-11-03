package playhold

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests run inside synctest bubbles. A goroutine still running when its test ends, such as a room that was
// never closed, makes the test fail with a deadlock. Time is fake, so timeouts don't slow the tests down.

const testTimeout = 5 * time.Second

// newTestRuntime returns a Runtime that shuts down when the test ends. Call it inside a bubble.
func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt := NewRuntime(Config{Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		require.NoError(t, rt.Shutdown(ctx))
	})
	return rt
}

func newTestRoomType(t *testing.T, cfg RoomConfig) *RoomType {
	t.Helper()
	typ, err := NewRoomType(cfg)
	require.NoError(t, err)
	return typ
}

func newTestRoom(t *testing.T, rt *Runtime, cfg RoomConfig) *Room {
	t.Helper()
	room, err := rt.CreateRoom(newTestRoomType(t, cfg))
	require.NoError(t, err)
	return room
}

// waitFor fails the test if ch is not closed in time.
func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testTimeout):
		require.FailNow(t, "timed out waiting")
	}
}

func TestRoomHandlesInputsInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{})

		const count = 100
		got := make([]int, 0, count)
		finished := make(chan struct{})
		for i := range count {
			err := room.submit(input{run: func(_ *RoomContext) error {
				got = append(got, i)
				if i == count-1 {
					close(finished)
				}
				return nil
			}})
			require.NoError(t, err)
		}
		waitFor(t, finished)

		want := make([]int, count)
		for i := range want {
			want[i] = i
		}
		assert.Equal(t, want, got)
	})
}

func TestRoomRejectsInputWhenQueueIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{QueueSize: 2})

		started := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		noop := func(_ *RoomContext) error { return nil }

		// Hold the room's goroutine, so later inputs stay in the queue.
		require.NoError(t, room.submit(input{run: func(_ *RoomContext) error {
			close(started)
			<-release
			return nil
		}}))
		waitFor(t, started)

		require.NoError(t, room.submit(input{run: noop}))
		require.NoError(t, room.submit(input{run: func(_ *RoomContext) error {
			close(finished)
			return nil
		}}))
		require.ErrorIs(t, room.submit(input{run: noop}), ErrQueueFull)

		// The room keeps running and handles what it queued.
		close(release)
		waitFor(t, finished)
	})
}

func TestRoomReportsHandlerResultToSender(t *testing.T) {
	errRule := errors.New("not your turn")

	tests := []struct {
		name string
		err  error
	}{
		{name: "success", err: nil},
		{name: "handler error", err: errRule},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				room := newTestRoom(t, newTestRuntime(t), RoomConfig{})

				results := make(chan error, 1)
				require.NoError(t, room.submit(input{
					run:    func(_ *RoomContext) error { return tt.err },
					result: func(err error) { results <- err },
				}))

				select {
				case err := <-results:
					assert.ErrorIs(t, err, tt.err)
				case <-time.After(testTimeout):
					require.FailNow(t, "timed out waiting for the result")
				}

				// A handler error doesn't close the room.
				synctest.Wait()
				select {
				case <-room.Done():
					assert.Fail(t, "room closed after a handler returned")
				default:
				}
			})
		})
	}
}

func TestRoomContextIdentifiesRoomAndPlayer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{})

		finished := make(chan struct{})
		var gotRoom RoomID
		var gotPlayer PlayerID
		require.NoError(t, room.submit(input{player: "alice", run: func(rc *RoomContext) error {
			gotRoom, gotPlayer = rc.RoomID(), rc.Player()
			close(finished)
			return nil
		}}))
		waitFor(t, finished)

		assert.Equal(t, room.ID(), gotRoom)
		assert.Equal(t, PlayerID("alice"), gotPlayer)
	})
}

func TestRoomPanicClosesOnlyThatRoom(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newTestRuntime(t)
		bad := newTestRoom(t, rt, RoomConfig{})
		good := newTestRoom(t, rt, RoomConfig{})

		require.NoError(t, bad.submit(input{run: func(_ *RoomContext) error { panic("boom") }}))
		waitFor(t, bad.Done())
		assert.Equal(t, ClosePanic, bad.Wait())

		_, stillListed := rt.Room(bad.ID())
		assert.False(t, stillListed)
		require.ErrorIs(t, bad.submit(input{run: func(_ *RoomContext) error { return nil }}), ErrRoomClosed)

		finished := make(chan struct{})
		require.NoError(t, good.submit(input{run: func(_ *RoomContext) error {
			close(finished)
			return nil
		}}))
		waitFor(t, finished)
	})
}

func TestRoomCloseHookReceivesReason(t *testing.T) {
	tests := []struct {
		name    string
		trigger func(room *Room, rt *Runtime)
		want    CloseReason
	}{
		{
			name:    "closed from outside",
			trigger: func(room *Room, _ *Runtime) { room.Close() },
			want:    CloseRequested,
		},
		{
			name: "closed by a handler",
			trigger: func(room *Room, _ *Runtime) {
				_ = room.submit(input{run: func(rc *RoomContext) error {
					rc.CloseRoom()
					return nil
				}})
			},
			want: CloseRequested,
		},
		{
			name: "handler panic",
			trigger: func(room *Room, _ *Runtime) {
				_ = room.submit(input{run: func(_ *RoomContext) error { panic("boom") }})
			},
			want: ClosePanic,
		},
		{
			name: "runtime shutdown",
			trigger: func(_ *Room, rt *Runtime) {
				ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
				defer cancel()
				_ = rt.Shutdown(ctx)
			},
			want: CloseShutdown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt := newTestRuntime(t)
				reasons := make(chan CloseReason, 1)
				room := newTestRoom(t, rt, RoomConfig{
					Close: func(_ *RoomContext, reason CloseReason) { reasons <- reason },
				})

				tt.trigger(room, rt)
				waitFor(t, room.Done())

				assert.Equal(t, tt.want, room.Wait())
				assert.Equal(t, tt.want, <-reasons)
			})
		})
	}
}

func TestRoomSurvivesPanicInCloseHook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{
			Close: func(_ *RoomContext, _ CloseReason) { panic("boom") },
		})

		room.Close()
		waitFor(t, room.Done())

		assert.Equal(t, CloseRequested, room.Wait())
	})
}

func TestCloseIsSafeToRepeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{})

		room.Close()
		room.Close()
		waitFor(t, room.Done())
		room.Close()

		assert.Equal(t, CloseRequested, room.Wait())
	})
}

func TestCreateRoomInitFailure(t *testing.T) {
	errInit := errors.New("bad config")

	tests := []struct {
		name string
		init func(rc *RoomContext) error
	}{
		{name: "error", init: func(_ *RoomContext) error { return errInit }},
		{name: "panic", init: func(_ *RoomContext) error { panic("boom") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt := newTestRuntime(t)
				closeCalled := false

				room, err := rt.CreateRoom(newTestRoomType(t, RoomConfig{
					Init:  tt.init,
					Close: func(_ *RoomContext, _ CloseReason) { closeCalled = true },
				}))
				require.Error(t, err)
				assert.Nil(t, room)
				assert.False(t, closeCalled)
				assert.Empty(t, rt.rooms)
			})
		})
	}
}

func TestRoomIDsAreNeverReused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newTestRuntime(t)

		first := newTestRoom(t, rt, RoomConfig{})
		first.Close()
		waitFor(t, first.Done())
		second := newTestRoom(t, rt, RoomConfig{})

		assert.NotEqual(t, first.ID(), second.ID())
		_, listed := rt.Room(second.ID())
		assert.True(t, listed)
	})
}

func TestNewRoomIDIsUniqueAcrossGoroutines(t *testing.T) {
	const (
		generators = 8
		perWorker  = 1000
	)
	results := make([][]RoomID, generators)

	var generating sync.WaitGroup
	for g := range generators {
		generating.Go(func() {
			ids := make([]RoomID, perWorker)
			for i := range ids {
				ids[i] = newRoomID()
			}
			results[g] = ids
		})
	}
	generating.Wait()

	seen := make(map[RoomID]struct{}, generators*perWorker)
	for _, ids := range results {
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	assert.Len(t, seen, generators*perWorker)
}

func TestNewRoomIDSortsByCreationTime(t *testing.T) {
	first := newRoomID()
	second := newRoomID()

	assert.Less(t, string(first), string(second))
}

func TestShutdownRefusesNewRoomsAndCanRepeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newTestRuntime(t)
		room := newTestRoom(t, rt, RoomConfig{})

		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		require.NoError(t, rt.Shutdown(ctx))
		require.NoError(t, rt.Shutdown(ctx))

		assert.Equal(t, CloseShutdown, room.Wait())
		_, err := rt.CreateRoom(newTestRoomType(t, RoomConfig{}))
		require.ErrorIs(t, err, ErrRuntimeClosed)
	})
}

func TestShutdownGivesUpOnStuckRoom(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newTestRuntime(t)
		room := newTestRoom(t, rt, RoomConfig{})

		started := make(chan struct{})
		release := make(chan struct{})
		require.NoError(t, room.submit(input{run: func(_ *RoomContext) error {
			close(started)
			<-release
			return nil
		}}))
		waitFor(t, started)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.ErrorIs(t, rt.Shutdown(ctx), context.DeadlineExceeded)

		// Once released, the room finishes normally.
		close(release)
		waitFor(t, room.Done())
		assert.Equal(t, CloseShutdown, room.Wait())
	})
}

func TestRoomStateIsSafeWithConcurrentSenders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{QueueSize: 1024})

		const (
			senders    = 8
			perSender  = 100
			totalInput = senders * perSender
		)
		var handled sync.WaitGroup
		handled.Add(totalInput)
		count := 0 // owned by the room's goroutine, checked by the race detector

		var sending sync.WaitGroup
		for range senders {
			sending.Go(func() {
				for range perSender {
					assert.NoError(t, room.submit(input{run: func(_ *RoomContext) error {
						count++
						handled.Done()
						return nil
					}}))
				}
			})
		}
		sending.Wait()
		handled.Wait()

		assert.Equal(t, totalInput, count)
	})
}

func TestCloseReasonString(t *testing.T) {
	tests := []struct {
		reason CloseReason
		want   string
	}{
		{CloseRequested, "requested"},
		{CloseShutdown, "shutdown"},
		{ClosePanic, "panic"},
		{CloseInitFailed, "init failed"},
		{CloseReason(0), "unknown (0)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.reason.String())
		})
	}
}
