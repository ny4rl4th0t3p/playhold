// Package transport carries frames between clients and the server over WebSocket.
package transport

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/coder/websocket"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

// StatusReplaced is the WebSocket close status of a connection replaced by a newer one from the same player.
// A client should not reconnect after it, or two open tabs would keep replacing each other.
const StatusReplaced websocket.StatusCode = 4001

// Defaults for Config fields left at zero.
const (
	defaultMaxMessageBytes  = 32 << 10
	defaultMaxConns         = 4096
	defaultMaxConnsPerIP    = 16
	defaultHandshakeTimeout = 10 * time.Second
	defaultPingInterval     = 30 * time.Second
	defaultPingTimeout      = 10 * time.Second
	defaultWriteTimeout     = 10 * time.Second
	defaultQueueSize        = 32
)

// Config configures a Server. Zero values use the defaults.
type Config struct {
	// MaxMessageBytes is the largest client message allowed. A larger one closes the connection.
	MaxMessageBytes int64
	// MaxConns is the most connections the server accepts at once.
	MaxConns int
	// MaxConnsPerIP is the most connections accepted at once from one IP address.
	MaxConnsPerIP int
	// HandshakeTimeout is how long a new connection has to reach Conn.HandshakeDone.
	HandshakeTimeout time.Duration
	// PingInterval is the time between checks that a client is still there.
	PingInterval time.Duration
	// PingTimeout is how long a client has to answer a check.
	PingTimeout time.Duration
	// WriteTimeout is how long one frame can take to write.
	WriteTimeout time.Duration
	// QueueSize is how many frames that can't be skipped can wait for one client.
	QueueSize int
	// OriginPatterns lists the other web origins allowed to connect. The server's own host is always allowed.
	OriginPatterns []string
	// Logger receives transport logs. Nil uses slog.Default.
	Logger *slog.Logger
}

func (c *Config) withDefaults() *Config {
	out := *c
	setDefault(&out.MaxMessageBytes, defaultMaxMessageBytes)
	setDefault(&out.MaxConns, defaultMaxConns)
	setDefault(&out.MaxConnsPerIP, defaultMaxConnsPerIP)
	setDefault(&out.HandshakeTimeout, defaultHandshakeTimeout)
	setDefault(&out.PingInterval, defaultPingInterval)
	setDefault(&out.PingTimeout, defaultPingTimeout)
	setDefault(&out.WriteTimeout, defaultWriteTimeout)
	setDefault(&out.QueueSize, defaultQueueSize)
	if out.Logger == nil {
		out.Logger = slog.Default()
	}
	return &out
}

func setDefault[T int | int64 | time.Duration](field *T, value T) {
	if *field <= 0 {
		*field = value
	}
}

// Connect is called for each new connection and returns the function that receives its frames. That function
// runs on the connection's read goroutine, so it must not block. An error closes the connection.
type Connect func(c *Conn) (onFrame func(*wire.ClientFrame), err error)

// Server accepts WebSocket connections. It is an http.Handler.
type Server struct {
	cfg     *Config
	connect Connect
	ctx     context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	total  int
	perIP  map[string]int
	closed bool
	active sync.WaitGroup
}

// NewServer returns a Server that passes new connections to connect.
func NewServer(cfg *Config, connect Connect) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg:     cfg.withDefaults(),
		connect: connect,
		ctx:     ctx,
		cancel:  cancel,
		perIP:   make(map[string]int),
	}
}

// Close disconnects every client and waits for their connections to end. New connections are refused.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	s.active.Wait()
}

// ServeHTTP upgrades the request to a WebSocket and serves it until the connection ends.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if status := s.acquire(ip); status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer s.release(ip)

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.cfg.OriginPatterns})
	if err != nil { // Accept has already written the response
		s.cfg.Logger.Debug("websocket handshake failed", "error", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()
	ws.SetReadLimit(s.cfg.MaxMessageBytes)

	s.serve(ws)
}

func (s *Server) serve(ws *websocket.Conn) {
	ctx, cancel := context.WithCancel(s.ctx)
	conn := newConn(ctx, cancel, s.cfg, s.cfg.Logger, func(ctx context.Context, data []byte) error {
		return ws.Write(ctx, websocket.MessageBinary, data)
	})
	conn.closeReplaced = func() { _ = ws.Close(StatusReplaced, "replaced by a newer connection") }
	conn.handshake = time.AfterFunc(s.cfg.HandshakeTimeout, conn.Close)

	var loops sync.WaitGroup
	loops.Go(conn.writeLoop)
	loops.Go(func() { s.keepalive(ctx, conn, ws) })

	onFrame, err := s.connect(conn)
	if err != nil {
		s.cfg.Logger.Warn("connection refused", "error", err)
		_ = ws.Close(websocket.StatusInternalError, "connection refused")
	} else {
		s.readLoop(ctx, conn, ws, onFrame)
	}

	conn.handshake.Stop()
	cancel()
	loops.Wait()
	for _, fn := range conn.onClose {
		fn()
	}
}

func (s *Server) readLoop(ctx context.Context, conn *Conn, ws *websocket.Conn, onFrame func(*wire.ClientFrame)) {
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			s.cfg.Logger.Debug("connection ended", "error", err)
			return
		}
		if typ != websocket.MessageBinary {
			_ = ws.Close(websocket.StatusUnsupportedData, "binary frames only")
			return
		}
		frame := &wire.ClientFrame{}
		if err := proto.Unmarshal(data, frame); err != nil {
			conn.SendError(0, wire.ErrorCode_ERROR_CODE_MALFORMED_FRAME, "frame could not be decoded")
			continue
		}
		onFrame(frame)
	}
}

// keepalive disconnects a client that stops answering. It runs on its own goroutine because Ping waits for the
// answer, which the read goroutine receives.
func (s *Server) keepalive(ctx context.Context, conn *Conn, ws *websocket.Conn) {
	ticker := time.NewTicker(s.cfg.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, s.cfg.PingTimeout)
		err := ws.Ping(pingCtx)
		cancel()
		if err != nil {
			s.cfg.Logger.Debug("client stopped answering", "error", err)
			conn.Close()
			return
		}
	}
}

// acquire takes a connection slot, or returns the HTTP status to refuse the request with.
func (s *Server) acquire(ip string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed, s.total >= s.cfg.MaxConns:
		return http.StatusServiceUnavailable
	case s.perIP[ip] >= s.cfg.MaxConnsPerIP:
		return http.StatusTooManyRequests
	}
	s.total++
	s.perIP[ip]++
	s.active.Add(1)
	return http.StatusOK
}

func (s *Server) release(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total--
	s.perIP[ip]--
	if s.perIP[ip] == 0 {
		delete(s.perIP, ip)
	}
	s.active.Done()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
