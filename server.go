package playhold

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/ny4rl4th0t3p/playhold/internal/memlimit"
	"github.com/ny4rl4th0t3p/playhold/internal/transport"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

const (
	defaultEmptyRoomTimeout = 5 * time.Minute
	defaultMaxRooms         = 10_000
	restoreTimeout          = time.Minute
	closeTimeout            = 10 * time.Second
	readyTimeout            = 2 * time.Second
)

// ServerConfig configures a Server. Zero values use the defaults.
type ServerConfig struct {
	// RoomType is the kind of room that joining a new code creates. It is required.
	RoomType *RoomType
	// Store keeps what has to survive a restart, and every module with state then needs a Snapshot. Nil keeps guests in
	// memory and doesn't save rooms, so a restart forgets both.
	Store store.Store
	// TimersAfterRestart says what happens to pending timers when the server comes back. The default is TimersPause.
	TimersAfterRestart TimersAfterRestart
	// EmptyRoomTimeout is how long a room stays open with nobody connected. The default is 5 minutes.
	EmptyRoomTimeout time.Duration
	// MaxRooms is the most rooms the server runs at once, counting finished ones until they close. Over it, joining a
	// new code gets an error, and existing rooms carry on. The default is 10,000.
	MaxRooms int
	// OriginPatterns lists the other web origins allowed to connect, such as "*.itch.zone". The server's own
	// host is always allowed.
	OriginPatterns []string
	// MaxConns is the most connections the server accepts at once.
	MaxConns int
	// MaxConnsPerIP is the most connections accepted at once from one IP address.
	MaxConnsPerIP int
	// MaxMessageBytes is the largest client message allowed. A larger one closes the connection.
	MaxMessageBytes int64
	// HandshakeTimeout is how long a new connection has to say Hello.
	HandshakeTimeout time.Duration
	// MemoryLimit is the process's soft memory limit in bytes. Zero uses about 80% of the machine's memory,
	// and a negative value leaves the limit alone. The GOMEMLIMIT environment variable always wins.
	MemoryLimit int64
	// Logger receives server logs. Nil uses the runtime's logger.
	Logger *slog.Logger
}

// Server lets clients connect, identify themselves and join rooms over WebSocket. It is an http.Handler.
type Server struct {
	transport *transport.Server
	writer    *snapshotWriter // nil if rooms aren't saved
	rt        *Runtime
	lobby     *lobby
	records   store.Store
}

// NewServer returns a Server that runs its rooms on rt. With a store, it first brings back every room saved there, and
// fails if it can't read them. A single room that can't be restored is logged and skipped.
func NewServer(rt *Runtime, cfg *ServerConfig) (*Server, error) {
	if cfg.RoomType == nil {
		return nil, errors.New("playhold: server config has no room type")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = rt.logger
	}
	idle := cfg.EmptyRoomTimeout
	if idle <= 0 {
		idle = defaultEmptyRoomTimeout
	}
	maxRooms := cfg.MaxRooms
	if maxRooms <= 0 {
		maxRooms = defaultMaxRooms
	}
	records := cfg.Store
	var writer *snapshotWriter
	if records == nil {
		records = memory.New()
	} else {
		if err := cfg.RoomType.requireSnapshots(); err != nil {
			return nil, err
		}
		writer = newSnapshotWriter(records, logger)
	}
	applyMemoryLimit(cfg.MemoryLimit, logger)

	g := &gateway{guests: newGuests(records), lobby: newLobby(rt, cfg.RoomType, idle, maxRooms, writer), logger: logger}
	g.lobby.timers = cfg.TimersAfterRestart
	if writer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
		defer cancel()
		if err := g.lobby.restoreAll(ctx, records); err != nil {
			writer.close(ctx)
			return nil, err
		}
	}
	return &Server{
		writer:  writer,
		rt:      rt,
		lobby:   g.lobby,
		records: records,
		transport: transport.NewServer(&transport.Config{
			MaxMessageBytes:  cfg.MaxMessageBytes,
			MaxConns:         cfg.MaxConns,
			MaxConnsPerIP:    cfg.MaxConnsPerIP,
			HandshakeTimeout: cfg.HandshakeTimeout,
			OriginPatterns:   cfg.OriginPatterns,
			Logger:           logger,
		}, g.connect),
	}, nil
}

// ServeHTTP upgrades the request to a WebSocket and serves it until the connection ends.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.transport.ServeHTTP(w, r) }

// Drain stops new rooms and new players, and makes Ready fail, so a load balancer stops sending players here. Rooms carry
// on, and players with a seat can still come back to it. Clients asked to wait get an error that makes them retry, which
// reaches the next server once this one has gone.
func (s *Server) Drain() { s.lobby.drain() }

// Shutdown stops the server without losing a match: it drains, closes every room, which saves it, writes everything
// waiting to be saved, and only then disconnects the clients, who reconnect and find their rooms again on the next server.
// It also shuts down the Runtime. It returns the context's error if a room didn't finish in time, though it still does
// the rest. It's safe to call more than once.
func (s *Server) Shutdown(ctx context.Context) error {
	s.Drain()
	err := s.rt.Shutdown(ctx)
	if s.writer != nil {
		s.writer.close(ctx)
	}
	s.transport.Close()
	return err
}

// Close is Shutdown with a default time limit, for when nothing is waiting on the result.
func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		s.rt.logger.Warn("server shutdown", "error", err)
	}
}

// Live answers a liveness check: the process is up. It always succeeds, because restarting a server doesn't help with
// anything Ready would report.
func (*Server) Live(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// Ready answers a readiness check: whether this server should be sent players. It fails while draining, and while the
// store can't be reached, since then new players can't be identified and games aren't being saved.
func (s *Server) Ready(w http.ResponseWriter, r *http.Request) {
	if s.lobby.isDraining() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := s.records.Ping(ctx); err != nil {
		s.rt.logger.Warn("readiness check failed", "error", err)
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// applyMemoryLimit sets the soft memory limit, which makes the garbage collector work harder before the machine
// runs out, instead of the whole process crashing and taking every room with it.
func applyMemoryLimit(limit int64, logger *slog.Logger) {
	if limit < 0 || os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	if limit == 0 {
		limit = memlimit.Default(os.DirFS("/"))
	}
	if limit == 0 {
		logger.Warn("could not read the machine's memory, so no memory limit was set")
		return
	}
	debug.SetMemoryLimit(limit)
}
