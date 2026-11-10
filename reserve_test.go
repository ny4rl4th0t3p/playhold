package playhold

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testReserveFor = 30 * time.Second

// matchEvents returns a module that records the room's PlayerJoined and MatchReady events, in order, and the record.
func matchEvents() (module Module, events *[]any) {
	events = new([]any)
	module = registerFunc{name: "m", register: func(r *Registry) {
		On(r, func(_ *RoomContext, event PlayerJoined) { *events = append(*events, event) })
		On(r, func(_ *RoomContext, event MatchReady) { *events = append(*events, event) })
	}}
	return module, events
}

func TestAWholeMatchedGroupIsReadyAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		module, events := matchEvents()
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{MaxPlayers: 3, Modules: []Module{module}})
		room.reserve([]PlayerID{"alice", "bob"}, testReserveFor, 0)

		require.NoError(t, seatAndWait(t, room, "alice"))
		require.NoError(t, seatAndWait(t, room, "bob"))

		assert.Equal(t, []any{
			PlayerJoined{Player: "alice"},
			PlayerJoined{Player: "bob"},
			MatchReady{Players: []PlayerID{"alice", "bob"}},
		}, *events)

		time.Sleep(testReserveFor + time.Second)
		synctest.Wait()
		assert.False(t, room.closed(), "the reservation ending does nothing once the group is complete")
		assert.Len(t, *events, 3, "MatchReady goes out once")
	})
}

func TestAMatchWithPlayersMissingWhenTheReservationEnds(t *testing.T) {
	tests := []struct {
		name       string
		minPlayers int
		joining    []PlayerID
		wantReady  *MatchReady // nil means the room closes as match incomplete
	}{
		{name: "enough of them came", minPlayers: 2, joining: []PlayerID{"alice", "bob"},
			wantReady: &MatchReady{Players: []PlayerID{"alice", "bob"}, Missing: 1}},
		{name: "too few came", minPlayers: 2, joining: []PlayerID{"alice"}},
		{name: "nobody came", minPlayers: 2},
		{name: "the whole group was needed", joining: []PlayerID{"alice", "bob"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				module, events := matchEvents()
				room := newTestRoom(t, newTestRuntime(t), RoomConfig{MaxPlayers: 3, Modules: []Module{module}})
				room.reserve([]PlayerID{"alice", "bob", "carol"}, testReserveFor, tt.minPlayers)
				for _, player := range tt.joining {
					require.NoError(t, seatAndWait(t, room, player))
				}

				time.Sleep(testReserveFor + time.Second)
				synctest.Wait()

				if tt.wantReady == nil {
					assert.Equal(t, CloseMatchIncomplete, room.Wait())
					for _, event := range *events {
						assert.IsType(t, PlayerJoined{}, event, "no MatchReady")
					}
					return
				}
				require.False(t, room.closed())
				assert.Equal(t, *tt.wantReady, (*events)[len(*events)-1])
				require.NoError(t, seatAndWait(t, room, "dave"), "the missing player's seat is free")
			})
		})
	}
}

func TestOnlyMatchedPlayersTakeReservedSeats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{MaxPlayers: 3})
		room.reserve([]PlayerID{"alice", "bob"}, testReserveFor, 1)

		require.ErrorIs(t, seatAndWait(t, room, "dave"), ErrRoomFull)
		require.NoError(t, seatAndWait(t, room, "alice"))
		require.ErrorIs(t, seatAndWait(t, room, "dave"), ErrRoomFull, "MinPlayers counts only when the reservation ends")
	})
}
