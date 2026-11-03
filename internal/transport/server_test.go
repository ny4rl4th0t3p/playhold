package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestFirstFrameStopsHandshakeTimeout(t *testing.T) {
	timeout := 100 * time.Millisecond
	ws := dial(t, startServer(t, &Config{HandshakeTimeout: timeout}, ignoreFrames))

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

func TestConnectErrorClosesTheConnection(t *testing.T) {
	url := startServer(t, &Config{}, func(_ *Conn) (func(*wire.ClientFrame), error) {
		return nil, errors.New("no room")
	})
	ws := dial(t, url)

	_, _, err := ws.Read(testContext(t))
	assert.Equal(t, websocket.StatusInternalError, websocket.CloseStatus(err))
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
