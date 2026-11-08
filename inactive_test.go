package playhold

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/stretchr/testify/assert"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

const testInactive = 10 * time.Minute // longer than testIdle and testGrace

// inactiveLobby is a lobby that closes rooms after testInactive, like the one a Server makes.
func inactiveLobby(t *testing.T, cfg RoomConfig) (*lobby, store.Store) {
	t.Helper()
	records := memory.New()
	l, _ := restoreLobby(t, records, cfg, Config{})
	l.inactive = testInactive
	return l, records
}

func TestRoomWithAConnectedButSilentPlayerClosesAsInactive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, records := inactiveLobby(t, RoomConfig{})
		room := enterRoom(t, l, testCode)
		seatViewer(t, room, "alice", &fakeSink{})

		sleepAndSettle(testInactive - time.Second)
		assert.False(t, room.closed())

		sleepAndSettle(time.Second)
		assert.True(t, room.closed())
		assert.Equal(t, CloseInactive, room.Wait())
		requireNoRooms(t, records)
		assert.Equal(t, wire.CloseReason_CLOSE_REASON_INACTIVE, wireCloseReason(room.Wait()), "players are told why")
	})
}

func TestMessagesAndJoiningKeepARoomOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _ := inactiveLobby(t, RoomConfig{})
		room := enterRoom(t, l, testCode)
		seatViewer(t, room, "alice", &fakeSink{})

		sleepAndSettle(testInactive - time.Second)
		addTo(t, room, 1)
		sleepAndSettle(testInactive - time.Second)
		seatViewer(t, room, "alice", &fakeSink{}) // coming back counts too
		sleepAndSettle(testInactive - time.Second)
		assert.False(t, room.closed(), "each one started the time over")

		sleepAndSettle(time.Second)
		assert.Equal(t, CloseInactive, room.Wait())
	})
}

func TestTimersAloneDontKeepARoomOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var ticks atomic.Int32
		ticker := registerFunc{name: "ticker", register: func(r *Registry) {
			On(r, func(rc *RoomContext, _ *wrapperspb.BoolValue) {
				ticks.Add(1)
				rc.After(time.Minute, &wrapperspb.BoolValue{})
			})
		}}
		l, _ := inactiveLobby(t, RoomConfig{Modules: []Module{ticker}})
		room := enterRoom(t, l, testCode)
		seatViewer(t, room, "alice", &fakeSink{})
		runInRoom(t, room, func(rc *RoomContext) { rc.After(time.Minute, &wrapperspb.BoolValue{}) })

		sleepAndSettle(testInactive)

		assert.Equal(t, CloseInactive, room.Wait())
		assert.Positive(t, ticks.Load(), "the timers ran meanwhile")
	})
}

func TestInactiveRoomsCanBeKeptOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, _ := inactiveLobby(t, RoomConfig{})
		l.inactive = 0 // what InactiveRoomTimeout{Off: true} gives
		room := enterRoom(t, l, testCode)
		seatViewer(t, room, "alice", &fakeSink{})

		sleepAndSettle(3 * testInactive)

		assert.False(t, room.closed())
	})
}

func TestTimeoutDefaults(t *testing.T) {
	tests := []struct {
		name    string
		timeout Timeout
		want    Timeout
	}{
		{name: "zero uses the default", want: Timeout{After: time.Hour}},
		{name: "a set time is kept", timeout: Timeout{After: time.Minute}, want: Timeout{After: time.Minute}},
		{name: "off stays off", timeout: Timeout{Off: true}, want: Timeout{Off: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.timeout.orDefault(time.Hour))
		})
	}
}
