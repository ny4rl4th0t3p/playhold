package transport

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// Conn is one client connection. Its Send methods never block, so a slow client can't hold up a room.
type Conn struct {
	ctx          context.Context
	cancel       context.CancelFunc
	write        func(ctx context.Context, data []byte) error
	writeTimeout time.Duration
	logger       *slog.Logger

	mu     sync.Mutex
	latest *wire.RoomState // the newest state not yet written

	wake  chan struct{}          // says latest may be set
	queue chan *wire.ServerFrame // frames that can't be skipped

	replaced      chan struct{} // asks writeLoop to close the connection with StatusReplaced
	closeReplaced func()        // set by the server before writeLoop starts
	handshake     *time.Timer   // set by the server before any frame arrives
	onClose       []func()      // only touched on the connection's read goroutine
}

func newConn(
	ctx context.Context,
	cancel context.CancelFunc,
	cfg *Config,
	logger *slog.Logger,
	write func(ctx context.Context, data []byte) error,
) *Conn {
	return &Conn{
		ctx:          ctx,
		cancel:       cancel,
		write:        write,
		writeTimeout: cfg.WriteTimeout,
		logger:       logger,
		wake:         make(chan struct{}, 1),
		queue:        make(chan *wire.ServerFrame, cfg.QueueSize),
		replaced:     make(chan struct{}, 1),
	}
}

// HandshakeDone stops the handshake timeout. Call it once the client has identified itself.
func (c *Conn) HandshakeDone() { c.handshake.Stop() }

// OnClose registers fn to run after the connection has ended. Call it from Connect or a frame callback.
func (c *Conn) OnClose(fn func()) { c.onClose = append(c.onClose, fn) }

// Replace closes the connection with StatusReplaced, so the client knows not to reconnect. It doesn't wait.
func (c *Conn) Replace() {
	select {
	case c.replaced <- struct{}{}:
	default: // already asked
	}
}

// SendWelcome accepts the client's Hello.
func (c *Conn) SendWelcome(playerID, secret string) {
	c.enqueue(&wire.ServerFrame{Body: &wire.ServerFrame_Welcome{
		Welcome: &wire.Welcome{PlayerId: playerID, Secret: secret},
	}})
}

// SendJoined accepts the client's Join, the frame numbered seq.
func (c *Conn) SendJoined(seq uint64, code string) {
	c.enqueue(&wire.ServerFrame{Body: &wire.ServerFrame_Joined{
		Joined: &wire.Joined{Seq: seq, Code: code},
	}})
}

// SendState makes state the next one this client receives, replacing any not yet written. A slow client skips
// states and never builds a queue. The caller must not change state afterward.
func (c *Conn) SendState(state *wire.RoomState) {
	c.mu.Lock()
	c.latest = state
	c.mu.Unlock()

	select {
	case c.wake <- struct{}{}:
	default: // already signaled
	}
}

// SendError reports a problem with the client frame numbered seq.
func (c *Conn) SendError(seq uint64, code wire.ErrorCode, message string) {
	c.enqueue(&wire.ServerFrame{Body: &wire.ServerFrame_Error{
		Error: &wire.Error{Seq: seq, Code: code, Message: message},
	}})
}

// SendClosed tells the client its room has closed.
func (c *Conn) SendClosed(reason wire.CloseReason) {
	c.enqueue(&wire.ServerFrame{Body: &wire.ServerFrame_Closed{
		Closed: &wire.RoomClosed{Reason: reason},
	}})
}

// Close disconnects the client without waiting.
func (c *Conn) Close() { c.cancel() }

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.ctx.Done() }

// enqueue disconnects a client that has fallen too far behind to take frames that can't be skipped.
func (c *Conn) enqueue(frame *wire.ServerFrame) {
	select {
	case c.queue <- frame:
	default:
		c.logger.Warn("client too slow, disconnecting")
		c.cancel()
	}
}

func (c *Conn) takeState() *wire.RoomState {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.latest
	c.latest = nil
	return state
}

// writeLoop is the only goroutine that writes to the client. It ends when the connection does.
func (c *Conn) writeLoop() {
	for {
		var next *wire.ServerFrame
		select {
		case <-c.ctx.Done():
			return
		case <-c.replaced:
			c.closeReplaced()
			c.cancel()
			return
		case <-c.wake:
		case next = <-c.queue:
		}
		// The state goes first, so a RoomClosed frame stays the last thing the client hears from a room.
		if state := c.takeState(); state != nil {
			if !c.writeFrame(&wire.ServerFrame{Body: &wire.ServerFrame_State{State: state}}) {
				return
			}
		}
		if next != nil && !c.writeFrame(next) {
			return
		}
	}
}

// writeFrame reports whether the connection is still usable.
func (c *Conn) writeFrame(frame *wire.ServerFrame) bool {
	data, err := proto.Marshal(frame)
	if err != nil {
		c.logger.Error("encoding frame failed", "error", err)
		return true
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.writeTimeout)
	defer cancel()
	if err := c.write(ctx, data); err != nil {
		c.logger.Debug("writing frame failed", "error", err)
		c.cancel()
		return false
	}
	return true
}
