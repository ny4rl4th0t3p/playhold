package playhold

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// These tests use a real network connection, which synctest bubbles don't support, so they use real time.

func gatewayContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// connectToRoom returns a client connected to the room as alice.
func connectToRoom(t *testing.T, room *Room) *websocket.Conn {
	t.Helper()
	server := transport.NewServer(&transport.Config{Logger: slog.New(slog.DiscardHandler)},
		connector(func(_ *transport.Conn) (*Room, PlayerID, error) { return room, "alice", nil }))
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	ws, _, err := websocket.Dial(gatewayContext(t), url, nil)
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

func messageFrame(t *testing.T, seq uint64, module string, id uint32, msg proto.Message) *wire.ClientFrame {
	t.Helper()
	payload, err := proto.Marshal(msg)
	require.NoError(t, err)
	return &wire.ClientFrame{Seq: seq, Body: &wire.ClientFrame_Message{
		Message: &wire.ModuleMessage{Module: module, Id: id, Payload: payload},
	}}
}

func TestMessageReachesItsHandlerAsThePlayer(t *testing.T) {
	type call struct {
		player PlayerID
		value  int32
	}
	calls := make(chan call, 1)
	module := registerFunc{name: "m", register: func(r *Registry) {
		Handle(r, msgAdd, func(rc *RoomContext, msg *addMsg) error {
			calls <- call{player: rc.Player(), value: msg.GetValue()}
			return nil
		})
	}}
	room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{module}})
	ws := connectToRoom(t, room)

	sendFrame(t, ws, messageFrame(t, 1, "m", msgAdd, &addMsg{Value: 3}))

	assert.Equal(t, call{player: "alice", value: 3}, <-calls)
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
			room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{refused}})
			ws := connectToRoom(t, room)

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
	room := newTestRoom(t, newTestRuntime(t), RoomConfig{QueueSize: 1, Modules: []Module{module}})
	ws := connectToRoom(t, room)
	defer close(release)

	sendFrame(t, ws, messageFrame(t, 1, "m", msgFail, &addMsg{}))
	waitFor(t, started)
	sendFrame(t, ws, messageFrame(t, 2, "m", msgAdd, &addMsg{}))
	sendFrame(t, ws, messageFrame(t, 3, "m", msgAdd, &addMsg{}))

	got := readServerFrame(t, ws).GetError()
	assert.Equal(t, wire.ErrorCode_ERROR_CODE_QUEUE_FULL, got.GetCode())
	assert.Equal(t, uint64(3), got.GetSeq())
}

func TestPlayersAreToldWhenTheRoomCloses(t *testing.T) {
	tests := []struct {
		name  string
		close func(t *testing.T, ws *websocket.Conn, room *Room)
		want  wire.CloseReason
	}{
		{
			name:  "closed on purpose",
			close: func(_ *testing.T, _ *websocket.Conn, room *Room) { room.Close() },
			want:  wire.CloseReason_CLOSE_REASON_REQUESTED,
		},
		{
			name: "handler panic",
			close: func(t *testing.T, ws *websocket.Conn, _ *Room) {
				t.Helper()
				sendFrame(t, ws, messageFrame(t, 1, "panicking", msgAdd, &addMsg{}))
			},
			want: wire.CloseReason_CLOSE_REASON_PANIC,
		},
	}
	panicking := registerFunc{name: "panicking", register: func(r *Registry) {
		Handle(r, msgAdd, func(_ *RoomContext, _ *addMsg) error { panic("boom") })
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			room := newTestRoom(t, newTestRuntime(t), RoomConfig{Modules: []Module{panicking}})
			ws := connectToRoom(t, room)

			tt.close(t, ws, room)

			assert.Equal(t, tt.want, readServerFrame(t, ws).GetClosed().GetReason())

			// The connection outlives the room, and later messages are refused.
			sendFrame(t, ws, messageFrame(t, 2, "panicking", msgAdd, &addMsg{}))
			assert.Equal(t, wire.ErrorCode_ERROR_CODE_ROOM_CLOSED, readServerFrame(t, ws).GetError().GetCode())
		})
	}
}

func TestBindErrorClosesTheConnection(t *testing.T) {
	server := transport.NewServer(&transport.Config{Logger: slog.New(slog.DiscardHandler)},
		connector(func(_ *transport.Conn) (*Room, PlayerID, error) { return nil, "", errors.New("no room") }))
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Close()
		httpServer.Close()
	})

	url := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	ws, _, err := websocket.Dial(gatewayContext(t), url, nil)
	require.NoError(t, err)
	defer func() { _ = ws.CloseNow() }()

	_, _, err = ws.Read(gatewayContext(t))
	assert.Equal(t, websocket.StatusInternalError, websocket.CloseStatus(err))
}
