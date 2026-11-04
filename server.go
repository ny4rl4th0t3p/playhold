package playhold

import (
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

const defaultEmptyRoomTimeout = 5 * time.Minute

// ServerConfig configures a Server. Zero values use the defaults.
type ServerConfig struct {
	// RoomType is the kind of room that joining a new code creates. It is required.
	RoomType *RoomType
	// Store keeps what has to survive a restart. Nil keeps everything in memory, so a restart forgets it.
	Store store.Store
	// EmptyRoomTimeout is how long a room stays open with nobody connected. The default is 5 minutes.
	EmptyRoomTimeout time.Duration
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
}

// NewServer returns a Server that runs its rooms on rt.
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
	records := cfg.Store
	if records == nil {
		records = memory.New()
	}
	applyMemoryLimit(cfg.MemoryLimit, logger)

	g := &gateway{guests: newGuests(records), lobby: newLobby(rt, cfg.RoomType, idle), logger: logger}
	return &Server{transport: transport.NewServer(&transport.Config{
		MaxMessageBytes:  cfg.MaxMessageBytes,
		MaxConns:         cfg.MaxConns,
		MaxConnsPerIP:    cfg.MaxConnsPerIP,
		HandshakeTimeout: cfg.HandshakeTimeout,
		OriginPatterns:   cfg.OriginPatterns,
		Logger:           logger,
	}, g.connect)}, nil
}

// ServeHTTP upgrades the request to a WebSocket and serves it until the connection ends.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.transport.ServeHTTP(w, r) }

// Close disconnects every client and refuses new ones. Rooms keep running until the Runtime shuts down.
func (s *Server) Close() { s.transport.Close() }

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
