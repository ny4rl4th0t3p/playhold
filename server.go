package playhold

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ny4rl4th0t3p/playhold/internal/memlimit"
	"github.com/ny4rl4th0t3p/playhold/internal/transport"
	"github.com/ny4rl4th0t3p/playhold/store"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

const (
	defaultEmptyRoomTimeout = 5 * time.Minute
	defaultMaxRooms         = 10_000
	defaultDisconnectGrace  = time.Minute
	defaultInactiveRoom     = 30 * time.Minute
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
	// SnapshotInterval is the least time between two saves of a room. Zero, the default, saves after every change. With
	// an interval, a change that comes sooner waits until the interval has passed, and the changes made meanwhile are
	// saved together, so a crash, or a room restored after a panic, can lose up to one interval of moves. A shutdown
	// still saves every room, and while the server drains every change is saved at once. It only saves work in rooms that
	// change more than once per interval.
	SnapshotInterval time.Duration
	// TimersAfterRestart says what happens to pending timers when the server comes back. The default is TimersPause.
	TimersAfterRestart TimersAfterRestart
	// DisconnectGrace is how long a seated player has to come back before the game gets a PlayerDisconnected. The default
	// is 1 minute. It starts over when the server restarts, since that's what cut everyone off. Off never tells the game
	// that a player left.
	DisconnectGrace Timeout
	// EmptyRoomTimeout is how long a room stays open with nobody connected. The default is 5 minutes. A room closing
	// doesn't wait for the grace periods, so keep this longer than DisconnectGrace if the game cares about those events.
	// Off keeps empty rooms open, and then only InactiveRoomTimeout closes them.
	EmptyRoomTimeout Timeout
	// InactiveRoomTimeout closes a room once no player has sent it anything for this long, connected or not: a message,
	// or joining or coming back. Timer turns don't count, so a room kept busy only by its own timers closes too. Its saved
	// state is deleted, and players are told it closed as inactive. The default is 30 minutes. The time starts over when
	// the server restarts.
	InactiveRoomTimeout Timeout
	// Admission refuses new guests, rooms and seats while the server is overloaded: memory near its limit, goroutines
	// waiting too long to run, or rooms' queues filling up. Rooms carry on, and players with a seat can always come back,
	// so connections themselves are never refused for this: who is connecting is only known once they say Hello. See
	// Admission for the defaults.
	Admission Admission
	// RestartWhenStuck lets the server restart its process when a stuck room's goroutine is still running 30 seconds after
	// the watchdog gave up on it, and the server has been overloaded for a minute since: most likely a goroutine spinning
	// forever, which Go can't stop. The server drains for 10 seconds, saves every room, and ListenAndServe returns
	// ErrRestartNeeded; a server not using ListenAndServe watches RestartNeeded. Set it only where a service manager, such
	// as systemd or a container restart policy, starts the process again; otherwise the server stays down. Without it,
	// the server logs an error every minute while that lasts. It needs admission control on.
	RestartWhenStuck bool
	// MaxRooms is the most rooms the server runs at once, counting finished ones until they close. Over it, joining a
	// new code gets an error, and existing rooms carry on. The default is 10,000.
	MaxRooms int
	// OriginPatterns lists the other web origins allowed to connect, such as "*.itch.zone". The server's own
	// host is always allowed. A pattern that matches every origin, such as "*", is refused outside development.
	OriginPatterns []string
	// TrustedProxies lists the address ranges of the proxies in front of the server, such as a load balancer that ends
	// TLS. A connection from one of them may arrive without TLS, and its client address is read from the proxy's header.
	// From anywhere else, that header is ignored, since a client could make it up. A range covering every address is
	// refused outside development.
	TrustedProxies []netip.Prefix
	// ProxyClientIP reads the client's address from a request that came through a trusted proxy, and reports whether it
	// found one; if not, the proxy's own address is used. Nil reads X-Forwarded-For, taking the rightmost address that
	// isn't a trusted proxy. Set it for a proxy that sends another header, such as Forwarded.
	ProxyClientIP func(r *http.Request) (netip.Addr, bool)
	// ConnectRate limits new connections from one IP address. Over it, the connection is refused with HTTP 429. The
	// default is one every 200 ms, with bursts of 20, so a whole network behind one address can reconnect after a
	// restart.
	ConnectRate RateLimit
	// GuestRate limits new guests created from one IP address, each a write to the store. Over it, the Hello gets a
	// rate-limited error and no guest is created. The default is one every 6 seconds, with bursts of 20.
	GuestRate RateLimit
	// MessageRate limits the frames one connection sends, of every kind. Over it, the frame gets a rate-limited error and
	// isn't handled. The default is one every 50 ms, with bursts of 40. A player has one connection at a time, so this is
	// also the limit per player. Modules can limit their own messages further with Registry.RateLimit.
	MessageRate RateLimit
	// Development turns off the checks that keep a public server safe, so it can run on a developer's machine over plain
	// HTTP. Without it, NewServer refuses settings that would expose players, a connection that arrives without TLS and
	// not through a trusted proxy is refused, and ListenAndServe won't start without TLS or a trusted proxy. Never set it
	// on a server players reach; the server logs a warning when it's on.
	Development bool
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
	transport   *transport.Server
	writer      *snapshotWriter // nil if rooms aren't saved
	rt          *Runtime
	lobby       *lobby
	records     store.Store
	logger      *slog.Logger
	development bool
	proxied     bool // there are trusted proxies, which may end TLS in front of the server

	admission      *admission // nil if off
	stuck          *stuckWatch
	stopBackground context.CancelFunc
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
	if cfg.Development {
		logger.Warn("development mode: the checks that keep a public server safe are off; never use it on a server players reach")
	} else if err := checkSecure(cfg); err != nil {
		return nil, err
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
		writer.interval = cfg.SnapshotInterval
	}
	applyMemoryLimit(cfg.MemoryLimit, logger)

	connectRate := cfg.ConnectRate.orDefault(defaultConnectRate)
	messageRate := cfg.MessageRate.orDefault(defaultMessageRate)
	guestRate := cfg.GuestRate.orDefault(defaultGuestRate)
	for layer, limit := range map[string]RateLimit{"ConnectRate": connectRate, "GuestRate": guestRate, "MessageRate": messageRate} {
		if limit.Off {
			logger.Warn("rate limit off; make sure something in front of the server limits this instead", "layer", layer)
		}
	}
	connects := newPerIP(connectRate)
	admit := newAdmission(cfg.Admission.orDefault(), rt.queueFill, logger)

	g := &gateway{
		guests:      newGuests(records),
		lobby:       newServerLobby(rt, cfg, writer, admit, logger),
		logger:      logger,
		newGuests:   newPerIP(guestRate),
		messageRate: messageRate,
		admission:   admit,
	}
	if writer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
		defer cancel()
		if err := g.lobby.restoreAll(ctx, records); err != nil {
			writer.close(ctx)
			return nil, err
		}
	}
	background, stopBackground := context.WithCancel(context.Background())
	if admit != nil {
		admit.check(admit.sample()) // so the server starts knowing where it stands
		go admit.run(background)
	}
	stuck := newStuckWatch(logger, admit, cfg.RestartWhenStuck, g.lobby.drain)
	rt.watchStuck(func(room *Room) { go stuck.follow(background, room) })
	go stuck.run(background)
	return &Server{
		writer:         writer,
		rt:             rt,
		lobby:          g.lobby,
		records:        records,
		logger:         logger,
		development:    cfg.Development,
		proxied:        len(cfg.TrustedProxies) > 0,
		admission:      admit,
		stuck:          stuck,
		stopBackground: stopBackground,
		transport: transport.NewServer(&transport.Config{
			MaxMessageBytes:  cfg.MaxMessageBytes,
			MaxConns:         cfg.MaxConns,
			MaxConnsPerIP:    cfg.MaxConnsPerIP,
			HandshakeTimeout: cfg.HandshakeTimeout,
			OriginPatterns:   cfg.OriginPatterns,
			TrustedProxies:   cfg.TrustedProxies,
			ProxyClientIP:    cfg.ProxyClientIP,
			RequireSecure:    !cfg.Development,
			AllowConnect:     connects.allow,
			Logger:           logger,
		}, g.connect),
	}, nil
}

// newServerLobby makes the lobby from the config: the room cap, the three timeouts with a warning for each turned off,
// and admission control.
func newServerLobby(rt *Runtime, cfg *ServerConfig, writer *snapshotWriter, admit *admission, logger *slog.Logger) *lobby {
	idle := cfg.EmptyRoomTimeout.orDefault(defaultEmptyRoomTimeout)
	grace := cfg.DisconnectGrace.orDefault(defaultDisconnectGrace)
	inactive := cfg.InactiveRoomTimeout.orDefault(defaultInactiveRoom)
	if idle.Off {
		logger.Warn("rooms with nobody connected never close; only InactiveRoomTimeout closes them")
	}
	if grace.Off {
		logger.Info("the game is never told that a player disconnected")
	}
	if inactive.Off {
		logger.Warn("rooms never close for inactivity; a room with an idle player or only timers stays open and saved")
	}
	maxRooms := cfg.MaxRooms
	if maxRooms <= 0 {
		maxRooms = defaultMaxRooms
	}
	l := newLobby(rt, cfg.RoomType, idle.duration(), maxRooms, writer)
	l.timers = cfg.TimersAfterRestart
	l.grace = grace.duration()
	l.inactive = inactive.duration()
	l.admit = admit.allow
	return l
}

// Timeout is how long to wait before something happens. The zero value uses the default; there is no way to turn it
// off by leaving it out. Off turns it off, and the server logs a warning at startup.
type Timeout struct {
	After time.Duration
	Off   bool
}

func (t Timeout) orDefault(d time.Duration) Timeout {
	if !t.Off && t.After <= 0 {
		t.After = d
	}
	return t
}

// duration is the time to wait, or zero if it's off, which is how the lobby and rooms are told.
func (t Timeout) duration() time.Duration {
	if t.Off {
		return 0
	}
	return t.After
}

// checkSecure refuses settings that would expose players on a public server.
func checkSecure(cfg *ServerConfig) error {
	// Hosts no real game uses, so a pattern matching them would match any site.
	probes := []string{"playhold-origin-check.invalid", "playhold-origin-check.invalid:443"}
	for _, pattern := range cfg.OriginPatterns {
		for _, probe := range probes {
			if matched, err := path.Match(strings.ToLower(pattern), probe); err != nil || matched {
				return fmt.Errorf("playhold: origin pattern %q would let any website connect; list the origins instead", pattern)
			}
		}
	}
	for _, p := range cfg.TrustedProxies {
		if !p.IsValid() || p.Bits() == 0 {
			return fmt.Errorf("playhold: trusted proxy range %q would trust every client's header; list the proxies instead", p)
		}
	}
	return nil
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
	s.stopBackground()
	s.Drain()
	err := s.rt.Shutdown(ctx)
	if s.writer != nil {
		s.writer.close(ctx)
	}
	s.transport.Close()
	return err
}

// RestartNeeded is closed when the server needs its process restarted, with RestartWhenStuck on: the server is already
// drained, and the caller should Shutdown and exit, for its service manager to start it again. ListenAndServe does this
// itself.
func (s *Server) RestartNeeded() <-chan struct{} { return s.stuck.restartNeeded }

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
