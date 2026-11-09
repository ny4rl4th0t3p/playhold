package playhold

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

func TestWithoutPlayersDropsOnlyThePlayer(t *testing.T) {
	tests := []struct {
		name string
		log  func(logger *slog.Logger)
	}{
		{name: "at the call", log: func(logger *slog.Logger) { logger.Info("hello", "player", "alice", "room", "r1") }},
		{name: "added with With", log: func(logger *slog.Logger) { logger.With("player", "alice", "room", "r1").Info("hello") }},
		{name: "in a group", log: func(logger *slog.Logger) {
			logger.WithGroup("session").Info("hello", "player", "alice", "room", "r1")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer

			tt.log(hidePlayers(slog.New(slog.NewTextHandler(&logs, nil))))

			assert.Contains(t, logs.String(), "msg=hello")
			assert.Contains(t, logs.String(), "r1", "other attributes stay")
			assert.NotContains(t, logs.String(), "alice")
		})
	}
}

func TestHidingPlayerIDsLeavesThemOutOfLogsAndTraces(t *testing.T) {
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	recorder, provider := newRecorder(t)
	// Rooms log through the runtime's logger, and the server through its own, so both go to the buffer.
	rt := NewRuntime(Config{Logger: logger})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		_ = rt.Shutdown(ctx)
	})
	cfg := &ServerConfig{
		RoomType:       newTestRoomType(t, RoomConfig{Name: "match", Modules: []Module{viewOfSaved, savedModule}}),
		Store:          memory.New(),
		HidePlayerIDs:  true,
		TracerProvider: provider,
		Logger:         logger,
		MemoryLimit:    -1,
		Development:    true,
		Admission:      Admission{Off: true},
	}
	server, err := NewServer(rt, cfg)
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	ws, welcome := seatedGuest(t, "ws"+strings.TrimPrefix(httpServer.URL, "http"))
	require.NotNil(t, readServerFrame(t, ws).GetState())

	// A panic is logged with the player who sent the message, unless the player is hidden.
	sendFrame(t, ws, messageFrame(t, 3, "saved", msgPanic, &addMsg{}))
	require.NotNil(t, readServerFrame(t, ws).GetError())
	require.Eventually(t, func() bool { return len(ended(recorder, "playhold.turn")) >= 2 }, testTimeout, 10*time.Millisecond)

	text := logs.String()
	assert.Contains(t, text, "player IDs left out of logs and traces")
	assert.Contains(t, text, "room handler panicked")
	assert.NotContains(t, text, welcome.GetPlayerId())
	for _, span := range ended(recorder, "playhold.turn") {
		assert.Empty(t, attr(span, attrPlayer))
	}
}
