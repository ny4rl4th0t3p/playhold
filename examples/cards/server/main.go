// Command server runs the sample game. Clients connect at /ws, and with -web it also serves the built browser page.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/examples/cards"
	"github.com/ny4rl4th0t3p/playhold/store/postgres"
)

const (
	readHeaderTimeout      = 5 * time.Second
	defaultShutdownTimeout = 8 * time.Second
)

func main() {
	f := defineFlags(flag.CommandLine)
	flag.Parse()
	cfg, listen, err := f.config()
	if err != nil {
		log.Fatal(err)
	}
	if f.debugAddr != "" {
		serveDebug(f.debugAddr)
	}
	if err := run(f.databaseURL, f.options, cfg, listen); err != nil {
		log.Fatal(err)
	}
}

// flags are the command line as given, before config checks it.
type flags struct {
	addr, web, databaseURL, debugAddr string

	dev bool

	tlsDomains, tlsCache, tlsEmail, tlsDirectory, tlsCert, tlsKey string

	proxies, origins, timers string

	connectRate, guestRate, messageRate string

	emptyRoom, grace, inactiveRoom string

	maxPerIP, maxRooms int

	snapshotInterval, shutdownTimeout time.Duration

	options cards.Options
}

func defineFlags(fs *flag.FlagSet) *flags {
	f := &flags{}
	fs.StringVar(&f.addr, "addr", "", "address to listen on; empty uses :443 with TLS and :8080 without")
	fs.BoolVar(&f.dev, "dev", false,
		"development mode: plain HTTP without a proxy, and the safety checks off; never on a server players reach")
	fs.StringVar(&f.tlsDomains, "tls-domains", "", "comma-separated domains for automatic certificates from Let's Encrypt")
	fs.StringVar(&f.tlsCache, "tls-cache", "", "folder that keeps the automatic certificates across restarts")
	fs.StringVar(&f.tlsEmail, "tls-email", "", "email Let's Encrypt writes to about the certificates; optional")
	fs.StringVar(&f.tlsDirectory, "tls-acme-directory", "",
		"ACME directory for automatic certificates; empty uses Let's Encrypt's; "+
			"use its staging directory, with its own -tls-cache, to check a first setup")
	fs.StringVar(&f.tlsCert, "tls-cert", "", "certificate file (PEM), instead of automatic certificates")
	fs.StringVar(&f.tlsKey, "tls-key", "", "the certificate's key file (PEM)")
	fs.StringVar(&f.proxies, "trusted-proxies", "",
		"comma-separated address ranges of the proxies in front of this server, such as 10.0.0.0/8")
	fs.StringVar(&f.web, "web", "", "folder with the built browser page, to serve it from this server")
	fs.StringVar(&f.origins, "origins", "", "comma-separated web origins allowed to connect, besides this server's own")
	fs.IntVar(&f.maxPerIP, "max-conns-per-ip", 0, "most connections from one IP address; 0 uses the default")
	fs.StringVar(&f.connectRate, "connect-rate", "",
		"new connections per IP address, as EVERY/BURST such as 200ms/20, or off; empty uses the default")
	fs.StringVar(&f.guestRate, "guest-rate", "",
		"new guests per IP address, as EVERY/BURST such as 6s/20, or off; empty uses the default")
	fs.StringVar(&f.messageRate, "message-rate", "",
		"frames per connection, as EVERY/BURST such as 50ms/40, or off; empty uses the default")
	fs.StringVar(&f.emptyRoom, "empty-room-timeout", "",
		"how long a room stays open with nobody connected, or off; empty uses the default")
	fs.StringVar(&f.grace, "disconnect-grace", "",
		"how long a player can be disconnected before others are told, or off; empty uses the default")
	fs.StringVar(&f.inactiveRoom, "inactive-room-timeout", "",
		"how long a room stays open with no player sending anything, or off; empty uses the default")
	fs.IntVar(&f.maxRooms, "max-rooms", 0, "most rooms at once, counting finished ones until they close; 0 uses the default")
	fs.StringVar(&f.databaseURL, "database-url", "", "PostgreSQL to save games in; without it, a restart loses them")
	fs.DurationVar(&f.snapshotInterval, "snapshot-interval", 0,
		"least time between two saves of a game; 0 saves after every move; a crash can lose up to this much")
	fs.StringVar(&f.timers, "timers-after-restart", "pause",
		"what happens to turn timers when the server comes back: pause, keep or cancel")
	fs.DurationVar(&f.shutdownTimeout, "shutdown-timeout", defaultShutdownTimeout,
		"how long to wait for games to be saved before exiting; keep it below what your service manager allows")
	fs.StringVar(&f.debugAddr, "debug-addr", "", "address for Go's profiling endpoints; keep it private; empty turns them off")
	fs.DurationVar(&f.options.TurnTime, "turn-time", 0, "how long a player has for each turn; 0 uses the default")
	fs.DurationVar(&f.options.FinishedRoomTime, "finished-room-time", 0,
		"how long a room stays open after its game ends; 0 uses the default")
	fs.IntVar(&f.options.TurnsSnapshotVersion, "turns-snapshot-version", 1, "saved game format, 1 or 2, to try a deploy that changes it")
	return f
}

// config checks the flags and turns them into the server's and the listener's config.
func (f *flags) config() (*playhold.ServerConfig, *playhold.ListenConfig, error) {
	policy, err := parseTimers(f.timers)
	if err != nil {
		return nil, nil, err
	}
	trusted, err := parseProxies(f.proxies)
	if err != nil {
		return nil, nil, err
	}
	var limits [3]playhold.RateLimit
	for i, value := range []string{f.connectRate, f.guestRate, f.messageRate} {
		if limits[i], err = parseRate(value); err != nil {
			return nil, nil, err
		}
	}
	var timeouts [3]playhold.Timeout
	for i, value := range []string{f.emptyRoom, f.grace, f.inactiveRoom} {
		if timeouts[i], err = parseTimeout(value); err != nil {
			return nil, nil, err
		}
	}
	if f.debugAddr != "" {
		if err := checkDebugAddr(f.debugAddr, f.dev); err != nil {
			return nil, nil, err
		}
	}
	cfg := &playhold.ServerConfig{
		SnapshotInterval:    f.snapshotInterval,
		TimersAfterRestart:  policy,
		MaxConnsPerIP:       f.maxPerIP,
		ConnectRate:         limits[0],
		GuestRate:           limits[1],
		MessageRate:         limits[2],
		MaxRooms:            f.maxRooms,
		EmptyRoomTimeout:    timeouts[0],
		DisconnectGrace:     timeouts[1],
		InactiveRoomTimeout: timeouts[2],
		OriginPatterns:      splitList(f.origins),
		TrustedProxies:      trusted,
		Development:         f.dev,
	}
	listen := &playhold.ListenConfig{
		Addr:                 f.addr,
		AutocertDomains:      splitList(f.tlsDomains),
		AutocertCacheDir:     f.tlsCache,
		AutocertEmail:        f.tlsEmail,
		AutocertDirectoryURL: f.tlsDirectory,
		CertFile:             f.tlsCert,
		KeyFile:              f.tlsKey,
		ShutdownTimeout:      f.shutdownTimeout,
	}
	if f.web != "" {
		listen.Handler = http.FileServer(http.Dir(f.web))
	}
	return cfg, listen, nil
}

func parseTimers(value string) (playhold.TimersAfterRestart, error) {
	switch value {
	case "pause":
		return playhold.TimersPause, nil
	case "keep":
		return playhold.TimersKeepRunning, nil
	case "cancel":
		return playhold.TimersCancel, nil
	default:
		return 0, fmt.Errorf("unknown -timers-after-restart %q: use pause, keep or cancel", value)
	}
}

func parseProxies(value string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range splitList(value) {
		p, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("-trusted-proxies: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}

// parseRate reads a rate limit flag: empty for the default, "off", or EVERY/BURST such as 200ms/20.
func parseRate(value string) (playhold.RateLimit, error) {
	switch value {
	case "":
		return playhold.RateLimit{}, nil
	case "off":
		return playhold.RateLimit{Off: true}, nil
	}
	everyText, burstText, found := strings.Cut(value, "/")
	every, err := time.ParseDuration(everyText)
	if !found || err != nil || every <= 0 {
		return playhold.RateLimit{}, fmt.Errorf("rate limit %q: use EVERY/BURST, such as 200ms/20, or off", value)
	}
	burst, err := strconv.Atoi(burstText)
	if err != nil || burst <= 0 {
		return playhold.RateLimit{}, fmt.Errorf("rate limit %q: the burst must be a positive number", value)
	}
	return playhold.RateLimit{Every: every, Burst: burst}, nil
}

// parseTimeout reads a timeout flag: empty for the default, "off", or a duration such as 5m.
func parseTimeout(value string) (playhold.Timeout, error) {
	switch value {
	case "":
		return playhold.Timeout{}, nil
	case "off":
		return playhold.Timeout{Off: true}, nil
	}
	after, err := time.ParseDuration(value)
	if err != nil || after <= 0 {
		return playhold.Timeout{}, fmt.Errorf("timeout %q: use a duration such as 5m, or off", value)
	}
	return playhold.Timeout{After: after}, nil
}

// checkDebugAddr keeps the profiling endpoints, which show the server's internals, off public addresses outside
// development.
func checkDebugAddr(addr string, dev bool) error {
	if dev {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-debug-addr: %w", err)
	}
	if ip, err := netip.ParseAddr(host); host == "localhost" || (err == nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("-debug-addr %q isn't on localhost; the profiling endpoints must stay private outside -dev", addr)
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func run(databaseURL string, options cards.Options, cfg *playhold.ServerConfig, listen *playhold.ListenConfig) error {
	typ, err := cards.NewRoomTypeWith(options)
	if err != nil {
		return err
	}
	rt := playhold.NewRuntime(playhold.Config{})
	cfg.RoomType = typ
	if databaseURL != "" {
		records, err := postgres.Open(context.Background(), databaseURL)
		if err != nil {
			return err
		}
		defer records.Close() // after ListenAndServe has written everything
		cfg.Store = records
	}
	server, err := playhold.NewServer(rt, cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // from here, a second signal kills the process, as a way out if the shutdown hangs
	}()
	err = server.ListenAndServe(ctx, listen)
	if ctx.Err() == nil {
		server.Close() // it never served, for example because the port is taken, so no player has anything to save
	}
	return err
}
