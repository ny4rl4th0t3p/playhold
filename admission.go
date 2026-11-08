package playhold

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"runtime/metrics"
	"sync/atomic"
	"time"
)

// ErrOverloaded means the server is too busy for new rooms or players. Rooms that already exist carry on, and players
// with a seat can always come back.
var ErrOverloaded = errors.New("playhold: the server is too busy for new games or players")

// Admission says when the server is too busy to take new work: new guests, new rooms and new seats. Each signal
// is checked every second; going over any of them refuses new work, and it's taken again once all of them have stayed
// under 90% of their thresholds for 10 checks in a row. Zero values use the defaults. Off turns admission control off,
// and the server logs a warning at startup.
type Admission struct {
	// Memory is the share of the memory limit (GOMEMLIMIT, or ServerConfig.MemoryLimit) the Go runtime may hold. The
	// default is 0.9. It's not checked when there's no limit.
	Memory float64
	// SchedP99 is how long goroutines may wait to run, at the 99th percentile over the last 5 seconds: the sign that the
	// CPU can't keep up. The default is 20 ms. The Go runtime records waits in steps of about 25%, and the p99 is read as
	// the bottom of its step, so a wait counts as over only once it's certainly over.
	SchedP99 time.Duration
	// Queue is the share of all rooms' inbound queues that may be in use. The default is 0.5.
	Queue float64
	Off   bool
}

const (
	defaultAdmissionMemory = 0.9
	defaultAdmissionSched  = 20 * time.Millisecond
	defaultAdmissionQueue  = 0.5

	admissionEvery    = time.Second
	schedWindow       = 5 // samples, one per admissionEvery
	minSchedSamples   = 100
	schedPercentile   = 0.99 // the share of waits SchedP99 bounds
	admissionRecovery = 0.9  // the share of each threshold a signal must be back under
	admissionCalm     = 10   // checks in a row under it before new work is taken again
)

func (a Admission) orDefault() Admission {
	if a.Off {
		return a
	}
	if a.Memory <= 0 {
		a.Memory = defaultAdmissionMemory
	}
	if a.SchedP99 <= 0 {
		a.SchedP99 = defaultAdmissionSched
	}
	if a.Queue <= 0 {
		a.Queue = defaultAdmissionQueue
	}
	return a
}

// load is one reading of the signals.
type load struct {
	memory       float64 // share of the memory limit in use; zero if there's no limit
	schedP99     time.Duration
	schedSamples uint64 // how many waits the p99 comes from
	queue        float64
}

// processReader reads the process's memory and waiting times; readProcess is replaced in tests.
type processReader interface {
	read() (memory float64, sched schedHistogram)
}

var readProcess processReader = runtimeMetrics{}

// admission decides whether the server takes new work.
type admission struct {
	limits     Admission
	logger     *slog.Logger
	queue      func() float64
	process    processReader
	overloaded atomic.Bool
	window     []schedHistogram // the last schedWindow readings, oldest first
	calm       int              // checks in a row under the recovery level while overloaded
}

// newAdmission returns nil if admission control is off.
func newAdmission(limits Admission, queue func() float64, logger *slog.Logger) *admission {
	if limits.Off {
		logger.Warn("admission control off; an overloaded server keeps taking new rooms and players")
		return nil
	}
	return &admission{limits: limits, logger: logger, queue: queue, process: readProcess}
}

// allow reports whether the server takes new work now. A nil admission always does.
func (a *admission) allow() bool { return a == nil || !a.overloaded.Load() }

// run checks the signals every second until ctx ends. The first check is made before it starts.
func (a *admission) run(ctx context.Context) {
	ticker := time.NewTicker(admissionEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		a.check(a.sample())
	}
}

func (a *admission) sample() load {
	memory, sched := a.process.read()
	a.window = append(a.window, sched)
	if len(a.window) > schedWindow {
		a.window = a.window[1:]
	}
	p99, samples := a.window[len(a.window)-1].p99Since(a.window[0])
	return load{memory: memory, schedP99: p99, schedSamples: samples, queue: a.queue()}
}

// check updates whether the server is overloaded, and logs each change once. Once overloaded, it takes new work again
// only after admissionCalm checks in a row under the recovery level, so a server at its limit doesn't flip back and
// forth, or take a wave of newcomers the moment it dips.
func (a *admission) check(l load) {
	if !a.overloaded.Load() {
		if signal := a.over(l, 1); signal != "" {
			a.overloaded.Store(true)
			a.calm = 0
			a.logger.Warn("server overloaded, refusing new guests, rooms and seats", "signal", signal,
				"memory", l.memory, "sched_p99", l.schedP99, "queue", l.queue)
		}
		return
	}
	if a.over(l, admissionRecovery) != "" {
		a.calm = 0
		return
	}
	a.calm++
	if a.calm >= admissionCalm {
		a.overloaded.Store(false)
		a.logger.Info("server no longer overloaded", "memory", l.memory, "sched_p99", l.schedP99, "queue", l.queue)
	}
}

// over returns the first signal above its threshold times scale, or "" if none is.
func (a *admission) over(l load, scale float64) string {
	switch {
	case l.memory > a.limits.Memory*scale:
		return "memory"
	case l.schedSamples >= minSchedSamples && float64(l.schedP99) > float64(a.limits.SchedP99)*scale:
		return "sched_p99"
	case l.queue > a.limits.Queue*scale:
		return "queue"
	}
	return ""
}

// schedHistogram is a reading of /sched/latencies:seconds. Its counts only grow, so two readings give the waits in
// between.
type schedHistogram struct {
	counts  []uint64
	buckets []float64 // seconds; len(counts)+1 boundaries
}

// p99Since returns the 99th percentile of the waits since an earlier reading, and how many waits there were. It's the
// bottom of the bucket the percentile falls in: the runtime's buckets are about 25% wide, and the bottom is what the
// wait certainly reached.
func (h schedHistogram) p99Since(earlier schedHistogram) (p99 time.Duration, samples uint64) {
	if len(earlier.counts) != len(h.counts) {
		return 0, 0
	}
	var total uint64
	for i := range h.counts {
		total += h.counts[i] - earlier.counts[i]
	}
	if total == 0 {
		return 0, 0
	}
	want := uint64(math.Ceil(schedPercentile * float64(total)))
	var seen uint64
	for i := range h.counts {
		seen += h.counts[i] - earlier.counts[i]
		if seen >= want {
			lower := h.buckets[i]
			if math.IsInf(lower, -1) {
				return 0, total // the runtime's underflow bucket, for waits it measured as negative
			}
			return time.Duration(lower * float64(time.Second)), total
		}
	}
	return 0, total
}

// runtimeMetrics reads the Go runtime's own measurements, which work on every platform.
type runtimeMetrics struct{}

func (runtimeMetrics) read() (memory float64, sched schedHistogram) {
	samples := []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/gc/gomemlimit:bytes"},
		{Name: "/sched/latencies:seconds"},
	}
	metrics.Read(samples)
	if limit := samples[2].Value.Uint64(); limit > 0 && limit < math.MaxInt64 {
		held := samples[0].Value.Uint64() - samples[1].Value.Uint64()
		memory = float64(held) / float64(limit)
	}
	hist := samples[3].Value.Float64Histogram()
	// Copied, because the runtime may reuse the histogram's memory on the next read.
	sched = schedHistogram{counts: append([]uint64(nil), hist.Counts...), buckets: append([]float64(nil), hist.Buckets...)}
	return memory, sched
}
