package playholdtest_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/playholdtest"
)

// counter is a module small enough to test the harness with: a count players add to, a timer that adds one, and a
// record of who disconnected.
type counter struct {
	version int // its snapshot version; zero saves nothing
}

type counterState struct {
	Count int64
	Dealt int64 // a random number from Init
	Away  []playhold.PlayerID
}

var counterKey = playhold.NewKey[counterState]("counter")

const (
	msgAdd   uint32 = 1
	msgTimer uint32 = 2 // adds one after a minute
	msgPanic uint32 = 3
)

var (
	errTooBig = errors.New("too big")
	errLocked = errors.New("locked")
)

func (counter) Name() string { return "counter" }

func (c counter) Register(r *playhold.Registry) {
	playhold.State(r, counterKey, func() *counterState { return &counterState{} })
	playhold.Handle(r, msgAdd, func(rc *playhold.RoomContext, msg *wrapperspb.Int64Value) error {
		if msg.GetValue() > 10 {
			return errTooBig
		}
		counterKey.Get(rc).Count += msg.GetValue()
		return nil
	})
	playhold.Handle(r, msgTimer, func(rc *playhold.RoomContext, _ *wrapperspb.Int64Value) error {
		rc.After(time.Minute, &wrapperspb.StringValue{Value: "add one"})
		return nil
	})
	playhold.Handle(r, msgPanic, func(_ *playhold.RoomContext, _ *wrapperspb.Int64Value) error { panic("boom") })
	playhold.On(r, func(rc *playhold.RoomContext, _ *wrapperspb.StringValue) { counterKey.Get(rc).Count++ })
	playhold.On(r, func(rc *playhold.RoomContext, event playhold.PlayerDisconnected) {
		state := counterKey.Get(rc)
		state.Away = append(state.Away, event.Player)
	})
	playhold.View(r, func(rc *playhold.RoomContext) *wrapperspb.Int64Value {
		if count := counterKey.Get(rc).Count; count != 0 {
			return wrapperspb.Int64(count)
		}
		return nil // nothing to show yet
	})
	if c.version == 0 {
		return
	}
	playhold.Snapshot(r, counterKey, c.version,
		func(state *counterState) *wrapperspb.Int64Value { return wrapperspb.Int64(state.Count) },
		func(msg *wrapperspb.Int64Value) *counterState { return &counterState{Count: msg.GetValue()} })
	if c.version == 2 {
		// Version 2 counts in tenths.
		playhold.Migrate(r, counterKey, 1, func(old []byte) ([]byte, error) {
			count := &wrapperspb.Int64Value{}
			if err := proto.Unmarshal(old, count); err != nil {
				return nil, err
			}
			return proto.Marshal(wrapperspb.Int64(count.GetValue() * 10))
		})
	}
}

func counterType(t *testing.T, cfg playhold.RoomConfig, version int) *playhold.RoomType {
	t.Helper()
	cfg.Name = "counter"
	cfg.Modules = []playhold.Module{counter{version: version}}
	cfg.Init = func(rc *playhold.RoomContext) error {
		counterKey.Get(rc).Dealt = rc.Rand().Int64()
		return nil
	}
	typ, err := playhold.NewRoomType(cfg)
	require.NoError(t, err)
	return typ
}

func newCounterRoom(t *testing.T, opts playholdtest.Options) *playholdtest.Room {
	t.Helper()
	return playholdtest.NewRoom(t, counterType(t, playhold.RoomConfig{}, 1), opts)
}

func count(p *playholdtest.Player) *wrapperspb.Int64Value {
	return playholdtest.View[wrapperspb.Int64Value](p, "counter")
}

// state reads the counter's state from inside the room.
func state(room *playholdtest.Room) counterState {
	var got counterState
	room.Do(func(rc *playhold.RoomContext) { got = *counterKey.Get(rc) })
	return got
}

// #region views
func TestPlayersGetTheRoomsViewsAfterEachTurn(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		room := playholdtest.NewRoom(t, counterType(t, playhold.RoomConfig{}, 1), playholdtest.Options{})
		alice := room.Join("alice")
		assert.Nil(t, playholdtest.View[wrapperspb.Int64Value](alice, "counter"), "a count of zero shows nothing")

		require.NoError(t, alice.Send("counter", msgAdd, wrapperspb.Int64(2)))
		assert.Equal(t, int64(2), playholdtest.View[wrapperspb.Int64Value](alice, "counter").GetValue())

		bob := room.Join("bob")
		assert.Equal(t, int64(2), playholdtest.View[wrapperspb.Int64Value](bob, "counter").GetValue(),
			"a player who joins gets the room's state at once")
		assert.Len(t, alice.Received(), 3, "a state for her join, her move and his join")
		assert.Len(t, bob.Received(), 1)
	})
}

// #endregion views

// #region errors
func TestHandlersErrorsComeBackAsThemselves(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		room := newCounterRoom(t, playholdtest.Options{})
		alice := room.Join("alice")

		require.ErrorIs(t, alice.Send("counter", msgAdd, wrapperspb.Int64(11)), errTooBig)
		require.ErrorIs(t, alice.Send("counter", 99, wrapperspb.Int64(1)), playhold.ErrUnknownMessage)
		assert.Nil(t, count(alice), "a refused move changes nothing")
	})
}

// #endregion errors

func TestJoinRefusals(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		typ := counterType(t, playhold.RoomConfig{
			MaxPlayers: 2,
			OnJoinAttempt: func(_ *playhold.RoomContext, attempt *playhold.JoinAttempt) error {
				if attempt.Player == "mallory" {
					return errLocked
				}
				return nil
			},
		}, 0)
		room := playholdtest.NewRoom(t, typ, playholdtest.Options{})

		_, err := room.TryJoin("mallory")
		require.ErrorIs(t, err, errLocked)
		room.Join("alice")
		room.Join("bob")
		_, err = room.TryJoin("carol")
		require.ErrorIs(t, err, playhold.ErrRoomFull)
	})
}

// #region advance
func TestAdvanceFiresTimersWhenTheyreDue(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		room := newCounterRoom(t, playholdtest.Options{})
		alice := room.Join("alice")
		require.NoError(t, alice.Send("counter", msgTimer, wrapperspb.Int64(0)))

		room.Advance(time.Minute - time.Second)
		assert.Nil(t, count(alice), "not yet")

		room.Advance(time.Second)
		assert.Equal(t, int64(1), count(alice).GetValue())
	})
}

// #endregion advance

func TestADisconnectedPlayerHasTheGracePeriodToComeBack(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		room := newCounterRoom(t, playholdtest.Options{})
		room.Join("alice")
		bob := room.Join("bob")

		bob.Disconnect()
		room.Advance(time.Minute - time.Second)
		assert.Empty(t, state(room).Away, "the default grace period is a minute")

		room.Advance(time.Second)
		assert.Equal(t, []playhold.PlayerID{"bob"}, state(room).Away)

		back := room.Join("bob")
		assert.Equal(t, playhold.PlayerID("bob"), back.ID(), "he keeps his seat")
	})
}

func TestAnUnusedRoomClosesAsOnAServer(t *testing.T) {
	tests := []struct {
		name     string
		inactive playhold.Timeout
		want     bool
	}{
		{name: "by default after 30 minutes", want: true},
		{name: "not with the timeout off", inactive: playhold.Timeout{Off: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			playholdtest.Run(t, func(t *testing.T) {
				room := newCounterRoom(t, playholdtest.Options{InactiveRoomTimeout: tt.inactive})
				room.Join("alice")

				room.Advance(time.Hour)

				reason, closed := room.Closed()
				require.Equal(t, tt.want, closed)
				if closed {
					assert.Equal(t, playhold.CloseInactive, reason)
				}
			})
		})
	}
}

// #region do
func TestDoSetsUpTheRoom(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		room := newCounterRoom(t, playholdtest.Options{})
		alice := room.Join("alice")

		room.Do(func(rc *playhold.RoomContext) { counterKey.Get(rc).Count = 7 })

		assert.Equal(t, int64(7), count(alice).GetValue(), "views go out after it")
	})
}

// #endregion do

func TestARestartedRoomCarriesOn(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		room := newCounterRoom(t, playholdtest.Options{})
		alice := room.Join("alice")
		require.NoError(t, alice.Send("counter", msgAdd, wrapperspb.Int64(3)))
		require.NoError(t, alice.Send("counter", msgTimer, wrapperspb.Int64(0)))
		before := len(alice.Received())

		room.Restart()

		back := room.Join("alice")
		assert.Equal(t, int64(3), count(back).GetValue(), "the count was saved")
		room.Advance(time.Minute)
		assert.Equal(t, int64(4), count(back).GetValue(), "and so was the timer")
		assert.Len(t, alice.Received(), before, "the connection from before the restart gets nothing more")
	})
}

// #region restart
func TestADeployRunsTheMigration(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		room := newCounterRoom(t, playholdtest.Options{})
		alice := room.Join("alice")
		require.NoError(t, alice.Send("counter", msgAdd, wrapperspb.Int64(3)))

		room.RestartAs(counterType(t, playhold.RoomConfig{}, 2)) // version 2 counts in tenths

		assert.Equal(t, int64(30), count(room.Join("alice")).GetValue())
	})
}

// #endregion restart

func TestTheSameSeedDealsTheSameGame(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		seed := playholdtest.Options{Seed: [32]byte{1}}
		first := state(newCounterRoom(t, seed)).Dealt

		assert.Equal(t, first, state(newCounterRoom(t, seed)).Dealt)
		assert.NotEqual(t, first, state(newCounterRoom(t, playholdtest.Options{Seed: [32]byte{2}})).Dealt)
	})
}

func TestATurnThatPanicsClosesTheRoom(t *testing.T) {
	playholdtest.Run(t, func(t *testing.T) {
		room := newCounterRoom(t, playholdtest.Options{})
		alice := room.Join("alice")

		require.ErrorIs(t, alice.Send("counter", msgPanic, wrapperspb.Int64(0)), playhold.ErrRoomClosed)

		reason, closed := room.Closed()
		require.True(t, closed)
		assert.Equal(t, playhold.ClosePanic, reason)
	})
}
