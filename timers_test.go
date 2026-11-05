package playhold

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// firedEvents notes timer events as they arrive. It has a lock because tests read it, then let time pass, and the room
// then adds to it: synctest.Wait orders a read after a write, but nothing orders a write after a read.
type firedEvents struct {
	mu     sync.Mutex
	events []string
}

func (f *firedEvents) add(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *firedEvents) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// recordsFired is a module that notes each timer event, which is a string.
func recordsFired(fired *firedEvents) registerFunc {
	return registerFunc{name: "fired", register: func(r *Registry) {
		On(r, func(_ *RoomContext, event *wrapperspb.StringValue) { fired.add(event.GetValue()) })
	}}
}

// after sets a timer on the room's goroutine.
func after(t *testing.T, room *Room, d time.Duration, name string) TimerID {
	t.Helper()
	var id TimerID
	runInRoom(t, room, func(rc *RoomContext) { id = rc.After(d, wrapperspb.String(name)) })
	return id
}

func passTime(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func TestTimersFireOnTimeAndInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fired := &firedEvents{}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{recordsFired(fired)}})

		after(t, room, 2*time.Minute, "later")
		after(t, room, time.Minute, "first at this time")
		after(t, room, time.Minute, "second at this time")

		passTime(time.Minute - time.Second)
		assert.Empty(t, fired.list())

		passTime(time.Second)
		assert.Equal(t, []string{"first at this time", "second at this time"}, fired.list())

		passTime(time.Minute)
		assert.Equal(t, []string{"first at this time", "second at this time", "later"}, fired.list())
	})
}

func TestSoonerTimerSetLaterStillFiresFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fired := &firedEvents{}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{recordsFired(fired)}})

		after(t, room, time.Hour, "slow")
		after(t, room, time.Minute, "quick")

		passTime(time.Minute)

		assert.Equal(t, []string{"quick"}, fired.list())
	})
}

func TestCanceledTimerDoesNotFire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fired := &firedEvents{}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{recordsFired(fired)}})
		first := after(t, room, time.Minute, "canceled")
		after(t, room, 2*time.Minute, "kept")

		runInRoom(t, room, func(rc *RoomContext) {
			assert.True(t, rc.CancelTimer(first))
			assert.False(t, rc.CancelTimer(first), "it's already gone")
			assert.False(t, rc.CancelTimer(999), "there never was one")
		})
		passTime(2 * time.Minute)

		assert.Equal(t, []string{"kept"}, fired.list())
	})
}

func TestTimerIsANormalTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		adds := registerFunc{name: "adds", register: func(r *Registry) {
			On(r, func(rc *RoomContext, event *addMsg) {
				assert.Empty(t, rc.Player(), "no player started this turn")
				savedKey.Get(rc).value += int(event.GetValue())
			})
		}}
		room, _ := newSavedRoom(t, records, RoomConfig{Modules: []Module{adds, viewOfSaved}})
		alice := &fakeSink{}
		seatViewer(t, room, "alice", alice)
		sent := len(alice.received())

		runInRoom(t, room, func(rc *RoomContext) { rc.After(time.Minute, &addMsg{Value: 5}) })
		synctest.Wait()
		sent = len(alice.received()) - sent // the turn that set the timer sends a view too

		passTime(time.Minute)

		states := alice.received()
		require.Len(t, states, 1+sent+1, "the timer's turn sends a view")
		assert.Equal(t, "5", viewText(t, states[len(states)-1].GetViews()[0]))

		snapshot, value := savedRoom(t, records, room)
		assert.Equal(t, int32(5), value, "the timer's turn saves too")
		assert.Empty(t, snapshot.GetTimers(), "a timer that has fired isn't saved again")
	})
}

func TestTimersCannotBeMisused(t *testing.T) {
	tests := []struct {
		name string
		run  func(rc *RoomContext)
	}{
		{name: "no event", run: func(rc *RoomContext) { rc.After(time.Minute, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				room := newTestRoom(t, newTestRuntime(t), RoomConfig{})

				require.NoError(t, room.submit(input{run: func(rc *RoomContext) error {
					tt.run(rc)
					return nil
				}}))
				waitFor(t, room.Done())

				assert.Equal(t, ClosePanic, room.Wait())
			})
		})
	}
}

func TestViewCannotSetATimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		module := registerFunc{name: "m", register: func(r *Registry) {
			View(r, func(rc *RoomContext) *wrapperspb.StringValue {
				rc.After(time.Minute, wrapperspb.String("from a view"))
				return nil
			})
		}}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{module}})

		require.NoError(t, room.seat("alice", &fakeSink{}, nil))
		waitFor(t, room.Done())

		assert.Equal(t, ClosePanic, room.Wait())
	})
}

func TestTimersAfterARestart(t *testing.T) {
	// A one-minute timer, 20 seconds in, with the server down for the next 30 seconds.
	tests := []struct {
		name   string
		policy TimersAfterRestart
		// firesAfter is how long after the restart it should fire, or zero if it never should.
		firesAfter time.Duration
	}{
		{name: "paused, the default", policy: TimersPause, firesAfter: 40 * time.Second},
		{name: "kept running", policy: TimersKeepRunning, firesAfter: 10 * time.Second},
		{name: "canceled", policy: TimersCancel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				records := memory.New()
				fired := &firedEvents{}
				cfg := RoomConfig{Modules: []Module{recordsFired(fired)}}

				firstLobby, firstRuntime := restoreLobby(t, records, cfg, Config{})
				after(t, enterRoom(t, firstLobby, testCode), time.Minute, "survivor")
				synctest.Wait()
				passTime(20 * time.Second)
				stopProcess(t, firstRuntime)
				passTime(30 * time.Second)

				secondLobby, _ := restoreLobby(t, records, cfg, Config{})
				secondLobby.timers = tt.policy
				require.NoError(t, secondLobby.restoreAll(t.Context(), records))
				enterRoom(t, secondLobby, testCode) // so the empty room stays open

				if tt.firesAfter == 0 {
					passTime(time.Hour)
					assert.Empty(t, fired.list())
					return
				}
				passTime(tt.firesAfter - time.Second)
				assert.Empty(t, fired.list())

				passTime(time.Second)
				assert.Equal(t, []string{"survivor"}, fired.list())
			})
		})
	}
}

func TestKeptTimersDueDuringDowntimeFireOnRestoreInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		fired := &firedEvents{}
		cfg := RoomConfig{Modules: []Module{recordsFired(fired)}}

		firstLobby, firstRuntime := restoreLobby(t, records, cfg, Config{})
		room := enterRoom(t, firstLobby, testCode)
		after(t, room, time.Second, "second")
		after(t, room, 2*time.Second, "third")
		synctest.Wait()
		stopProcess(t, firstRuntime)

		passTime(time.Minute) // the server is down

		secondLobby, _ := restoreLobby(t, records, cfg, Config{})
		secondLobby.timers = TimersKeepRunning
		require.NoError(t, secondLobby.restoreAll(t.Context(), records))
		synctest.Wait()

		assert.Equal(t, []string{"second", "third"}, fired.list())
	})
}

func TestPauseIsNeverLessThanTheTimeLeft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// With no clean shutdown, the last saved time is the room's last turn, 20 seconds before the crash. Writing the
		// snapshot that way here is the same as if the process had died at that point.
		records := memory.New()
		fired := &firedEvents{}
		w := newTestWriter(t, records)
		rt := newTestRuntime(t)
		cfg := RoomConfig{Modules: []Module{recordsFired(&firedEvents{}), savedModule}} // this server's timer doesn't count
		first := newLobby(rt, newTestRoomType(t, cfg), testIdle, testMaxRooms, w)
		after(t, enterRoom(t, first, testCode), time.Minute, "survivor")
		synctest.Wait()
		saved, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		passTime(20 * time.Second)

		other := memory.New()
		require.NoError(t, other.WriteRooms(t.Context(), saved, nil))
		second, _ := restoreLobby(t, other, RoomConfig{Modules: []Module{recordsFired(fired)}}, Config{})
		require.NoError(t, second.restoreAll(t.Context(), other))
		enterRoom(t, second, testCode)

		passTime(time.Minute - time.Second)
		assert.Empty(t, fired.list(), "the player had 40 seconds left, and gets a full minute")

		passTime(time.Second)
		assert.Equal(t, []string{"survivor"}, fired.list())
	})
}

func TestTimerLeft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{})
		id := after(t, room, time.Minute, "timer")

		passTime(20 * time.Second)

		runInRoom(t, room, func(rc *RoomContext) {
			left, pending := rc.TimerLeft(id)
			assert.True(t, pending)
			assert.Equal(t, 40*time.Second, left)

			rc.CancelTimer(id)
			_, pending = rc.TimerLeft(id)
			assert.False(t, pending)
		})
	})
}

func TestCrashIsNotSilent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs lockedBuffer
		records := memory.New()
		w := newTestWriter(t, records)
		rt := NewRuntime(Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			assert.NoError(t, rt.Shutdown(ctx))
		})
		crashing := registerFunc{name: "crashing", register: func(r *Registry) {
			On(r, func(_ *RoomContext, _ *wrapperspb.StringValue) { panic("boom") })
		}}
		typ := newTestRoomType(t, RoomConfig{Modules: []Module{crashing, savedModule}})
		l := newLobby(rt, typ, testIdle, testMaxRooms, w)
		after(t, enterRoom(t, l, testCode), time.Second, "poison")
		synctest.Wait()

		passTime(time.Second)

		text := logs.String()
		assert.Contains(t, text, "timer_event=google.protobuf.StringValue", "the crash names the timer that caused it")
		assert.Contains(t, text, "room crashed, restored from its last snapshot", "each restart is reported")
		assert.Contains(t, text, "room keeps crashing, closing it", "and so is giving up")
	})
}

// lockedBuffer collects logs from several goroutines.
type lockedBuffer struct {
	mu   sync.Mutex
	logs bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.logs.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.logs.String()
}

func TestTimerCanStillBeCanceledAfterARestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		fired := &firedEvents{}
		cfg := RoomConfig{Modules: []Module{recordsFired(fired)}}

		firstLobby, firstRuntime := restoreLobby(t, records, cfg, Config{})
		id := after(t, enterRoom(t, firstLobby, testCode), time.Minute, "canceled")
		synctest.Wait()
		stopProcess(t, firstRuntime)

		secondLobby, _ := restoreLobby(t, records, cfg, Config{})
		require.NoError(t, secondLobby.restoreAll(t.Context(), records))
		room := enterRoom(t, secondLobby, testCode)

		var canceled bool
		runInRoom(t, room, func(rc *RoomContext) { canceled = rc.CancelTimer(id) })
		assert.True(t, canceled)
		assert.NotEqual(t, id, after(t, room, time.Hour, "new"), "IDs carry on from where they were")

		passTime(time.Minute)
		assert.Empty(t, fired.list())
	})
}

func TestTimerThatCrashesTheRoomHitsTheRestartLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		attempts := 0
		crashing := registerFunc{name: "crashing", register: func(r *Registry) {
			On(r, func(_ *RoomContext, _ *wrapperspb.StringValue) {
				attempts++
				panic("boom")
			})
		}}
		l, _ := restoreLobby(t, records, RoomConfig{Modules: []Module{crashing}}, Config{})
		after(t, enterRoom(t, l, testCode), time.Second, "poison")
		synctest.Wait()

		passTime(time.Second)

		assert.Equal(t, maxRestarts+1, attempts)
		assert.Empty(t, l.rooms)
		requireNoRooms(t, records)
	})
}

func TestRoomWithATimerEventThisServerDoesNotKnowIsNotRestored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		records := memory.New()
		fired := &firedEvents{}
		cfg := RoomConfig{Modules: []Module{recordsFired(fired)}}

		firstLobby, firstRuntime := restoreLobby(t, records, cfg, Config{})
		after(t, enterRoom(t, firstLobby, testCode), time.Hour, "event")
		synctest.Wait()
		stopProcess(t, firstRuntime)

		rooms, err := records.LoadRooms(t.Context())
		require.NoError(t, err)
		snapshot := &wire.RoomSnapshot{}
		require.NoError(t, proto.Unmarshal(rooms[0].Data, snapshot))
		snapshot.GetTimers()[0].EventType = "game.v1.RemovedLongAgo"
		rooms[0].Data, err = proto.Marshal(snapshot)
		require.NoError(t, err)
		require.NoError(t, records.WriteRooms(t.Context(), []store.RoomRecord{rooms[0]}, nil))

		secondLobby, _ := restoreLobby(t, records, cfg, Config{})
		require.NoError(t, secondLobby.restoreAll(t.Context(), records))

		assert.Empty(t, secondLobby.rooms)
	})
}
