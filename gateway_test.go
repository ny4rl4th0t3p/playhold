package playhold

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// These tests use a real network connection, which synctest bubbles don't support, so they use real time.

const testCode = "room-1"

func gatewayContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// startGameServer returns the URL of a running server.
func startGameServer(t *testing.T, rooms RoomConfig, handshake time.Duration) string {
	t.Helper()
	return startServerWith(t, &ServerConfig{RoomType: newTestRoomType(t, rooms), HandshakeTimeout: handshake})
}

// startServerWith returns the URL of a running server with this config, on its own runtime.
func startServerWith(t *testing.T, cfg *ServerConfig) string {
	t.Helper()
	cfg.MemoryLimit = -1
	cfg.Logger = slog.New(slog.DiscardHandler)
	cfg.Development = true // plain HTTP, as in every test that doesn't check TLS
	if cfg.Admission == (Admission{}) {
		cfg.Admission.Off = true // a busy test machine shouldn't refuse players; admission_test.go turns it on
	}
	server, err := NewServer(newTestRuntime(t), cfg)
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	return "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

func dialServer(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(gatewayContext(t), url, nil) //nolint:bodyclose // Dial's docs say the body needs no closing
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

func sendFrame(t *testing.T, ws *websocket.Conn, frame *wire.ClientFrame) {
	t.Helper()
	data, err := proto.Marshal(frame)
	require.NoError(t, err)
	require.NoError(t, ws.Write(gatewayContext(t), websocket.MessageBinary, data))
}

func readServerFrame(t *testing.T, ws *websocket.Conn) *wire.ServerFrame {
	t.Helper()
	_, data, err := ws.Read(gatewayContext(t))
	require.NoError(t, err)
	frame := &wire.ServerFrame{}
	require.NoError(t, proto.Unmarshal(data, frame))
	return frame
}

func helloFrame(version uint32, player, secret string) *wire.ClientFrame {
	return &wire.ClientFrame{Seq: 1, Body: &wire.ClientFrame_Hello{
		Hello: &wire.Hello{ProtocolVersion: version, PlayerId: player, Secret: secret},
	}}
}

func joinFrame(code string) *wire.ClientFrame {
	return &wire.ClientFrame{Seq: 2, Body: &wire.ClientFrame_Join{Join: &wire.Join{Code: code}}}
}

func messageFrame(t *testing.T, seq uint64, module string, id uint32, msg proto.Message) *wire.ClientFrame {
	t.Helper()
	payload, err := proto.Marshal(msg)
	require.NoError(t, err)
	return &wire.ClientFrame{Seq: seq, Body: &wire.ClientFrame_Message{
		Message: &wire.ModuleMessage{Module: module, Id: id, Payload: payload},
	}}
}

// newGuest connects as a new guest.
func newGuest(t *testing.T, url string) (*websocket.Conn, *wire.Welcome) {
	t.Helper()
	ws := dialServer(t, url)
	sendFrame(t, ws, helloFrame(protocolVersion, "", ""))
	welcome := readServerFrame(t, ws).GetWelcome()
	require.NotNil(t, welcome)
	return ws, welcome
}

// returningGuest connects with an existing identity.
func returningGuest(t *testing.T, url string, welcome *wire.Welcome) *websocket.Conn {
	t.Helper()
	ws := dialServer(t, url)
	sendFrame(t, ws, helloFrame(protocolVersion, welcome.GetPlayerId(), welcome.GetSecret()))
	require.NotNil(t, readServerFrame(t, ws).GetWelcome())
	return ws
}

func joinRoom(t *testing.T, ws *websocket.Conn, code string) {
	t.Helper()
	sendFrame(t, ws, joinFrame(code))
	joined := readServerFrame(t, ws).GetJoined()
	require.NotNil(t, joined)
	assert.Equal(t, code, joined.GetCode())
	assert.Equal(t, uint64(2), joined.GetSeq())
}

// seatedGuest connects as a new guest and joins the test room.
func seatedGuest(t *testing.T, url string) (*websocket.Conn, *wire.Welcome) {
	t.Helper()
	ws, welcome := newGuest(t, url)
	joinRoom(t, ws, testCode)
	return ws, welcome
}

func TestNewGuestGetsAnIdentity(t *testing.T) {
	url := startGameServer(t, RoomConfig{}, 0)

	_, first := newGuest(t, url)
	_, second := newGuest(t, url)

	assert.NotEmpty(t, first.GetPlayerId())
	assert.NotEmpty(t, first.GetSecret())
	assert.NotEqual(t, first.GetPlayerId(), second.GetPlayerId())
	assert.NotEqual(t, first.GetSecret(), second.GetSecret())
}

func TestReturningGuestKeepsTheirIdentity(t *testing.T) {
	url := startGameServer(t, RoomConfig{}, 0)
	_, welcome := newGuest(t, url)

	ws := dialServer(t, url)
	sendFrame(t, ws, helloFrame(protocolVersion, welcome.GetPlayerId(), welcome.GetSecret()))

	got := readServerFrame(t, ws).GetWelcome()
	assert.Equal(t, welcome.GetPlayerId(), got.GetPlayerId())
	assert.Empty(t, got.GetSecret(), "the secret is only sent once")
}

func TestGuestKeepsTheirIdentityOnANewServerWithTheSameStore(t *testing.T) {
	records := memory.New()
	cfg := func() *ServerConfig {
		return &ServerConfig{RoomType: newTestRoomType(t, RoomConfig{}), Store: records}
	}
	_, welcome := newGuest(t, startServerWith(t, cfg()))

	ws := dialServer(t, startServerWith(t, cfg()))
	sendFrame(t, ws, helloFrame(protocolVersion, welcome.GetPlayerId(), welcome.GetSecret()))

	assert.Equal(t, welcome.GetPlayerId(), readServerFrame(t, ws).GetWelcome().GetPlayerId())
}

func TestStoreOutageIsReportedAndTheClientCanRetry(t *testing.T) {
	url := startServerWith(t, &ServerConfig{RoomType: newTestRoomType(t, RoomConfig{}), Store: downStore{}})

	tests := []struct {
		name  string
		frame *wire.ClientFrame
	}{
		{name: "new guest", frame: helloFrame(protocolVersion, "", "")},
		{name: "returning guest", frame: helloFrame(protocolVersion, "alice", "secret")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := dialServer(t, url)

			sendFrame(t, ws, tt.frame)

			got := readServerFrame(t, ws).GetError()
			assert.Equal(t, wire.ErrorCode_ERROR_CODE_UNAVAILABLE, got.GetCode())
			assert.NotContains(t, got.GetMessage(), errStoreDown.Error(), "internal errors stay on the server")

			// The connection stays open, so saying hello again is allowed.
			sendFrame(t, ws, tt.frame)
			assert.NotNil(t, readServerFrame(t, ws).GetError())
		})
	}
}

func TestHelloProblems(t *testing.T) {
	url := startGameServer(t, RoomConfig{}, 0)
	_, welcome := newGuest(t, url)

	tests := []struct {
		name  string
		frame *wire.ClientFrame
		want  wire.ErrorCode
	}{
		{
			name:  "wrong secret",
			frame: helloFrame(protocolVersion, welcome.GetPlayerId(), "wrong"),
			want:  wire.ErrorCode_ERROR_CODE_UNAUTHORIZED,
		},
		{
			name:  "unknown player",
			frame: helloFrame(protocolVersion, "nobody", welcome.GetSecret()),
			want:  wire.ErrorCode_ERROR_CODE_UNAUTHORIZED,
		},
		{
			name:  "no secret",
			frame: helloFrame(protocolVersion, welcome.GetPlayerId(), ""),
			want:  wire.ErrorCode_ERROR_CODE_UNAUTHORIZED,
		},
		{
			name:  "secret without a player",
			frame: helloFrame(protocolVersion, "", welcome.GetSecret()),
			want:  wire.ErrorCode_ERROR_CODE_UNAUTHORIZED,
		},
		{
			name:  "unsupported protocol",
			frame: helloFrame(protocolVersion+1, "", ""),
			want:  wire.ErrorCode_ERROR_CODE_UNSUPPORTED_PROTOCOL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := dialServer(t, url)

			sendFrame(t, ws, tt.frame)
			assert.Equal(t, tt.want, readServerFrame(t, ws).GetError().GetCode())

			// The client can try again, as a new guest.
			sendFrame(t, ws, helloFrame(protocolVersion, "", ""))
			assert.NotEmpty(t, readServerFrame(t, ws).GetWelcome().GetPlayerId())
		})
	}
}

func TestFramesMustComeInOrder(t *testing.T) {
	url := startGameServer(t, RoomConfig{}, 0)
	message := messageFrame(t, 2, "m", msgAdd, &addMsg{})

	tests := []struct {
		name  string
		setup func(t *testing.T, ws *websocket.Conn)
		frame *wire.ClientFrame
	}{
		{name: "join before hello", frame: joinFrame(testCode)},
		{name: "message before hello", frame: message},
		{
			name: "message before join",
			setup: func(t *testing.T, ws *websocket.Conn) {
				t.Helper()
				sendFrame(t, ws, helloFrame(protocolVersion, "", ""))
				readServerFrame(t, ws)
			},
			frame: message,
		},
		{
			name: "second hello",
			setup: func(t *testing.T, ws *websocket.Conn) {
				t.Helper()
				sendFrame(t, ws, helloFrame(protocolVersion, "", ""))
				readServerFrame(t, ws)
			},
			frame: helloFrame(protocolVersion, "", ""),
		},
		{
			name: "second join",
			setup: func(t *testing.T, ws *websocket.Conn) {
				t.Helper()
				sendFrame(t, ws, helloFrame(protocolVersion, "", ""))
				readServerFrame(t, ws)
				joinRoom(t, ws, testCode)
			},
			frame: joinFrame("another-room"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := dialServer(t, url)
			if tt.setup != nil {
				tt.setup(t, ws)
			}

			sendFrame(t, ws, tt.frame)

			assert.Equal(t, wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME, readServerFrame(t, ws).GetError().GetCode())
		})
	}
}

func TestRoomCodesMustBeSafe(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{code: "abc-DEF_123", want: true},
		{code: strings.Repeat("a", maxRoomCodeLen), want: true},
		{code: "", want: false},
		{code: strings.Repeat("a", maxRoomCodeLen+1), want: false},
		{code: "with space", want: false},
		{code: "../etc", want: false},
		{code: "número", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			assert.Equal(t, tt.want, validRoomCode(tt.code))
		})
	}
}

func TestSameCodeReachesTheSameRoom(t *testing.T) {
	values := make(chan RoomID, 3)
	module := registerFunc{name: "m", register: func(r *Registry) {
		Handle(r, msgAdd, func(rc *RoomContext, _ *addMsg) error {
			values <- rc.RoomID()
			return nil
		})
	}}
	url := startGameServer(t, RoomConfig{Modules: []Module{module}}, 0)
	first, _ := seatedGuest(t, url)
	second, _ := seatedGuest(t, url)
	third, _ := newGuest(t, url)
	joinRoom(t, third, "another-room")

	sendFrame(t, first, messageFrame(t, 3, "m", msgAdd, &addMsg{}))
	firstRoom := <-values
	sendFrame(t, second, messageFrame(t, 3, "m", msgAdd, &addMsg{}))
	secondRoom := <-values
	sendFrame(t, third, messageFrame(t, 3, "m", msgAdd, &addMsg{}))
	thirdRoom := <-values

	assert.Equal(t, firstRoom, secondRoom)
	assert.NotEqual(t, firstRoom, thirdRoom)
}

func TestMessageReachesItsHandlerAsThePlayer(t *testing.T) {
	type call struct {
		player string
		value  int32
	}
	calls := make(chan call, 1)
	module := registerFunc{name: "m", register: func(r *Registry) {
		Handle(r, msgAdd, func(rc *RoomContext, msg *addMsg) error {
			calls <- call{player: string(rc.Player()), value: msg.GetValue()}
			return nil
		})
	}}
	url := startGameServer(t, RoomConfig{Modules: []Module{module}}, 0)
	ws, welcome := seatedGuest(t, url)

	sendFrame(t, ws, messageFrame(t, 3, "m", msgAdd, &addMsg{Value: 3}))

	assert.Equal(t, call{player: welcome.GetPlayerId(), value: 3}, <-calls)
}

func TestReconnectKeepsTheSeat(t *testing.T) {
	joined := make(chan PlayerID, 3)
	module := registerFunc{name: "m", register: func(r *Registry) {
		On(r, func(_ *RoomContext, event PlayerJoined) { joined <- event.Player })
	}}
	url := startGameServer(t, RoomConfig{MaxPlayers: 1, Modules: []Module{module}}, 0)

	first, welcome := seatedGuest(t, url)
	assert.Equal(t, PlayerID(welcome.GetPlayerId()), <-joined)
	require.NoError(t, first.CloseNow())

	// The room is full, but this player already has the seat.
	joinRoom(t, returningGuest(t, url, welcome), testCode)

	// Nobody else can take it.
	other, _ := newGuest(t, url)
	sendFrame(t, other, joinFrame(testCode))
	got := readServerFrame(t, other).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_ROOM_FULL, got.GetCode())
	assert.Equal(t, uint64(2), got.GetSeq())

	assert.Empty(t, joined, "coming back is not a new seat")
}

func TestPlayerRefusedByAFullRoomCanJoinAnother(t *testing.T) {
	url := startGameServer(t, RoomConfig{MaxPlayers: 1}, 0)
	seatedGuest(t, url)
	ws, _ := newGuest(t, url)

	sendFrame(t, ws, joinFrame(testCode))
	require.Equal(t, wire.ErrorCode_ERROR_CODE_ROOM_FULL, readServerFrame(t, ws).GetError().GetCode())

	joinRoom(t, ws, "another-room")
}

func TestNewestConnectionReplacesTheOldOne(t *testing.T) {
	url := startGameServer(t, RoomConfig{}, 0)
	old, welcome := seatedGuest(t, url)

	newest := returningGuest(t, url, welcome)

	_, _, err := old.Read(gatewayContext(t))
	assert.Equal(t, transport.StatusReplaced, websocket.CloseStatus(err))

	// The newest connection keeps working after the old one has ended.
	joinRoom(t, newest, testCode)
}

func TestHelloStopsTheHandshakeTimeout(t *testing.T) {
	timeout := 100 * time.Millisecond
	url := startGameServer(t, RoomConfig{}, timeout)
	ws, _ := newGuest(t, url)

	time.Sleep(3 * timeout)

	joinRoom(t, ws, testCode)
}

func TestFailedHelloDoesNotStopTheHandshakeTimeout(t *testing.T) {
	url := startGameServer(t, RoomConfig{}, 100*time.Millisecond)
	ws := dialServer(t, url)

	sendFrame(t, ws, helloFrame(protocolVersion, "nobody", "wrong"))
	require.NotNil(t, readServerFrame(t, ws).GetError())

	_, _, err := ws.Read(gatewayContext(t))
	require.Error(t, err)
}

func TestServerWithTooManyRoomsSaysSo(t *testing.T) {
	url := startServerWith(t, &ServerConfig{RoomType: newTestRoomType(t, RoomConfig{}), MaxRooms: 1})
	seatedGuest(t, url)
	ws, _ := newGuest(t, url)

	sendFrame(t, ws, joinFrame("another-room"))
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_SERVER_FULL, readServerFrame(t, ws).GetError().GetCode())

	// The room that exists can still be joined.
	joinRoom(t, ws, testCode)
}

func TestRoomThatFailsToStartIsReported(t *testing.T) {
	url := startGameServer(t, RoomConfig{Init: func(_ *RoomContext) error { return errors.New("bad config") }}, 0)
	ws, _ := newGuest(t, url)

	sendFrame(t, ws, joinFrame(testCode))

	got := readServerFrame(t, ws).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_REJECTED, got.GetCode())
	assert.NotContains(t, got.GetMessage(), "bad config", "internal errors stay on the server")
}

func TestProblemsAreReportedToTheSender(t *testing.T) {
	refused := registerFunc{name: "refusing", register: func(r *Registry) {
		Handle(r, msgFail, func(_ *RoomContext, _ *addMsg) error { return errors.New("not your turn") })
		Handle(r, msgAdd, func(_ *RoomContext, _ *addMsg) error { return nil })
	}}
	badPayload := &wire.ClientFrame{Seq: 7, Body: &wire.ClientFrame_Message{
		Message: &wire.ModuleMessage{Module: "refusing", Id: msgAdd, Payload: []byte{0xff}},
	}}

	tests := []struct {
		name  string
		frame *wire.ClientFrame
		want  wire.ErrorCode
	}{
		{name: "no body", frame: &wire.ClientFrame{Seq: 7}, want: wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME},
		{
			name:  "no module name",
			frame: messageFrame(t, 7, "", msgAdd, &addMsg{}),
			want:  wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME,
		},
		{
			name:  "unknown module",
			frame: messageFrame(t, 7, "nope", msgAdd, &addMsg{}),
			want:  wire.ErrorCode_ERROR_CODE_UNKNOWN_MESSAGE,
		},
		{name: "undecodable payload", frame: badPayload, want: wire.ErrorCode_ERROR_CODE_BAD_MESSAGE},
		{
			name:  "handler refuses",
			frame: messageFrame(t, 7, "refusing", msgFail, &addMsg{}),
			want:  wire.ErrorCode_ERROR_CODE_REJECTED,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, _ := seatedGuest(t, startGameServer(t, RoomConfig{Modules: []Module{refused}}, 0))

			sendFrame(t, ws, tt.frame)

			got := readServerFrame(t, ws).GetError()
			assert.Equal(t, tt.want, got.GetCode())
			assert.Equal(t, uint64(7), got.GetSeq())
		})
	}
}

func TestBusyRoomReportsQueueFull(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	module := registerFunc{name: "m", register: func(r *Registry) {
		Handle(r, msgFail, func(_ *RoomContext, _ *addMsg) error {
			close(started)
			<-release
			return nil
		})
		Handle(r, msgAdd, func(_ *RoomContext, _ *addMsg) error { return nil })
	}}
	ws, _ := seatedGuest(t, startGameServer(t, RoomConfig{QueueSize: 1, Modules: []Module{module}}, 0))
	defer close(release)

	sendFrame(t, ws, messageFrame(t, 3, "m", msgFail, &addMsg{}))
	waitFor(t, started)
	sendFrame(t, ws, messageFrame(t, 4, "m", msgAdd, &addMsg{}))
	sendFrame(t, ws, messageFrame(t, 5, "m", msgAdd, &addMsg{}))

	got := readServerFrame(t, ws).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_QUEUE_FULL, got.GetCode())
	assert.Equal(t, uint64(5), got.GetSeq())
}

func TestPlayersAreToldWhenTheRoomCloses(t *testing.T) {
	module := registerFunc{name: "m", register: func(r *Registry) {
		Handle(r, msgFail, func(rc *RoomContext, _ *addMsg) error {
			rc.CloseRoom()
			return nil
		})
		Handle(r, msgAdd, func(_ *RoomContext, _ *addMsg) error { panic("boom") })
	}}

	tests := []struct {
		name string
		id   uint32
		want wire.CloseReason
	}{
		{name: "closed on purpose", id: msgFail, want: wire.CloseReason_CLOSE_REASON_REQUESTED},
		{name: "handler panic", id: msgAdd, want: wire.CloseReason_CLOSE_REASON_PANIC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, _ := seatedGuest(t, startGameServer(t, RoomConfig{Modules: []Module{module}}, 0))

			sendFrame(t, ws, messageFrame(t, 3, "m", tt.id, &addMsg{}))

			assert.Equal(t, tt.want, readServerFrame(t, ws).GetClosed().GetReason())

			// The connection outlives the room: messages are refused, and it can join a new room.
			sendFrame(t, ws, messageFrame(t, 4, "m", msgAdd, &addMsg{}))
			assert.Equal(t, wire.ErrorCode_ERROR_CODE_ROOM_CLOSED, readServerFrame(t, ws).GetError().GetCode())
			joinRoom(t, ws, testCode)
		})
	}
}

func TestPlayerSeesTheirViewAfterJoiningAndAfterReconnecting(t *testing.T) {
	text := func(t *testing.T, frame *wire.ServerFrame) string {
		t.Helper()
		views := frame.GetState().GetViews()
		require.Len(t, views, 1)
		return viewText(t, views[0])
	}
	url := startGameServer(t, RoomConfig{Modules: []Module{showsPlayer("cards")}}, 0)

	first, welcome := seatedGuest(t, url)
	want := "cards for " + welcome.GetPlayerId()
	assert.Equal(t, want, text(t, readServerFrame(t, first)))
	require.NoError(t, first.CloseNow())

	again := returningGuest(t, url, welcome)
	joinRoom(t, again, testCode)
	assert.Equal(t, want, text(t, readServerFrame(t, again)))

	sendFrame(t, again, messageFrame(t, 3, "cards", msgAdd, &addMsg{}))
	assert.Equal(t, want, text(t, readServerFrame(t, again)))
}

func TestPlayersAreToldWhenTheirRoomIsStuck(t *testing.T) {
	release := make(chan struct{})
	module := registerFunc{name: "m", register: func(r *Registry) {
		Handle(r, msgAdd, func(_ *RoomContext, _ *addMsg) error {
			<-release
			return nil
		})
	}}
	server, err := NewServer(newConfiguredRuntime(t, Config{StuckTimeout: 50 * time.Millisecond}), &ServerConfig{
		RoomType:    newTestRoomType(t, RoomConfig{Modules: []Module{module}}),
		MemoryLimit: -1,
		Logger:      slog.New(slog.DiscardHandler),
		Development: true,
		Admission:   Admission{Off: true},
	})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})
	defer close(release)

	ws, _ := seatedGuest(t, "ws"+strings.TrimPrefix(httpServer.URL, "http"))
	sendFrame(t, ws, messageFrame(t, 3, "m", msgAdd, &addMsg{}))

	assert.Equal(t, wire.CloseReason_CLOSE_REASON_STUCK, readServerFrame(t, ws).GetClosed().GetReason())
}

func TestNewServerNeedsARoomType(t *testing.T) {
	_, err := NewServer(NewRuntime(Config{}), &ServerConfig{})

	require.Error(t, err)
}

func TestApplyMemoryLimit(t *testing.T) {
	const before, explicit int64 = 1 << 40, 1 << 30
	logger := slog.New(slog.DiscardHandler)

	tests := []struct {
		name  string
		env   string
		limit int64
		want  int64
	}{
		{name: "explicit limit", limit: explicit, want: explicit},
		{name: "negative leaves it alone", limit: -1, want: before},
		{name: "environment wins", env: "1GiB", limit: explicit, want: before},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GOMEMLIMIT", tt.env)
			previous := debug.SetMemoryLimit(before)
			t.Cleanup(func() { debug.SetMemoryLimit(previous) })

			applyMemoryLimit(tt.limit, logger)

			assert.Equal(t, tt.want, debug.SetMemoryLimit(-1))
		})
	}
}
