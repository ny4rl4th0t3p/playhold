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

	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

func TestAServerWithNoStoreSaysSoAtStartup(t *testing.T) {
	tests := []struct {
		name    string
		records store.Store
		warned  bool
	}{
		{name: "no store", records: nil, warned: true},
		{name: "a store", records: memory.New(), warned: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &lockedBuffer{}
			rt := NewRuntime(Config{Logger: slog.New(slog.NewTextHandler(logs, nil))})
			server, err := NewServer(rt, &ServerConfig{RoomType: newTestRoomType(t, RoomConfig{Name: "t"}), Store: tt.records})
			require.NoError(t, err)
			t.Cleanup(server.Close)

			text := logs.String()
			if tt.warned {
				assert.Contains(t, text, `level=WARN msg="no store: games and guests live in memory only`)
			} else {
				assert.NotContains(t, text, "no store")
			}
		})
	}
}

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
