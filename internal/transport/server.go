// Package transport carries frames between clients and the server over WebSocket.
package transport

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
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
	// TrustedProxies lists the addresses of the proxies in front of the server. Only a request from one of them has its
	// client address read from a header; from anywhere else, the header could be made up.
	TrustedProxies []netip.Prefix
	// ProxyClientIP reads the client's address from a request that came through a trusted proxy, and reports whether it
	// found one. Nil reads X-Forwarded-For.
	ProxyClientIP func(r *http.Request) (netip.Addr, bool)
	// RequireSecure refuses a connection that arrived without TLS and not through a trusted proxy.
	RequireSecure bool
	// AllowConnect reports whether a new connection from this IP address is allowed now. Nil allows every one.
	AllowConnect func(ip string) bool
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
// runs on the connection's read goroutine, so waiting there only holds up this client, and it must not wait on anyone
// else's. An error closes the connection.
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

// Conns returns how many connections are open.
func (s *Server) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// ServeHTTP upgrades the request to a WebSocket and serves it until the connection ends.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip, proxied := s.clientIP(r)
	if s.cfg.RequireSecure && r.TLS == nil && !proxied {
		s.cfg.Logger.Warn("connection refused: no TLS and not through a trusted proxy", "remote_addr", r.RemoteAddr)
		http.Error(w, "TLS required", http.StatusForbidden)
		return
	}
	if s.cfg.AllowConnect != nil && !s.cfg.AllowConnect(ip) {
		http.Error(w, "too many new connections from this address", http.StatusTooManyRequests)
		return
	}
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

	s.serve(ws, ip)
}

func (s *Server) serve(ws *websocket.Conn, ip string) {
	ctx, cancel := context.WithCancel(s.ctx)
	conn := newConn(ctx, cancel, s.cfg, s.cfg.Logger, func(ctx context.Context, data []byte) error {
		return ws.Write(ctx, websocket.MessageBinary, data)
	})
	conn.ip = ip
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

func (*Server) readLoop(ctx context.Context, conn *Conn, ws *websocket.Conn, onFrame func(*wire.ClientFrame)) {
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			conn.log().Debug("connection ended", "error", err)
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
			conn.log().Debug("client stopped answering", "error", err)
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

// clientIP returns the client's address, and whether the request came through a trusted proxy. Behind one, the address
// comes from the proxy's header; if the header gives none, it's the proxy's own.
func (s *Server) clientIP(r *http.Request) (ip string, proxied bool) {
	addrPort, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr, false
	}
	remote := addrPort.Addr().Unmap()
	if !trusted(s.cfg.TrustedProxies, remote) {
		return remote.String(), false
	}
	read := s.cfg.ProxyClientIP
	if read == nil {
		read = func(r *http.Request) (netip.Addr, bool) { return forwardedFor(r, s.cfg.TrustedProxies) }
	}
	if client, ok := read(r); ok {
		return client.Unmap().String(), true
	}
	return remote.String(), true
}

// forwardedFor returns the rightmost address in X-Forwarded-For that isn't a trusted proxy. Each proxy appends the
// address it got the request from, so everything left of the first untrusted one could have been made up by the client.
func forwardedFor(r *http.Request, proxies []netip.Prefix) (netip.Addr, bool) {
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return netip.Addr{}, false
		}
		if addr = addr.Unmap(); !trusted(proxies, addr) {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

func trusted(proxies []netip.Prefix, addr netip.Addr) bool {
	for _, p := range proxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
