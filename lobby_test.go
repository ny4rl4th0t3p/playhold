package playhold

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIdle = time.Minute

func newTestLobby(t *testing.T, cfg RoomConfig) *lobby {
	t.Helper()
	return newLobby(newTestRuntime(t), newTestRoomType(t, cfg), testIdle)
}

func enterRoom(t *testing.T, l *lobby, code string) *Room {
	t.Helper()
	room, err := l.enter(code)
	require.NoError(t, err)
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

		_, err := l.enter("abc")

		require.ErrorIs(t, err, errInit)
	})
}
