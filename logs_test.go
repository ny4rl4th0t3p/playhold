package playhold

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

func TestAStuckRoomsLogNamesTheTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		release := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		logs := &lockedBuffer{}
		rt := NewRuntime(Config{StuckTimeout: timeout, Logger: slog.New(slog.NewTextHandler(logs, nil))})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			_ = rt.Shutdown(ctx)
		})
		rt.useBreaker(testBreaker(t, memory.New(), "build", nil)) // inside a server, rooms track their running turn
		module := registerFunc{name: "m", register: func(r *Registry) {
			Handle(r, msgBlock, func(_ *RoomContext, _ *addMsg) error {
				<-release
				return nil
			})
		}}
		room, err := rt.CreateRoom(newTestRoomType(t, RoomConfig{Modules: []Module{module}}))
		require.NoError(t, err)

		require.NoError(t, room.dispatch("carol", "m", msgBlock, mustMarshal(t, &addMsg{}), nil))
		time.Sleep(timeout)
		synctest.Wait()

		text := logs.String()
		assert.Contains(t, text, `msg="room stuck, closing it"`)
		assert.Contains(t, text, "player=carol")
		assert.Contains(t, text, "module=m")
		releaseOnce.Do(func() { close(release) })
		synctest.Wait()
	})
}
