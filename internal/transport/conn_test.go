package transport

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"

	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// heldConn is a Conn whose writes wait until the test takes them, like a client that isn't reading.
type heldConn struct {
	*Conn
	written chan []byte
}

func newHeldConn(t *testing.T, cfg *Config) *heldConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	written := make(chan []byte)
	conn := newConn(ctx, cancel, cfg.withDefaults(), slog.New(slog.DiscardHandler), func(ctx context.Context, data []byte) error {
		select {
		case written <- data:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	var loop sync.WaitGroup
	loop.Go(conn.writeLoop)
	t.Cleanup(func() {
		cancel()
		loop.Wait()
	})
	return &heldConn{Conn: conn, written: written}
}

func (c *heldConn) next(t *testing.T) *wire.ServerFrame {
	t.Helper()
	frame := &wire.ServerFrame{}
	require.NoError(t, proto.Unmarshal(<-c.written, frame))
	return frame
}

func stateNamed(module string) *wire.RoomState {
	return &wire.RoomState{Views: []*wire.ModuleView{{Module: module}}}
}

func TestSlowClientSkipsStates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newHeldConn(t, &Config{})

		conn.SendState(stateNamed("first"))
		synctest.Wait() // the first state is now being written

		conn.SendState(stateNamed("second"))
		conn.SendState(stateNamed("third"))
		conn.SendState(stateNamed("fourth"))

		assert.Equal(t, "first", conn.next(t).GetState().GetViews()[0].GetModule())
		assert.Equal(t, "fourth", conn.next(t).GetState().GetViews()[0].GetModule())

		synctest.Wait()
		select {
		case <-conn.written:
			assert.Fail(t, "a skipped state was written")
		default:
		}
	})
}

func TestRoomClosedComesAfterTheLastState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newHeldConn(t, &Config{})

		conn.SendError(1, wire.ErrorCode_ERROR_CODE_REJECTED, "held")
		synctest.Wait()

		conn.SendState(stateNamed("last"))
		conn.SendClosed(wire.CloseReason_CLOSE_REASON_REQUESTED)

		assert.NotNil(t, conn.next(t).GetError())
		assert.NotNil(t, conn.next(t).GetState())
		assert.NotNil(t, conn.next(t).GetClosed())
	})
}

func TestQueuedFramesComeBeforeTheState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newHeldConn(t, &Config{})

		conn.SendError(1, wire.ErrorCode_ERROR_CODE_REJECTED, "held")
		synctest.Wait()

		conn.SendJoined(2, "room")
		conn.SendState(stateNamed("first view"))

		assert.NotNil(t, conn.next(t).GetError())
		assert.NotNil(t, conn.next(t).GetJoined())
		assert.NotNil(t, conn.next(t).GetState())
	})
}

func TestClientThatFallsBehindOnErrorsIsDisconnected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newHeldConn(t, &Config{QueueSize: 2})

		conn.SendError(1, wire.ErrorCode_ERROR_CODE_REJECTED, "being written")
		synctest.Wait()

		conn.SendError(2, wire.ErrorCode_ERROR_CODE_REJECTED, "queued")
		conn.SendError(3, wire.ErrorCode_ERROR_CODE_REJECTED, "queued")
		select {
		case <-conn.Done():
			assert.Fail(t, "disconnected before the queue overflowed")
		default:
		}

		conn.SendError(4, wire.ErrorCode_ERROR_CODE_REJECTED, "overflow")
		<-conn.Done()
	})
}

func TestConnectionLogsNameThePlayerOnceKnown(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// No writer runs, so the queue of one fills at once and every frame after it overflows.
	conn := newConn(ctx, cancel, (&Config{QueueSize: 1}).withDefaults(), slog.New(slog.NewTextHandler(&logs, nil)),
		func(_ context.Context, _ []byte) error { return nil })
	conn.SendError(1, wire.ErrorCode_ERROR_CODE_REJECTED, "queued")

	conn.SendError(2, wire.ErrorCode_ERROR_CODE_REJECTED, "overflow")
	assert.Contains(t, logs.String(), "client too slow")
	assert.NotContains(t, logs.String(), "player=", "not known before Hello")

	logs.Reset()
	conn.SetPlayer("alice")
	conn.SendError(3, wire.ErrorCode_ERROR_CODE_REJECTED, "overflow")
	assert.Contains(t, logs.String(), "player=alice")
}

func TestSendAfterTheWriterStopsDoesNotBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := newHeldConn(t, &Config{QueueSize: 1})
		conn.Close()
		synctest.Wait()

		for range 3 {
			conn.SendState(stateNamed("ignored"))
			conn.SendError(1, wire.ErrorCode_ERROR_CODE_REJECTED, "ignored")
		}
	})
}
