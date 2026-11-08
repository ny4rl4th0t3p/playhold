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
	addr := flag.String("addr", "", "address to listen on; empty uses :443 with TLS and :8080 without")
	dev := flag.Bool("dev", false, "development mode: plain HTTP without a proxy, and the safety checks off; never on a server players reach")
	tlsDomains := flag.String("tls-domains", "", "comma-separated domains for automatic certificates from Let's Encrypt")
	tlsCache := flag.String("tls-cache", "", "folder that keeps the automatic certificates across restarts")
	tlsEmail := flag.String("tls-email", "", "email Let's Encrypt writes to about the certificates; optional")
	tlsDirectory := flag.String("tls-acme-directory", "",
		"ACME directory for automatic certificates; empty uses Let's Encrypt's; "+
			"use its staging directory, with its own -tls-cache, to check a first setup")
	tlsCert := flag.String("tls-cert", "", "certificate file (PEM), instead of automatic certificates")
	tlsKey := flag.String("tls-key", "", "the certificate's key file (PEM)")
	proxies := flag.String("trusted-proxies", "", "comma-separated address ranges of the proxies in front of this server, such as 10.0.0.0/8")
	web := flag.String("web", "", "folder with the built browser page, to serve it from this server")
	origins := flag.String("origins", "", "comma-separated web origins allowed to connect, besides this server's own")
	maxPerIP := flag.Int("max-conns-per-ip", 0, "most connections from one IP address; 0 uses the default")
	emptyRoom := flag.Duration("empty-room-timeout", 0, "how long a room stays open with nobody connected; 0 uses the default")
	grace := flag.Duration("disconnect-grace", 0, "how long a player can be disconnected before others are told; 0 uses the default")
	maxRooms := flag.Int("max-rooms", 0, "most rooms at once, counting finished ones until they close; 0 uses the default")
	databaseURL := flag.String("database-url", "", "PostgreSQL to save games in; without it, a restart loses them")
	snapshotInterval := flag.Duration("snapshot-interval", 0,
		"least time between two saves of a game; 0 saves after every move; a crash can lose up to this much")
	timers := flag.String("timers-after-restart", "pause", "what happens to turn timers when the server comes back: pause, keep or cancel")
	shutdownTimeout := flag.Duration("shutdown-timeout", defaultShutdownTimeout,
		"how long to wait for games to be saved before exiting; keep it below what your service manager allows")
	debugAddr := flag.String("debug-addr", "", "address for Go's profiling endpoints; keep it private; empty turns them off")
	var options cards.Options
	flag.DurationVar(&options.TurnTime, "turn-time", 0, "how long a player has for each turn; 0 uses the default")
	flag.DurationVar(&options.FinishedRoomTime, "finished-room-time", 0, "how long a room stays open after its game ends; 0 uses the default")
	flag.IntVar(&options.TurnsSnapshotVersion, "turns-snapshot-version", 1, "saved game format, 1 or 2, to try a deploy that changes it")
	flag.Parse()

	policy, err := parseTimers(*timers)
	if err != nil {
		log.Fatal(err)
	}
	trusted, err := parseProxies(*proxies)
	if err != nil {
		log.Fatal(err)
	}
	if *debugAddr != "" {
		if err := checkDebugAddr(*debugAddr, *dev); err != nil {
			log.Fatal(err)
		}
		serveDebug(*debugAddr)
	}
	cfg := &playhold.ServerConfig{
		SnapshotInterval:   *snapshotInterval,
		TimersAfterRestart: policy,
		MaxConnsPerIP:      *maxPerIP,
		MaxRooms:           *maxRooms,
		EmptyRoomTimeout:   *emptyRoom,
		DisconnectGrace:    *grace,
		OriginPatterns:     splitList(*origins),
		TrustedProxies:     trusted,
		Development:        *dev,
	}
	listen := &playhold.ListenConfig{
		Addr:                 *addr,
		AutocertDomains:      splitList(*tlsDomains),
		AutocertCacheDir:     *tlsCache,
		AutocertEmail:        *tlsEmail,
		AutocertDirectoryURL: *tlsDirectory,
		CertFile:             *tlsCert,
		KeyFile:              *tlsKey,
		ShutdownTimeout:      *shutdownTimeout,
	}
	if *web != "" {
		listen.Handler = http.FileServer(http.Dir(*web))
	}
	if err := run(*databaseURL, options, cfg, listen); err != nil {
		log.Fatal(err)
	}
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
