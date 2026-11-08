package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// These tests use a real network connection, which synctest bubbles don't support, so they use real time.
const testTimeout = 5 * time.Second

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

func ignoreFrames(_ *Conn) (func(*wire.ClientFrame), error) {
	return func(_ *wire.ClientFrame) {}, nil
}

// startServer returns the URL of a running server.
func startServer(t *testing.T, cfg *Config, connect Connect) string {
	t.Helper()
	cfg.Logger = slog.New(slog.DiscardHandler)
	server := NewServer(cfg, connect)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	return "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(testContext(t), url, nil) //nolint:bodyclose // Dial's docs say the body needs no closing
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// dialStatus returns the HTTP status of a refused connection attempt.
func dialStatus(t *testing.T, url string, opts *websocket.DialOptions) int {
	t.Helper()
	_, resp, err := websocket.Dial(testContext(t), url, opts) //nolint:bodyclose // Dial's docs say the body needs no closing
	require.Error(t, err)
	require.NotNil(t, resp)
	return resp.StatusCode
}

func readFrame(t *testing.T, ws *websocket.Conn) *wire.ServerFrame {
	t.Helper()
	_, data, err := ws.Read(testContext(t))
	require.NoError(t, err)
	frame := &wire.ServerFrame{}
	require.NoError(t, proto.Unmarshal(data, frame))
	return frame
}

func TestFramesRoundTrip(t *testing.T) {
	received := make(chan *wire.ClientFrame, 1)
	connected := make(chan *Conn, 1)
	url := startServer(t, &Config{}, func(c *Conn) (func(*wire.ClientFrame), error) {
		connected <- c
		return func(frame *wire.ClientFrame) { received <- frame }, nil
	})
	ws := dial(t, url)

	sent := &wire.ClientFrame{Seq: 7, Body: &wire.ClientFrame_Message{
		Message: &wire.ModuleMessage{Module: "deck", Id: 1, Payload: []byte{1, 2, 3}},
	}}
	data, err := proto.Marshal(sent)
	require.NoError(t, err)
	require.NoError(t, ws.Write(testContext(t), websocket.MessageBinary, data))
	assert.True(t, proto.Equal(sent, <-received))

	state := stateNamed("deck")
	(<-connected).SendState(state)
	assert.True(t, proto.Equal(state, readFrame(t, ws).GetState()))
}

func TestOversizedMessageClosesTheConnection(t *testing.T) {
	url := startServer(t, &Config{MaxMessageBytes: 16}, ignoreFrames)
	ws := dial(t, url)

	require.NoError(t, ws.Write(testContext(t), websocket.MessageBinary, make([]byte, 17)))

	_, _, err := ws.Read(testContext(t))
	assert.Equal(t, websocket.StatusMessageTooBig, websocket.CloseStatus(err))
}

func TestTextMessageClosesTheConnection(t *testing.T) {
	ws := dial(t, startServer(t, &Config{}, ignoreFrames))

	require.NoError(t, ws.Write(testContext(t), websocket.MessageText, []byte("hello")))

	_, _, err := ws.Read(testContext(t))
	assert.Equal(t, websocket.StatusUnsupportedData, websocket.CloseStatus(err))
}

func TestMalformedFrameGetsAnErrorAndKeepsTheConnection(t *testing.T) {
	received := make(chan *wire.ClientFrame, 1)
	url := startServer(t, &Config{}, func(_ *Conn) (func(*wire.ClientFrame), error) {
		return func(frame *wire.ClientFrame) { received <- frame }, nil
	})
	ws := dial(t, url)

	require.NoError(t, ws.Write(testContext(t), websocket.MessageBinary, []byte{0xff}))
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME, readFrame(t, ws).GetError().GetCode())

	data, err := proto.Marshal(&wire.ClientFrame{Seq: 2})
	require.NoError(t, err)
	require.NoError(t, ws.Write(testContext(t), websocket.MessageBinary, data))
	assert.Equal(t, uint64(2), (<-received).GetSeq())
}

func TestConnectionLimits(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want int
	}{
		{name: "server cap", cfg: Config{MaxConns: 1}, want: http.StatusServiceUnavailable},
		{name: "per IP cap", cfg: Config{MaxConns: 8, MaxConnsPerIP: 1}, want: http.StatusTooManyRequests},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := startServer(t, &tt.cfg, ignoreFrames)
			first := dial(t, url)

			assert.Equal(t, tt.want, dialStatus(t, url, nil))

			// Ending a connection frees its slot.
			require.NoError(t, first.Close(websocket.StatusNormalClosure, ""))
			require.Eventually(t, func() bool {
				ws, _, err := websocket.Dial(testContext(t), url, nil) //nolint:bodyclose // Dial's docs say the body needs no closing
				if err != nil {
					return false
				}
				_ = ws.CloseNow()
				return true
			}, testTimeout, 10*time.Millisecond)
		})
	}
}

func TestSilentConnectionIsClosedAfterHandshakeTimeout(t *testing.T) {
	ws := dial(t, startServer(t, &Config{HandshakeTimeout: 20 * time.Millisecond}, ignoreFrames))

	_, _, err := ws.Read(testContext(t))
	require.Error(t, err)
}

func TestFramesDoNotStopHandshakeTimeoutOnTheirOwn(t *testing.T) {
	ws := dial(t, startServer(t, &Config{HandshakeTimeout: 100 * time.Millisecond}, ignoreFrames))

	data, err := proto.Marshal(&wire.ClientFrame{Seq: 1})
	require.NoError(t, err)
	require.NoError(t, ws.Write(testContext(t), websocket.MessageBinary, data))

	_, _, err = ws.Read(testContext(t))
	require.Error(t, err)
}

func TestHandshakeDoneStopsHandshakeTimeout(t *testing.T) {
	timeout := 100 * time.Millisecond
	ws := dial(t, startServer(t, &Config{HandshakeTimeout: timeout}, func(c *Conn) (func(*wire.ClientFrame), error) {
		return func(_ *wire.ClientFrame) { c.HandshakeDone() }, nil
	}))

	data, err := proto.Marshal(&wire.ClientFrame{Seq: 1})
	require.NoError(t, err)
	require.NoError(t, ws.Write(testContext(t), websocket.MessageBinary, data))

	time.Sleep(3 * timeout)

	// A reply shows the connection outlived the timeout.
	require.NoError(t, ws.Write(testContext(t), websocket.MessageBinary, []byte{0xff}))
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME, readFrame(t, ws).GetError().GetCode())
}

func TestOtherOriginsAreRefusedUnlessListed(t *testing.T) {
	opts := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://game.example"}}}

	refused := startServer(t, &Config{}, ignoreFrames)
	assert.Equal(t, http.StatusForbidden, dialStatus(t, refused, opts))

	allowed := startServer(t, &Config{OriginPatterns: []string{"game.example"}}, ignoreFrames)
	ws, _, err := websocket.Dial(testContext(t), allowed, opts) //nolint:bodyclose // Dial's docs say the body needs no closing
	require.NoError(t, err)
	_ = ws.CloseNow()
}

func TestReplacedConnectionClosesWithItsOwnStatus(t *testing.T) {
	connected := make(chan *Conn, 1)
	url := startServer(t, &Config{}, func(c *Conn) (func(*wire.ClientFrame), error) {
		connected <- c
		return func(_ *wire.ClientFrame) {}, nil
	})
	ws := dial(t, url)

	(<-connected).Replace()

	_, _, err := ws.Read(testContext(t))
	assert.Equal(t, StatusReplaced, websocket.CloseStatus(err))
}

func TestOnCloseRunsWhenTheConnectionEnds(t *testing.T) {
	ended := make(chan struct{})
	url := startServer(t, &Config{}, func(c *Conn) (func(*wire.ClientFrame), error) {
		c.OnClose(func() { close(ended) })
		return func(_ *wire.ClientFrame) {}, nil
	})
	ws := dial(t, url)

	require.NoError(t, ws.CloseNow())

	select {
	case <-ended:
	case <-testContext(t).Done():
		require.FailNow(t, "OnClose did not run")
	}
}

func TestConnectErrorClosesTheConnection(t *testing.T) {
	url := startServer(t, &Config{}, func(_ *Conn) (func(*wire.ClientFrame), error) {
		return nil, errors.New("no room")
	})
	ws := dial(t, url)

	_, _, err := ws.Read(testContext(t))
	assert.Equal(t, websocket.StatusInternalError, websocket.CloseStatus(err))
}

func TestClientIP(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	fromBehindProxy := func(_ *http.Request) (netip.Addr, bool) { return netip.MustParseAddr("192.0.2.9"), true }
	tests := []struct {
		name        string
		remote      string
		forwarded   []string
		proxies     []netip.Prefix
		read        func(*http.Request) (netip.Addr, bool)
		want        string
		wantProxied bool
	}{
		{name: "no proxy configured", remote: "203.0.113.5:4000", want: "203.0.113.5"},
		{
			name: "no proxy configured ignores the header", remote: "203.0.113.5:4000", forwarded: []string{"198.51.100.1"},
			want: "203.0.113.5",
		},
		{
			name: "an untrusted sender can't pick its address", remote: "203.0.113.5:4000", forwarded: []string{"198.51.100.1"},
			proxies: proxies, want: "203.0.113.5",
		},
		{
			name: "a trusted proxy's header is read", remote: "10.0.0.2:4000", forwarded: []string{"198.51.100.1"},
			proxies: proxies, want: "198.51.100.1", wantProxied: true,
		},
		{
			name: "the rightmost untrusted address, past other trusted proxies", remote: "10.0.0.2:4000",
			forwarded: []string{"6.6.6.6, 198.51.100.1, 10.0.0.7"}, proxies: proxies, want: "198.51.100.1", wantProxied: true,
		},
		{
			name: "header lines are read as one list", remote: "10.0.0.2:4000",
			forwarded: []string{"6.6.6.6", "198.51.100.1"}, proxies: proxies, want: "198.51.100.1", wantProxied: true,
		},
		{
			name: "no header gives the proxy's own address", remote: "10.0.0.2:4000",
			proxies: proxies, want: "10.0.0.2", wantProxied: true,
		},
		{
			name: "an unreadable hop gives the proxy's own address", remote: "10.0.0.2:4000",
			forwarded: []string{"198.51.100.1, not-an-address"}, proxies: proxies, want: "10.0.0.2", wantProxied: true,
		},
		{
			name: "an IPv4 address written as IPv6 is unwrapped", remote: "[::ffff:10.0.0.2]:4000",
			forwarded: []string{"::ffff:198.51.100.1"}, proxies: proxies, want: "198.51.100.1", wantProxied: true,
		},
		{
			name: "the game's own reader replaces the header", remote: "10.0.0.2:4000",
			forwarded: []string{"198.51.100.1"}, proxies: proxies, read: fromBehindProxy, want: "192.0.2.9", wantProxied: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer(&Config{TrustedProxies: tt.proxies, ProxyClientIP: tt.read}, ignoreFrames)
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			r.RemoteAddr = tt.remote
			for _, line := range tt.forwarded {
				r.Header.Add("X-Forwarded-For", line)
			}

			got, proxied := server.clientIP(r)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantProxied, proxied)
		})
	}
}

func TestRequireSecureRefusesPlainConnectionsNotFromATrustedProxy(t *testing.T) {
	refused := startServer(t, &Config{RequireSecure: true}, ignoreFrames)
	assert.Equal(t, http.StatusForbidden, dialStatus(t, refused, nil))

	// The test client connects from 127.0.0.1, standing in for a proxy that ends TLS in front of the server.
	proxied := startServer(t, &Config{
		RequireSecure:  true,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	}, ignoreFrames)
	_ = dial(t, proxied)

	server := NewServer(&Config{RequireSecure: true, Logger: slog.New(slog.DiscardHandler)}, ignoreFrames)
	httpServer := httptest.NewTLSServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	ws, _, err := websocket.Dial(testContext(t), "wss"+strings.TrimPrefix(httpServer.URL, "https"), //nolint:bodyclose // Dial's docs say the body needs no closing
		&websocket.DialOptions{HTTPClient: httpServer.Client()})
	require.NoError(t, err, "a TLS connection is accepted")
	_ = ws.CloseNow()
}

func TestCloseDisconnectsClientsAndRefusesNewOnes(t *testing.T) {
	server := NewServer(&Config{Logger: slog.New(slog.DiscardHandler)}, ignoreFrames)
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	ws := dial(t, url)

	server.Close()

	_, _, err := ws.Read(testContext(t))
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, dialStatus(t, url, nil))
}
