// Package sdk connects a Go program to a playhold server, for bots and load tests. It's minimal: unlike the TypeScript
// SDK it doesn't reconnect by itself, because a load test has to see and count what goes wrong. To come back after a
// dropped connection, connect again with the same identity and join the same code.
package sdk

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/coder/websocket"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

const protocolVersion = 1

// ErrRoomClosed means the player's room has closed for good.
var ErrRoomClosed = errors.New("playhold: the room closed")

var errUnexpectedFrame = errors.New("playhold: unexpected frame from the server")

// ErrorCode says what kind of problem the server reported.
type ErrorCode = wire.ErrorCode

// The problems a server can report.
const (
	ErrorMalformedFrame      = wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME
	ErrorUnknownMessage      = wire.ErrorCode_ERROR_CODE_UNKNOWN_MESSAGE
	ErrorBadMessage          = wire.ErrorCode_ERROR_CODE_BAD_MESSAGE
	ErrorQueueFull           = wire.ErrorCode_ERROR_CODE_QUEUE_FULL
	ErrorRoomClosed          = wire.ErrorCode_ERROR_CODE_ROOM_CLOSED
	ErrorRejected            = wire.ErrorCode_ERROR_CODE_REJECTED
	ErrorUnauthorized        = wire.ErrorCode_ERROR_CODE_UNAUTHORIZED
	ErrorRoomFull            = wire.ErrorCode_ERROR_CODE_ROOM_FULL
	ErrorUnsupportedProtocol = wire.ErrorCode_ERROR_CODE_UNSUPPORTED_PROTOCOL
	ErrorUnavailable         = wire.ErrorCode_ERROR_CODE_UNAVAILABLE
	ErrorServerFull          = wire.ErrorCode_ERROR_CODE_SERVER_FULL
	ErrorUpdateRequired      = wire.ErrorCode_ERROR_CODE_UPDATE_REQUIRED
	ErrorRateLimited         = wire.ErrorCode_ERROR_CODE_RATE_LIMITED
	ErrorTurnedOff           = wire.ErrorCode_ERROR_CODE_TURNED_OFF
	ErrorRoomNotFound        = wire.ErrorCode_ERROR_CODE_ROOM_NOT_FOUND
)

// Error is a problem the server reported. Seq is the number of the frame it's about.
type Error struct {
	Seq     uint64
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("playhold: %s: %s", e.Code, e.Message)
}

// Identity is a guest's ID and secret, which the server issues on the first visit. The zero value asks for a new guest.
type Identity struct {
	PlayerID string
	Secret   string
}

// Options says how to connect.
type Options struct {
	// URL is the server's WebSocket address.
	URL string
	// Identity is who to connect as.
	Identity Identity
	// SchemaVersions says which version of each module's messages this client was built for.
	SchemaVersions map[string]uint32
}

// ModuleView is what one module shows this player. View is that module's own message.
type ModuleView struct {
	Module string
	View   []byte
}

// Client is one connection to a server. Its methods aren't safe to call at the same time, except that one goroutine can
// read while another sends.
type Client struct {
	ws       *websocket.Conn
	identity Identity
	seq      uint64
}

// Connect opens a connection and says hello. It returns an *Error if the server refuses this client.
func Connect(ctx context.Context, opts *Options) (*Client, error) {
	ws, _, err := websocket.Dial(ctx, opts.URL, nil)
	if err != nil {
		return nil, err
	}
	c := &Client{ws: ws, identity: opts.Identity}
	if err := c.hello(ctx, opts.SchemaVersions); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) hello(ctx context.Context, versions map[string]uint32) error {
	_, err := c.send(ctx, &wire.ClientFrame{Body: &wire.ClientFrame_Hello{Hello: &wire.Hello{
		ProtocolVersion: protocolVersion,
		PlayerId:        c.identity.PlayerID,
		Secret:          c.identity.Secret,
		SchemaVersions:  versions,
	}}})
	if err != nil {
		return err
	}
	frame, err := c.read(ctx)
	if err != nil {
		return err
	}
	switch body := frame.GetBody().(type) {
	case *wire.ServerFrame_Welcome:
		c.identity.PlayerID = body.Welcome.GetPlayerId()
		if secret := body.Welcome.GetSecret(); secret != "" {
			c.identity.Secret = secret
		}
		return nil
	case *wire.ServerFrame_Error:
		return toError(body.Error)
	default:
		return errUnexpectedFrame
	}
}

// Identity returns who this client is, which is what to keep for connecting again.
func (c *Client) Identity() Identity { return c.identity }

// Join enters the room with this code, and waits for the answer. It returns an *Error if the room refuses this player,
// or with ErrorRoomNotFound if no room has the code and the server doesn't create rooms from codes clients choose. Asked
// to try again, with ErrorUnavailable or ErrorRateLimited, wait, connect again and repeat.
func (c *Client) Join(ctx context.Context, code string) error {
	_, err := c.join(ctx, code)
	return err
}

// CreateRoom creates a room, with a code the server makes, joins it, and returns the code, for other players to join
// with. Errors are as for Join.
func (c *Client) CreateRoom(ctx context.Context) (string, error) {
	return c.join(ctx, "")
}

// join sends a Join and waits for the answer, returning the room's code.
func (c *Client) join(ctx context.Context, code string) (string, error) {
	if _, err := c.send(ctx, &wire.ClientFrame{Body: &wire.ClientFrame_Join{Join: &wire.Join{Code: code}}}); err != nil {
		return "", err
	}
	for {
		frame, err := c.read(ctx)
		if err != nil {
			return "", err
		}
		switch body := frame.GetBody().(type) {
		case *wire.ServerFrame_Joined:
			return body.Joined.GetCode(), nil
		case *wire.ServerFrame_Error:
			return "", toError(body.Error)
		case *wire.ServerFrame_Closed:
			return "", closedError(body.Closed)
		}
	}
}

// Send sends a message to one of the room's modules, and returns the frame's number, which appears in any error about it.
func (c *Client) Send(ctx context.Context, module string, id uint32, msg proto.Message) (uint64, error) {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return 0, err
	}
	return c.send(ctx, &wire.ClientFrame{Body: &wire.ClientFrame_Message{
		Message: &wire.ModuleMessage{Module: module, Id: id, Payload: payload},
	}})
}

// Next waits for the player's next full view of the room. It returns an *Error if the server reported a problem, after
// which the connection can still be used, and ErrRoomClosed if the room has closed. Canceling ctx ends the connection.
func (c *Client) Next(ctx context.Context) ([]ModuleView, error) {
	for {
		frame, err := c.read(ctx)
		if err != nil {
			return nil, err
		}
		switch body := frame.GetBody().(type) {
		case *wire.ServerFrame_State:
			views := make([]ModuleView, 0, len(body.State.GetViews()))
			for _, view := range body.State.GetViews() {
				views = append(views, ModuleView{Module: view.GetModule(), View: view.GetView()})
			}
			return views, nil
		case *wire.ServerFrame_Error:
			return nil, toError(body.Error)
		case *wire.ServerFrame_Closed:
			return nil, closedError(body.Closed)
		}
	}
}

// Close ends the connection.
func (c *Client) Close() { _ = c.ws.CloseNow() }

func (c *Client) send(ctx context.Context, frame *wire.ClientFrame) (uint64, error) {
	c.seq++
	frame.Seq = c.seq
	data, err := proto.Marshal(frame)
	if err != nil {
		return 0, err
	}
	return c.seq, c.ws.Write(ctx, websocket.MessageBinary, data)
}

func (c *Client) read(ctx context.Context) (*wire.ServerFrame, error) {
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	frame := &wire.ServerFrame{}
	return frame, proto.Unmarshal(data, frame)
}

func toError(e *wire.Error) *Error {
	return &Error{Seq: e.GetSeq(), Code: e.GetCode(), Message: e.GetMessage()}
}

func closedError(closed *wire.RoomClosed) error {
	return fmt.Errorf("%w: %s", ErrRoomClosed, closed.GetReason())
}
