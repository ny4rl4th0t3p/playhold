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
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	web := flag.String("web", "", "folder with the built browser page, to serve it from this server")
	origins := flag.String("origins", "", "comma-separated web origins allowed to connect, besides this server's own")
	maxPerIP := flag.Int("max-conns-per-ip", 0, "most connections from one IP address; 0 uses the default")
	emptyRoom := flag.Duration("empty-room-timeout", 0, "how long a room stays open with nobody connected; 0 uses the default")
	maxRooms := flag.Int("max-rooms", 0, "most rooms at once, counting finished ones until they close; 0 uses the default")
	databaseURL := flag.String("database-url", "", "PostgreSQL to save games in; without it, a restart loses them")
	timers := flag.String("timers-after-restart", "pause", "what happens to turn timers when the server comes back: pause, keep or cancel")
	var options cards.Options
	flag.DurationVar(&options.TurnTime, "turn-time", 0, "how long a player has for each turn; 0 uses the default")
	flag.DurationVar(&options.FinishedRoomTime, "finished-room-time", 0, "how long a room stays open after its game ends; 0 uses the default")
	flag.Parse()

	policy, err := parseTimers(*timers)
	if err != nil {
		log.Fatal(err)
	}
	cfg := &playhold.ServerConfig{
		TimersAfterRestart: policy,
		MaxConnsPerIP:      *maxPerIP,
		MaxRooms:           *maxRooms,
		EmptyRoomTimeout:   *emptyRoom,
	}
	if err := run(*addr, *web, *origins, *databaseURL, options, cfg); err != nil {
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

func run(addr, web, origins, databaseURL string, options cards.Options, cfg *playhold.ServerConfig) error {
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
	if web != "" {
		mux.Handle("/", http.FileServer(http.Dir(web)))
	}
	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown(httpServer, server, rt)
	}()

	slog.Info("listening", "addr", addr, "memory_limit_bytes", debug.SetMemoryLimit(-1))
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-ctx.Done()
	return nil
}

func shutdown(httpServer *http.Server, server *playhold.Server, rt *playhold.Runtime) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	server.Close()
	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Warn("http server shutdown", "error", err)
	}
	if err := rt.Shutdown(ctx); err != nil {
		slog.Warn("rooms shutdown", "error", err)
	}
}
