package playhold

import (
	"errors"

	"github.com/ny4rl4th0t3p/playhold/internal/transport"
	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// bindFunc decides which room and player a new connection belongs to.
type bindFunc func(c *transport.Conn) (*Room, PlayerID, error)

// connector returns the transport callback that routes a connection's frames to its room.
func connector(bind bindFunc) transport.Connect {
	return func(c *transport.Conn) (func(*wire.ClientFrame), error) {
		room, player, err := bind(c)
		if err != nil {
			return nil, err
		}
		go func() {
			select {
			case <-room.Done():
				c.SendClosed(wireCloseReason(room.Wait()))
			case <-c.Done():
			}
		}()
		return func(frame *wire.ClientFrame) { routeFrame(c, room, player, frame) }, nil
	}
}

func routeFrame(c *transport.Conn, room *Room, player PlayerID, frame *wire.ClientFrame) {
	seq := frame.GetSeq()
	msg := frame.GetMessage()
	if msg == nil || msg.GetModule() == "" {
		c.SendError(seq, wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME, "expected a message for a module")
		return
	}
	report := func(err error) {
		if err != nil {
			c.SendError(seq, wireErrorCode(err), err.Error())
		}
	}
	report(room.dispatch(player, msg.GetModule(), msg.GetId(), msg.GetPayload(), report))
}

func wireErrorCode(err error) wire.ErrorCode {
	switch {
	case errors.Is(err, ErrUnknownMessage):
		return wire.ErrorCode_ERROR_CODE_UNKNOWN_MESSAGE
	case errors.Is(err, ErrBadMessage):
		return wire.ErrorCode_ERROR_CODE_BAD_MESSAGE
	case errors.Is(err, ErrQueueFull):
		return wire.ErrorCode_ERROR_CODE_QUEUE_FULL
	case errors.Is(err, ErrRoomClosed):
		return wire.ErrorCode_ERROR_CODE_ROOM_CLOSED
	default:
		return wire.ErrorCode_ERROR_CODE_REJECTED
	}
}

func wireCloseReason(reason CloseReason) wire.CloseReason {
	switch reason {
	case CloseRequested:
		return wire.CloseReason_CLOSE_REASON_REQUESTED
	case CloseShutdown:
		return wire.CloseReason_CLOSE_REASON_SHUTDOWN
	case ClosePanic:
		return wire.CloseReason_CLOSE_REASON_PANIC
	default:
		return wire.CloseReason_CLOSE_REASON_UNSPECIFIED
	}
}
