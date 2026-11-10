package playhold

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// startObservedServer is startServerWith, also returning the server, to read its metrics.
func startObservedServer(t *testing.T, cfg *ServerConfig) (server *Server, url string) {
	t.Helper()
	cfg.RoomType = newTestRoomType(t, RoomConfig{Name: "match", Modules: []Module{viewOfSaved, savedModule}})
	cfg.MemoryLimit = -1
	cfg.Logger = slog.New(slog.DiscardHandler)
	cfg.Development = true
	cfg.AllowClientCodes = true
	cfg.Admission.Off = true
	server, err := NewServer(newTestRuntime(t), cfg)
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	return server, "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

// sampleCount returns how many observations a histogram without labels, or with these label values, has had. It doesn't
// fail the test itself, so require.Eventually can call it.
func sampleCount(server *Server, name string, labelValues ...string) uint64 {
	families, err := server.metrics.registry.Gather()
	if err != nil {
		return 0
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			var values []string
			for _, label := range metric.GetLabel() {
				values = append(values, label.GetValue())
			}
			if strings.Join(values, ",") == strings.Join(labelValues, ",") {
				return metric.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func scrape(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

func TestMetricsCountMessagesTurnsAndWhatsRunning(t *testing.T) {
	server, url := startObservedServer(t, &ServerConfig{Store: memory.New()})
	ws, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, ws).GetState())

	sendFrame(t, ws, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 1}))
	require.NotNil(t, readServerFrame(t, ws).GetState())
	sendFrame(t, ws, messageFrame(t, 4, "saved", msgRefuseAfterAdding, &addMsg{Value: 1}))
	require.NotNil(t, readServerFrame(t, ws).GetError())
	require.NotNil(t, readServerFrame(t, ws).GetState())
	sendFrame(t, ws, messageFrame(t, 5, "nope", 1, &addMsg{}))
	require.NotNil(t, readServerFrame(t, ws).GetError())
	require.NotNil(t, readServerFrame(t, ws).GetState())

	m := server.metrics
	assert.InDelta(t, 1.0, testutil.ToFloat64(m.messages[handlerKey{module: "saved", id: msgAdd}][resultOK]), 0)
	assert.InDelta(t, 1.0, testutil.ToFloat64(m.messages[handlerKey{module: "saved", id: msgRefuseAfterAdding}][resultRejected]), 0)
	assert.InDelta(t, 1.0, testutil.ToFloat64(m.unknownMessages[resultUnknownMessage]), 0, "an unknown module adds no label")
	assert.Equal(t, uint64(3), sampleCount(server, "playhold_message_latency_seconds"))
	assert.Equal(t, uint64(3), sampleCount(server, "playhold_turn_duration_seconds", "message"))
	assert.Positive(t, sampleCount(server, "playhold_turn_duration_seconds", "other"), "the join")
	assert.Positive(t, testutil.ToFloat64(m.viewBytes[0]))
	require.Eventually(t, func() bool { return sampleCount(server, "playhold_snapshot_delay_seconds") > 0 },
		testTimeout, 10*time.Millisecond, "the saves are written in the background")

	text := scrape(t, server.MetricsHandler())
	for _, line := range []string{"playhold_rooms 1", "playhold_connections 1", "playhold_players_connected 1",
		"playhold_overloaded 0", "playhold_messages_turned_off 0"} {
		assert.Contains(t, text, line+"\n")
	}
	assert.Contains(t, text, "go_goroutines ", "the Go runtime's own metrics")
}

func TestMetricsCountARoomRestoredAfterAPanic(t *testing.T) {
	server, url := startObservedServer(t, &ServerConfig{Store: memory.New()})
	ws, _ := seatedGuest(t, url)
	require.NotNil(t, readServerFrame(t, ws).GetState())

	sendFrame(t, ws, messageFrame(t, 3, "saved", msgPanic, &addMsg{}))
	require.NotNil(t, readServerFrame(t, ws).GetError())
	require.NotNil(t, readServerFrame(t, ws).GetState(), "the copy is running")

	m := server.metrics
	assert.InDelta(t, 1.0, testutil.ToFloat64(m.restores.WithLabelValues("panic")), 0)
	assert.InDelta(t, 1.0, testutil.ToFloat64(m.closes[ClosePanic]), 0)
	assert.InDelta(t, 1.0, testutil.ToFloat64(m.messages[handlerKey{module: "saved", id: msgPanic}][resultFailed]), 0)
}

func TestMetricsCountRateLimitedFrames(t *testing.T) {
	server, url := startObservedServer(t, &ServerConfig{MessageRate: RateLimit{Every: time.Hour, Burst: 2}})
	ws, _ := seatedGuest(t, url) // Hello and Join take the burst
	require.NotNil(t, readServerFrame(t, ws).GetState())

	sendFrame(t, ws, messageFrame(t, 3, "saved", msgAdd, &addMsg{Value: 1}))

	assert.Equal(t, wire.ErrorCode_ERROR_CODE_RATE_LIMITED, readServerFrame(t, ws).GetError().GetCode())
	assert.InDelta(t, 1.0, testutil.ToFloat64(server.metrics.rateLimited.WithLabelValues("frame")), 0)
}

func TestMetricsAreServedOnTheirOwnListener(t *testing.T) {
	server, err := newQuietServer(t, &ServerConfig{Development: true})
	require.NoError(t, err)
	var listen net.ListenConfig
	game, err := listen.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	metricsListener, err := server.listenMetrics(t.Context(), MetricsListen{Addr: "127.0.0.1:0"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.serve(ctx, game, nil, &ListenConfig{}, metricsListener) }()

	get := func(url string) (int, string) {
		req, err := http.NewRequestWithContext(gatewayContext(t), http.MethodGet, url, http.NoBody)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}
	status, body := get("http://" + metricsListener.Addr().String() + "/metrics")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "playhold_rooms ")
	status, _ = get("http://" + game.Addr().String() + "/metrics")
	assert.Equal(t, http.StatusNotFound, status, "never on the game's listener")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-gatewayContext(t).Done():
		require.FailNow(t, "serve did not return after ctx ended")
	}
}

func TestMetricsListener(t *testing.T) {
	tests := []struct {
		name     string
		listen   MetricsListen
		dev      bool
		wantOff  bool
		wantWarn bool
	}{
		{name: "this machine only", listen: MetricsListen{Addr: "127.0.0.1:0"}},
		{name: "every address warns", listen: MetricsListen{Addr: "0.0.0.0:0"}, wantWarn: true},
		{name: "every address in development doesn't", listen: MetricsListen{Addr: "0.0.0.0:0"}, dev: true},
		{name: "off", listen: MetricsListen{Off: true}, wantOff: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := &lockedBuffer{}
			server, err := newQuietServer(t, &ServerConfig{Development: tt.dev})
			require.NoError(t, err)
			server.logger = slog.New(slog.NewTextHandler(logs, nil))

			listener, err := server.listenMetrics(t.Context(), tt.listen)

			require.NoError(t, err)
			if tt.wantOff {
				assert.Nil(t, listener)
				return
			}
			require.NotNil(t, listener)
			_ = listener.Close()
			assert.Equal(t, tt.wantWarn, strings.Contains(logs.String(), "other machines can reach"))
		})
	}
}

func TestIsLoopback(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:9464", want: true},
		{addr: "[::1]:9464", want: true},
		{addr: "localhost:9464", want: true},
		{addr: ":9464"},
		{addr: "0.0.0.0:9464"},
		{addr: "10.0.0.5:9464"},
		{addr: "not an address"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			assert.Equal(t, tt.want, isLoopback(tt.addr))
		})
	}
}
