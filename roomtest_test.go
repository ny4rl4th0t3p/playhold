package playhold

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/roomtest"
)

// savedCount is a module whose state is saved, for the harness's restarts.
type savedCount struct{}

type savedCountState struct{ count int64 }

var savedCountKey = NewKey[savedCountState]("saved-count")

func (savedCount) Name() string { return "saved-count" }

func (savedCount) Register(r *Registry) {
	State(r, savedCountKey, func() *savedCountState { return &savedCountState{} })
	Snapshot(r, savedCountKey, 1,
		func(state *savedCountState) *wrapperspb.Int64Value { return wrapperspb.Int64(state.count) },
		func(msg *wrapperspb.Int64Value) *savedCountState { return &savedCountState{count: msg.GetValue()} })
}

// unsavedCount is a module with state but no snapshot.
type unsavedCount struct{}

var unsavedCountKey = NewKey[savedCountState]("unsaved-count")

func (unsavedCount) Name() string { return "unsaved-count" }

func (unsavedCount) Register(r *Registry) {
	State(r, unsavedCountKey, func() *savedCountState { return &savedCountState{} })
}

// newHarness starts a harness room of typ, closed when the test ends. Call it inside a bubble.
func newHarness(t *testing.T, typ *RoomType) roomtest.Room {
	t.Helper()
	h, err := newTestHarness(&roomtest.Config{Type: typ, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, h.Close(context.Background())) })
	return h
}

func TestTheTestHarnessRefusesARestartItCantDo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		saved := newTestRoomType(t, RoomConfig{Name: "saved", Modules: []Module{savedCount{}}})
		unsaved := newTestRoomType(t, RoomConfig{Name: "unsaved", Modules: []Module{unsavedCount{}}})

		require.ErrorIs(t, newHarness(t, saved).Restart("a room type"), errNotARoomType)
		require.ErrorContains(t, newHarness(t, unsaved).Restart(unsaved), "saves its state")
		require.ErrorContains(t, newHarness(t, saved).Restart(unsaved), "has state but no snapshot",
			"a deploy onto a room type that can't save its rooms")

		closed := newHarness(t, saved)
		require.NoError(t, closed.Do(func(rc any) { rc.(*RoomContext).CloseRoom() }, nil))
		synctest.Wait()
		require.ErrorContains(t, closed.Restart(saved), "had closed")
	})
}

func TestTheTestHarnessNeedsARoomType(t *testing.T) {
	_, err := newTestHarness(&roomtest.Config{Type: "a room type"})

	require.ErrorIs(t, err, errNotARoomType)
}

func TestTheTestHarnessPicksASeedOnlyForTheZeroOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		typ := newTestRoomType(t, RoomConfig{Name: "saved", Modules: []Module{savedCount{}}})
		given := [32]byte{7}
		h, err := newTestHarness(&roomtest.Config{Type: typ, Seed: given, Logger: slog.New(slog.DiscardHandler)})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, h.Close(context.Background())) })

		require.Equal(t, given, h.Seed())
		require.NotEqual(t, [32]byte{}, newHarness(t, typ).Seed(), "a random one for the zero seed")
	})
}
