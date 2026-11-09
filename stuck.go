package playhold

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ErrRestartNeeded means the server shut down so its process can be restarted: a stuck room's goroutine kept running
// while the server stayed overloaded, and only a new process gets rid of it. Every room was saved, and comes back when
// the server starts again.
var ErrRestartNeeded = errors.New("playhold: restart needed to get rid of a stuck room's goroutine")

const (
	stuckGrace         = 30 * time.Second // how long a stuck room's goroutine has to end on its own
	stuckOverload      = time.Minute      // how long the server must stay overloaded while one runs
	stuckCheckEvery    = 10 * time.Second
	drainBeforeRestart = 10 * time.Second // so a load balancer sees /readyz fail and sends players elsewhere
)

// stuckWatch follows the goroutines of rooms the watchdog gave up on. Go can't stop them: most end by themselves, and one
// that's only blocked costs little. One that keeps running while the server stays overloaded is most likely spinning,
// taking CPU from every room, and only a restart of the process removes it.
type stuckWatch struct {
	logger    *slog.Logger
	admission *admission // nil if off, and then nothing is judged overloaded
	restart   bool       // ServerConfig.RestartWhenStuck
	drain     func()

	running atomic.Int32 // stuck goroutines still running after stuckGrace

	once          sync.Once
	restartNeeded chan struct{}
}

func newStuckWatch(logger *slog.Logger, admit *admission, restart bool, drain func()) *stuckWatch {
	if restart && admit == nil {
		logger.Warn("RestartWhenStuck needs admission control to tell when a stuck room hurts the server; it never restarts")
	}
	return &stuckWatch{logger: logger, admission: admit, restart: restart, drain: drain, restartNeeded: make(chan struct{})}
}

// follow watches one stuck room's goroutine until it ends. Still running after stuckGrace, it's an incident.
func (w *stuckWatch) follow(ctx context.Context, room *Room) {
	select {
	case <-room.exited:
		room.logger.Info("stuck room's goroutine finished after all")
		return
	case <-time.After(stuckGrace):
	case <-ctx.Done():
		return
	}
	room.logger.Warn("stuck room's goroutine is still running; it uses memory, and CPU if it's spinning, until it ends or "+
		"the server restarts", "after", stuckGrace)
	w.running.Add(1)
	room.stillStuck()
	select {
	case <-room.exited:
		w.running.Add(-1)
		room.logger.Info("stuck room's goroutine finished after all")
	case <-ctx.Done():
	}
}

// run checks every few seconds whether a stuck goroutine is hurting the server, until ctx ends.
func (w *stuckWatch) run(ctx context.Context) {
	ticker := time.NewTicker(stuckCheckEvery)
	defer ticker.Stop()
	var lastWarned time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		if w.running.Load() == 0 || w.admission.overloadedFor(now) < stuckOverload {
			continue
		}
		if w.restart {
			w.restartSoon()
			return
		}
		if now.Sub(lastWarned) >= time.Minute {
			lastWarned = now
			w.logger.Error("a stuck room's goroutine is still running and the server has been overloaded for a minute; "+
				"restart the server, or set RestartWhenStuck with a service manager that restarts it",
				"stuck_goroutines", w.running.Load())
		}
	}
}

// restartSoon drains the server, then asks for a restart once a load balancer has had time to notice.
func (w *stuckWatch) restartSoon() {
	w.once.Do(func() {
		w.logger.Error("a stuck room's goroutine is still running and the server has been overloaded for a minute; "+
			"draining, then restarting", "stuck_goroutines", w.running.Load(), "drain", drainBeforeRestart)
		w.drain()
		time.AfterFunc(drainBeforeRestart, func() { close(w.restartNeeded) })
	})
}
