package main

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"runtime"
)

const (
	// blockProfileRate samples about one blocking event per this many nanoseconds spent blocked.
	blockProfileRate = 10_000
	// mutexProfileFraction samples about one in this many contended locks.
	mutexProfileFraction = 100
)

// serveDebug serves Go's profiling endpoints on their own address, to measure the server under load. They show the
// server's internals, so they only listen when asked to, and the address must stay private. Blocking and lock
// contention are only recorded while they're on, because recording them has a cost of its own. Importing net/http/pprof
// also registers its handlers on http.DefaultServeMux, which nothing in this server serves.
func serveDebug(addr string) {
	runtime.SetBlockProfileRate(blockProfileRate)
	runtime.SetMutexProfileFraction(mutexProfileFraction)

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index) // also serves the named profiles, such as block and mutex
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}

	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("debug endpoints stopped", "addr", addr, "error", err)
		}
	}()
	slog.Info("debug endpoints listening", "addr", addr)
}
