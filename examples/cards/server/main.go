// Command server runs the sample game. Clients connect at /ws, and with -web it also serves the built browser page.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os/signal"
	"runtime/debug"
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
	addr := flag.String("addr", ":8080", "address to listen on")
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
	if *debugAddr != "" {
		serveDebug(*debugAddr)
	}
	cfg := &playhold.ServerConfig{
		SnapshotInterval:   *snapshotInterval,
		TimersAfterRestart: policy,
		MaxConnsPerIP:      *maxPerIP,
		MaxRooms:           *maxRooms,
		EmptyRoomTimeout:   *emptyRoom,
		DisconnectGrace:    *grace,
	}
	if err := run(*addr, *web, *origins, *databaseURL, options, cfg, *shutdownTimeout); err != nil {
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

func run(addr, web, origins, databaseURL string, options cards.Options, cfg *playhold.ServerConfig, shutdownTimeout time.Duration) error {
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
		defer records.Close()
		cfg.Store = records
	}
	if origins != "" {
		cfg.OriginPatterns = strings.Split(origins, ",")
	}
	server, err := playhold.NewServer(rt, cfg)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	mux.HandleFunc("/healthz", server.Live)
	mux.HandleFunc("/readyz", server.Ready)
	if web != "" {
		mux.Handle("/", http.FileServer(http.Dir(web)))
	}
	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serving := make(chan error, 1)
	go func() { serving <- httpServer.ListenAndServe() }()
	slog.Info("listening", "addr", addr, "memory_limit_bytes", debug.SetMemoryLimit(-1))

	select {
	case err := <-serving:
		return err // it never started, for example because the port is taken, so no player has anything to save
	case <-ctx.Done():
	}

	stop() // from here, a second signal kills the process, as a way out if the shutdown hangs
	shutdown(httpServer, server, shutdownTimeout)
	// The store closes after this, so everything has to be written by now.
	if err := <-serving; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// shutdown saves the games first, while the HTTP server is still answering health checks.
func shutdown(httpServer *http.Server, server *playhold.Server, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Warn("game server shutdown", "error", err)
	}
	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Warn("http server shutdown", "error", err)
	}
}
