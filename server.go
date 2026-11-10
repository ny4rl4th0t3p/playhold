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

	"go.opentelemetry.io/otel/trace"

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
	defaultGuestExpiry      = 90 * 24 * time.Hour
	guestExpiryEvery        = time.Hour
	guestExpiryTimeout      = time.Minute // deleting many guests at once can take a while
	restoreTimeout          = time.Minute
	closeTimeout            = 10 * time.Second
	readyTimeout            = 2 * time.Second
)

// ServerConfig configures a Server. Zero values use the defaults.
type ServerConfig struct {
	// RoomType is the kind of room the server creates. It is required.
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
	// CrashJournal keeps the last turns on disk, to find and reproduce what crashed the server. Off unless it has a Path.
	// See JournalConfig.
	CrashJournal JournalConfig
	// RestartWhenStuck lets the server restart its process when a stuck room's goroutine is still running 30 seconds after
	// the watchdog gave up on it, and the server has been overloaded for a minute since: most likely a goroutine spinning
	// forever, which Go can't stop. The server drains for 10 seconds, saves every room, and ListenAndServe returns
	// ErrRestartNeeded; a server not using ListenAndServe watches RestartNeeded. Set it only where a service manager, such
	// as systemd or a container restart policy, starts the process again; otherwise the server stays down. Without it,
	// the server logs an error every minute while that lasts. It needs admission control on.
	RestartWhenStuck bool
	// AllowClientCodes lets a Join with a code no room has create a room with that code, as a game that picks its own codes
	// needs. Without it, the default, only a Join with an empty code creates a room, with a long random code the server
	// makes, so nobody can guess another room's code or claim a name; a code no room has is refused with ROOM_NOT_FOUND.
	// Joining an existing room by its code works either way.
	AllowClientCodes bool
	// Matchmaking lets players queue to be matched with others, by the game's Matcher. Nil, the default, turns it off,
	// and a Queue is refused. See Matchmaking.
	Matchmaking *Matchmaking
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
	// GuestExpiry deletes a guest once they haven't been seen for this long: a returning guest is seen each time their
	// Hello is accepted, and a new one when they're created. The server checks every hour. A guest who comes back after
	// expiring is told their identity is unknown, and the SDKs start them again as a new guest. The default is 90 days.
	// Off keeps every guest, and the server logs a warning.
	GuestExpiry Timeout
	// HidePlayerIDs leaves players' IDs out of the server's logs and traces, for a game that would rather not record them.
	// A log line about a player's connection or turn then can't be traced back to that player. The crash journal, when on,
	// still records each turn's player, since reproducing a crash is what it's for. The server logs it at startup.
	HidePlayerIDs bool
	// TracerProvider receives OpenTelemetry traces: a span for each room's turn, each call to the store, and restoring the
	// rooms at startup. Nil traces nothing, at no cost. playhold only uses OpenTelemetry's API: the game sets up its SDK,
	// and where the traces go.
	TracerProvider trace.TracerProvider
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
	journal        *journal // nil if off
	breaker        *breaker
	metrics        *metrics
	stopBackground context.CancelFunc
}

// NewServer returns a Server that runs its rooms on rt. With a store, it first brings back every room saved there, and
// fails if it can't read them. A single room that can't be restored is logged and skipped.
func NewServer(rt *Runtime, cfg *ServerConfig) (*Server, error) {
	if err := checkConfig(cfg); err != nil {
		return nil, err
	}
	logger := serverLogger(cfg, rt)
	if cfg.Development {
		logger.Warn("development mode: the checks that keep a public server safe are off; never use it on a server players reach")
	} else if err := checkSecure(cfg); err != nil {
		return nil, err
	}
	m := newMetrics(cfg.RoomType)
	rt.useMetrics(m)
	tracer := newTracer(cfg.TracerProvider, logger)
	rt.useTracer(tracer)
	records := cfg.Store
	var writer *snapshotWriter
	if records == nil {
		records = memory.New()
	} else {
		if err := cfg.RoomType.requireSnapshots(); err != nil {
			return nil, err
		}
		writer = newSnapshotWriter(traceStore(records, tracer), logger)
		writer.interval, writer.metrics = cfg.SnapshotInterval, m
	}
	records = traceStore(records, tracer)
	applyMemoryLimit(cfg.MemoryLimit, logger)
	crashJournal, incidents, err := openIncidents(cfg, rt, records, m, logger) // before rooms are restored, so they use both
	if err != nil {
		return nil, err
	}

	allowConnect, guestRate, messageRate := newRateLimits(cfg, m, logger)
	admit := newAdmission(cfg.Admission.orDefault(), rt.queueFill, logger)
	g := &gateway{
		guests:      newGuests(records),
		lobby:       newServerLobby(rt, cfg, writer, admit, incidents, m, logger),
		logger:      logger,
		newGuests:   guestRate,
		messageRate: messageRate,
		admission:   admit,
		breaker:     incidents,
		metrics:     m,
	}
	if writer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
		defer cancel()
		if err := g.lobby.restoreAll(ctx, records); err != nil {
			writer.close(ctx)
			if crashJournal != nil {
				_ = crashJournal.close()
			}
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
	go g.lobby.matchmaker.run(background) // returns at once without matchmaking
	startGuestExpiry(background, cfg.GuestExpiry, g.guests, m, logger)
	server := &Server{
		writer:         writer,
		rt:             rt,
		lobby:          g.lobby,
		records:        records,
		logger:         logger,
		development:    cfg.Development,
		proxied:        len(cfg.TrustedProxies) > 0,
		admission:      admit,
		stuck:          stuck,
		journal:        crashJournal,
		breaker:        incidents,
		metrics:        m,
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
			AllowConnect:     allowConnect,
			Logger:           logger,
		}, g.connect),
	}
	server.watchGauges(g.guests)
	return server, nil
}

// startGuestExpiry deletes guests unseen for longer than the expiry, at once and then every hour, until ctx ends, unless
// it's off.
func startGuestExpiry(ctx context.Context, cfg Timeout, g *guests, m *metrics, logger *slog.Logger) {
	expiry := cfg.orDefault(defaultGuestExpiry)
	if expiry.Off {
		logger.Warn("guests never expire; set GuestExpiry, or delete old ones yourself, to keep no more than you need")
		return
	}
	go func() {
		ticker := time.NewTicker(guestExpiryEvery)
		defer ticker.Stop()
		for {
			expireCtx, cancel := context.WithTimeout(ctx, guestExpiryTimeout)
			deleted, err := g.expire(expireCtx, expiry.After)
			cancel()
			switch {
			case err != nil && ctx.Err() == nil:
				logger.Error("deleting guests unseen for too long failed; trying again in an hour", "error", err)
			case deleted > 0:
				logger.Info("deleted guests unseen for too long", "guests", deleted, "unseen_for", expiry.After)
				m.guestsExpired(deleted)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// newRateLimits reads the rate limit layers, with a warning for each one turned off. It returns what the transport asks
// before taking a new connection, the limit on new guests, and the limit on frames.
func newRateLimits(
	cfg *ServerConfig, m *metrics, logger *slog.Logger,
) (allowConnect func(ip string) bool, guests *perIP, messageRate RateLimit) {
	connectRate := cfg.ConnectRate.orDefault(defaultConnectRate)
	messageRate = cfg.MessageRate.orDefault(defaultMessageRate)
	guestRate := cfg.GuestRate.orDefault(defaultGuestRate)
	for layer, limit := range map[string]RateLimit{"ConnectRate": connectRate, "GuestRate": guestRate, "MessageRate": messageRate} {
		if limit.Off {
			logger.Warn("rate limit off; make sure something in front of the server limits this instead", "layer", layer)
		}
	}
	connects := newPerIP(connectRate)
	allowConnect = func(ip string) bool {
		if connects.allow(ip) {
			return true
		}
		m.limited("connect")
		return false
	}
	return allowConnect, newPerIP(guestRate), messageRate
}

// watchGauges reports what's running now each time Prometheus asks.
func (s *Server) watchGauges(players *guests) {
	m := s.metrics
	m.gauge("rooms", "Rooms running, counting finished ones until they close.", func() float64 { return float64(s.rt.roomCount()) })
	m.gauge("connections", "Open connections.", func() float64 { return float64(s.transport.Conns()) })
	m.gauge("players_connected", "Players with a connection.", func() float64 { return float64(players.connected()) })
	m.gauge("queue_fill", "The share of all rooms' inbound queues in use.", s.rt.queueFill)
	m.gauge("overloaded", "1 while admission control refuses new guests, rooms and seats.", func() float64 {
		if s.admission != nil && s.admission.overloaded.Load() {
			return 1
		}
		return 0
	})
	m.gauge("stuck_goroutines", "Goroutines of stuck rooms still running long after the watchdog gave up on them.",
		func() float64 { return float64(s.stuck.running.Load()) })
	m.gauge("messages_turned_off", "Messages the breaker refuses now.", func() float64 { return float64(s.breaker.turnedOffNow()) })
	m.gauge("players_waiting", "Players in the matchmaking queue.",
		func() float64 { return float64(s.lobby.matchmaker.waitingCount()) })
}

// openIncidents opens the crash journal, if there's one, and the breaker, which also counts the turns the journal shows
// were running when the process last died, and gives both to the runtime's rooms.
func openIncidents(
	cfg *ServerConfig, rt *Runtime, records store.Store, m *metrics, logger *slog.Logger,
) (*journal, *breaker, error) {
	crashJournal, err := openServerJournal(cfg.CrashJournal, rt, logger)
	if err != nil {
		return nil, nil, err
	}
	var crashed []JournalEntry
	if crashJournal != nil {
		crashed = crashJournal.crashed
	}
	build, err := currentBuild()
	if err != nil {
		logger.Warn("could not read the server's executable, so a new build won't turn messages back on", "error", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), restoreTimeout)
	defer cancel()
	incidents, err := newBreaker(ctx, records, build, logger, crashed)
	if err != nil {
		if crashJournal != nil {
			_ = crashJournal.close()
		}
		return nil, nil, err
	}
	incidents.metrics = m
	for range crashed {
		m.incident(JournalCrashed)
	}
	rt.useBreaker(incidents)
	return crashJournal, incidents, nil
}

// openServerJournal opens the crash journal if the config has a path, reports what it shows about the last crash, and
// gives it to the runtime's rooms. It returns nil if there's no path.
func openServerJournal(cfg JournalConfig, rt *Runtime, logger *slog.Logger) (*journal, error) {
	if cfg.Path == "" {
		logger.Info("no crash journal; set ServerConfig.CrashJournal.Path to keep the last turns before a crash")
		return nil, nil // no path means no journal, which isn't an error
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	j, err := openJournal(cfg, logger)
	if err != nil {
		return nil, err
	}
	rt.useJournal(j)
	logger.Info("crash journal on", "path", cfg.Path, "entries", cfg.Entries, "entry_size", cfg.EntrySize)
	return j, nil
}

// newServerLobby makes the lobby from the config: the room cap, the three timeouts with a warning for each turned off,
// admission control, and the breaker, which keeps rooms that kept crashing the server from being restored.
func newServerLobby(
	rt *Runtime, cfg *ServerConfig, writer *snapshotWriter, admit *admission, incidents *breaker, m *metrics,
	logger *slog.Logger,
) *lobby {
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
	l.breaker, l.metrics = incidents, m
	l.clientCodes = cfg.AllowClientCodes
	l.matchmaker = newMatchmaker(cfg.Matchmaking, l, m, logger)
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

// checkConfig refuses a config the server can't run with.
func checkConfig(cfg *ServerConfig) error {
	switch {
	case cfg.RoomType == nil:
		return errors.New("playhold: server config has no room type")
	case cfg.Matchmaking != nil && cfg.Matchmaking.Matcher == nil && cfg.RoomType.cfg.MaxPlayers <= 0:
		return errors.New("playhold: the default matcher makes groups of RoomConfig.MaxPlayers, which isn't set; " +
			"set it, or a Matcher")
	}
	return nil
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
	s.breaker.wait()
	if s.writer != nil {
		s.writer.close(ctx)
	}
	if s.journal != nil {
		_ = s.journal.close() // a second Shutdown finds it closed already, which is fine
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
