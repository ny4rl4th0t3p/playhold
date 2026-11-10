package playhold

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testIdle     = time.Minute
	testMaxRooms = 1000
)

func newTestLobby(t *testing.T, cfg RoomConfig) *lobby {
	t.Helper()
	return newLobby(newTestRuntime(t), newTestRoomType(t, cfg), testIdle, testMaxRooms, nil)
}

func TestLobbyRefusesNewRoomsOverTheCapButStillJoinsExistingOnes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLobby(newTestRuntime(t), newTestRoomType(t, RoomConfig{}), testIdle, 2, nil)
		first := enterRoom(t, l, "one")
		enterRoom(t, l, "two")

		_, _, err := l.enter("three")
		require.ErrorIs(t, err, ErrTooManyRooms)

		assert.Same(t, first, enterRoom(t, l, "one"))

		// A room closing makes space for a new one.
		first.Close()
		waitFor(t, first.Done())
		synctest.Wait()
		enterRoom(t, l, "three")
	})
}

func enterRoom(t *testing.T, l *lobby, code string) *Room {
	t.Helper()
	room, got, err := l.enter(code)
	require.NoError(t, err)
	require.Equal(t, code, got)
	return room
}

func TestLobbyMapsCodesToRooms(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLobby(t, RoomConfig{})

		first := enterRoom(t, l, "abc")

		assert.Same(t, first, enterRoom(t, l, "abc"))
		assert.NotSame(t, first, enterRoom(t, l, "xyz"))
	})
}

func TestEmptyRoomClosesAfterTheIdleTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLobby(t, RoomConfig{})
		room := enterRoom(t, l, "abc")

		l.leave("abc", room)
		time.Sleep(testIdle - time.Second)
		synctest.Wait()
		assert.False(t, room.closed())

		time.Sleep(time.Second)
		synctest.Wait()
		assert.True(t, room.closed())
		assert.Equal(t, CloseRequested, room.Wait())

		// The code is free again, and a new room never reuses the old ID.
		next := enterRoom(t, l, "abc")
		assert.NotEqual(t, room.ID(), next.ID())
	})
}

func TestEmptyRoomsCanBeKeptOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLobby(newTestRuntime(t), newTestRoomType(t, RoomConfig{}), 0, testMaxRooms, nil) // what EmptyRoomTimeout{Off: true} gives
		room := enterRoom(t, l, "abc")

		l.leave("abc", room)
		time.Sleep(10 * testIdle)
		synctest.Wait()

		assert.False(t, room.closed())
	})
}

func TestComingBackKeepsAnEmptyRoomOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLobby(t, RoomConfig{})
		room := enterRoom(t, l, "abc")

		l.leave("abc", room)
		time.Sleep(testIdle / 2)
		assert.Same(t, room, enterRoom(t, l, "abc"))

		time.Sleep(2 * testIdle)
		synctest.Wait()
		assert.False(t, room.closed())
	})
}

func TestRoomStaysOpenWhileAnyConnectionRemains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLobby(t, RoomConfig{})
		room := enterRoom(t, l, "abc")
		enterRoom(t, l, "abc")

		l.leave("abc", room)
		time.Sleep(2 * testIdle)
		synctest.Wait()
		assert.False(t, room.closed())
	})
}

func TestRoomThatClosedItselfFreesItsCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLobby(t, RoomConfig{})
		room := enterRoom(t, l, "abc")

		room.Close()
		waitFor(t, room.Done())

		assert.NotSame(t, room, enterRoom(t, l, "abc"))

		// Leaving the old room doesn't touch the new one's count.
		l.leave("abc", room)
		time.Sleep(2 * testIdle)
		synctest.Wait()
		assert.False(t, enterRoom(t, l, "abc").closed())
	})
}

func TestLobbyReportsARoomThatFailsToStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		errInit := errors.New("bad config")
		l := newTestLobby(t, RoomConfig{Init: func(_ *RoomContext) error { return errInit }})

		_, _, err := l.enter("abc")

		require.ErrorIs(t, err, errInit)
	})
}

func TestANewCodeNeverTakesOneInUse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLobby(t, RoomConfig{})
		running := enterRoom(t, l, "RUNNING")
		l.savedCodes = map[string]bool{"SAVED": true} // a room the restore left in the store
		codes := []string{"RUNNING", "SAVED", "FRESH"}
		l.makeCode = func() string {
			code := codes[0]
			codes = codes[1:]
			return code
		}

		room, code, err := l.enter("")

		require.NoError(t, err)
		assert.Equal(t, "FRESH", code)
		assert.NotSame(t, running, room)
	})
}

func TestLobbyMakesCodesAndCanRefuseThoseClientsChoose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLobby(t, RoomConfig{})
		l.clientCodes = false

		room, code, err := l.enter("")
		require.NoError(t, err)
		assert.Len(t, code, 26)
		assert.True(t, validRoomCode(code), "safe to put in a link")
		_, other, err := l.enter("")
		require.NoError(t, err)
		assert.NotEqual(t, code, other, "every empty code is a new room")

		joined, again, err := l.enter(code)
		require.NoError(t, err)
		assert.Same(t, room, joined, "joining by the code works")
		assert.Equal(t, code, again)

		_, _, err = l.enter("my-room")
		require.ErrorIs(t, err, ErrRoomNotFound)
	})
}
