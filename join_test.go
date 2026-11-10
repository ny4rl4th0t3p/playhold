package playhold

import (
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

func TestOnJoinAttemptDecidesWhoTakesAFreeSeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		errStarted := errors.New("the game has started")
		started := false
		var asked []PlayerID
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{
			MaxPlayers: 3,
			OnJoinAttempt: func(_ *RoomContext, attempt *JoinAttempt) error {
				asked = append(asked, attempt.Player)
				if started {
					return errStarted
				}
				return nil
			},
		})

		require.NoError(t, seatAndWait(t, room, "alice"))
		started = true // read on the room's goroutine, after this test's next seat request reaches it

		require.ErrorIs(t, seatAndWait(t, room, "bob"), errStarted, "the game locked the room")
		require.NoError(t, seatAndWait(t, room, "alice"), "a player with a seat is never asked again")
		assert.Equal(t, []PlayerID{"alice", "bob"}, asked)
	})
}

func TestOnJoinAttemptRunsAfterTheRoomsOwnLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		asked := 0
		room := newTestRoom(t, newTestRuntime(t), RoomConfig{
			MaxPlayers: 1,
			OnJoinAttempt: func(_ *RoomContext, _ *JoinAttempt) error {
				asked++
				return nil
			},
		})
		require.NoError(t, seatAndWait(t, room, "alice"))

		require.ErrorIs(t, seatAndWait(t, room, "bob"), ErrRoomFull)
		assert.Equal(t, 1, asked, "a full room doesn't ask the game")
	})
}

func TestJoinRefusalsReachTheClient(t *testing.T) {
	errRule := errors.New("only friends")
	tests := []struct {
		name   string
		refuse error
		want   wire.ErrorCode
	}{
		{name: "the room is full, as the game says", refuse: ErrRoomFull, want: wire.ErrorCode_ERROR_CODE_ROOM_FULL},
		{name: "any other refusal", refuse: errRule, want: wire.ErrorCode_ERROR_CODE_REJECTED},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := startGameServer(t, RoomConfig{OnJoinAttempt: func(_ *RoomContext, _ *JoinAttempt) error { return tt.refuse }}, 0)
			ws, _ := newGuest(t, url)

			sendFrame(t, ws, joinFrame(testCode))

			assert.Equal(t, tt.want, readServerFrame(t, ws).GetError().GetCode())
		})
	}
}

func TestByDefaultTheServerMakesTheCodes(t *testing.T) {
	server, err := NewServer(newTestRuntime(t), &ServerConfig{
		RoomType:    newTestRoomType(t, RoomConfig{}),
		MemoryLimit: -1,
		Logger:      slog.New(slog.DiscardHandler),
		Development: true,
		Admission:   Admission{Off: true},
	})
	require.NoError(t, err)
	url := startServerFor(t, server)

	alice, _ := newGuest(t, url)
	sendFrame(t, alice, joinFrame(""))
	joined := readServerFrame(t, alice).GetJoined()
	require.NotNil(t, joined, "an empty code makes a room")
	code := joined.GetCode()
	assert.Len(t, code, 26)

	bob, _ := newGuest(t, url)
	joinRoom(t, bob, code) // a friend follows the link

	carol, _ := newGuest(t, url)
	sendFrame(t, carol, joinFrame("my-room"))
	refused := readServerFrame(t, carol).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_ROOM_NOT_FOUND, refused.GetCode(), "a code no room has makes no room")
}
