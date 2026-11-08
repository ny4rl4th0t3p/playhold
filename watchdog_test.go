package playhold

import (
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

var blockingKey = NewKey[counterState]("blocking")

// savedValueIn returns what the store holds for savedModule, in the only room saved.
func savedValueIn(t *testing.T, records store.Store) int32 {
	t.Helper()
	rooms, err := records.LoadRooms(t.Context())
	require.NoError(t, err)
	require.Len(t, rooms, 1)
	snapshot := &wire.RoomSnapshot{}
	require.NoError(t, proto.Unmarshal(rooms[0].Data, snapshot))
	module := findModule(snapshot, "saved")
	require.NotNil(t, module)
	state := &addMsg{}
	require.NoError(t, proto.Unmarshal(module.GetState(), state))
	return state.GetValue()
}

func TestNothingGetsOutOfARoomAbandonedMidTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		var block atomic.Bool
		release := make(chan struct{})
		// Its save runs game code after the turn's check for being stuck, which is where the watchdog can strike.
		blocking := registerFunc{name: "blocking", register: func(r *Registry) {
			State(r, blockingKey, func() *counterState { return &counterState{} })
			Snapshot(r, blockingKey, 1,
				func(_ *counterState) *addMsg {
					if block.Load() {
						<-release
					}
					return &addMsg{}
				},
				func(_ *addMsg) *counterState { return &counterState{} },
			)
		}}
		records := memory.New()
		l, _ := restoreLobby(t, records, RoomConfig{Modules: []Module{viewOfSaved, blocking}}, Config{StuckTimeout: timeout})
		room := enterRoom(t, l, testCode)
		sink := &fakeSink{}
		seatViewer(t, room, "alice", sink)
		addTo(t, room, 3)
		synctest.Wait()
		seen := len(sink.received())

		block.Store(true)
		require.NoError(t, room.dispatch("alice", "saved", msgAdd, mustMarshal(t, &addMsg{Value: 5}), nil))
		time.Sleep(timeout)
		synctest.Wait()
		require.True(t, room.closed(), "the watchdog gave up while the save was running")

		block.Store(false) // so the copy can save
		close(release)
		synctest.Wait()

		assert.Len(t, sink.received(), seen, "the abandoned room sent no view")
		assert.Equal(t, int32(3), savedValueIn(t, records), "the abandoned room's save didn't overwrite the copy's")
		assert.Equal(t, 3, valueIn(t, enterRoom(t, l, testCode)), "the copy came back from the last save that got out")
	})
}

func mustMarshal(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(msg)
	require.NoError(t, err)
	return data
}

func TestSlowTurnsAreLogged(t *testing.T) {
	const msgSlow uint32 = 7
	tests := []struct {
		name     string
		budget   Timeout
		takes    time.Duration
		wantSlow bool
	}{
		{name: "over the budget", budget: Timeout{After: 50 * time.Millisecond}, takes: 80 * time.Millisecond, wantSlow: true},
		{name: "within the budget", budget: Timeout{After: 50 * time.Millisecond}, takes: 20 * time.Millisecond},
		{name: "over the default", takes: 150 * time.Millisecond, wantSlow: true},
		{name: "off", budget: Timeout{Off: true}, takes: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				logs := &lockedBuffer{}
				rt := NewRuntime(Config{Logger: slog.New(slog.NewTextHandler(logs, nil)), SlowTurn: tt.budget})
				t.Cleanup(func() { stopProcess(t, rt) })
				slow := registerFunc{name: "slow", register: func(r *Registry) {
					Handle(r, msgSlow, func(_ *RoomContext, _ *addMsg) error {
						time.Sleep(tt.takes)
						return nil
					})
				}}
				room, err := rt.CreateRoom(newTestRoomType(t, RoomConfig{Modules: []Module{slow}}))
				require.NoError(t, err)

				require.NoError(t, dispatchAndWait(t, room, "slow", msgSlow, &addMsg{}))

				if tt.wantSlow {
					assert.Contains(t, logs.String(), "slow turn")
					assert.Contains(t, logs.String(), "module=slow message_id=7")
					return
				}
				assert.NotContains(t, logs.String(), "slow turn")
			})
		})
	}
}
