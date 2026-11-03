package playhold

import (
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seatAndWait seats the player and returns the result.
func seatAndWait(t *testing.T, room *Room, player PlayerID) error {
	t.Helper()
	results := make(chan error, 1)
	require.NoError(t, room.seat(player, nil, func(err error) { results <- err }))
	return <-results
}

func TestSeats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var joined []PlayerID
		module := registerFunc{name: "m", register: func(r *Registry) {
			On(r, func(rc *RoomContext, event PlayerJoined) {
				assert.Equal(t, event.Player, rc.Player())
				joined = append(joined, event.Player)
			})
		}}
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{MaxPlayers: 2, Modules: []Module{module}})

		require.NoError(t, seatAndWait(t, room, "alice"))
		require.NoError(t, seatAndWait(t, room, "bob"))
		require.ErrorIs(t, seatAndWait(t, room, "carol"), ErrRoomFull)

		// A seated player can always come back, even to a full room.
		require.NoError(t, seatAndWait(t, room, "alice"))

		assert.Equal(t, []PlayerID{"alice", "bob"}, joined)
		runInRoom(t, room, func(rc *RoomContext) {
			assert.Equal(t, []PlayerID{"alice", "bob"}, rc.Players())
		})
	})
}

func TestNoSeatLimitByDefault(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{})

		for _, player := range []PlayerID{"alice", "bob", "carol"} {
			require.NoError(t, seatAndWait(t, room, player))
		}
	})
}

func TestPlayersReturnsACopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{})
		require.NoError(t, seatAndWait(t, room, "alice"))

		runInRoom(t, room, func(rc *RoomContext) { rc.Players()[0] = "mallory" })

		runInRoom(t, room, func(rc *RoomContext) { assert.Equal(t, []PlayerID{"alice"}, rc.Players()) })
	})
}
