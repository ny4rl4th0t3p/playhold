// Command server runs the sample game. Clients connect at /ws, and with -web it also serves the built browser page.
package main

import (
	"context"
	"errors"
	"flag"
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
	flag.Parse()

	if err := run(*addr, *web, *origins, *maxPerIP, *emptyRoom); err != nil {
		log.Fatal(err)
	}
}

func run(addr, web, origins string, maxPerIP int, emptyRoom time.Duration) error {
	typ, err := cards.NewRoomType()
	if err != nil {
		return err
	}
	rt := playhold.NewRuntime(playhold.Config{})
	cfg := &playhold.ServerConfig{RoomType: typ, MaxConnsPerIP: maxPerIP, EmptyRoomTimeout: emptyRoom}
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
