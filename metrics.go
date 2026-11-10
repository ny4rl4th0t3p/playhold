package playhold

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	metricsNamespace = "playhold"
	unknownLabel     = "unknown"
	decimal          = 10

	// Histogram buckets double each time: turns from 50 µs to about 1.6 s, messages from 100 µs to about 3.3 s, and
	// saves from 1 ms to about 8 s.
	bucketFactor        = 2
	turnBucketStart     = 50e-6
	messageBucketStart  = 100e-6
	snapshotBucketStart = 1e-3
	timeBuckets         = 16
	snapshotBuckets     = 14
)

// messageResult is how a player's message ended, as playhold_messages_total counts it.
type messageResult int

const (
	resultOK messageResult = iota
	resultRejected
	resultUnknownMessage
	resultBadMessage
	resultQueueFull
	resultRoomClosed
	resultTurnedOff
	resultFailed
	resultCount
)

var resultNames = [resultCount]string{
	"ok", "rejected", "unknown_message", "bad_message", "queue_full", "room_closed", "turned_off", "failed",
}

func resultOf(err error) messageResult {
	switch {
	case err == nil:
		return resultOK
	case errors.Is(err, ErrUnknownMessage):
		return resultUnknownMessage
	case errors.Is(err, ErrBadMessage):
		return resultBadMessage
	case errors.Is(err, ErrQueueFull):
		return resultQueueFull
	case errors.Is(err, ErrRoomClosed):
		return resultRoomClosed
	case errors.Is(err, ErrTurnedOff):
		return resultTurnedOff
	case errors.Is(err, ErrMessageFailed):
		return resultFailed
	default:
		return resultRejected // the handler refused it
	}
}

// turnKind is what started a turn, as playhold_turn_duration_seconds labels it.
type turnKind int

const (
	turnMessage turnKind = iota // a player's message
	turnTimer                   // a timer or a grace period
	turnOther                   // a player joining, coming back or leaving
	turnKinds
)

var turnKindNames = [turnKinds]string{"message", "timer", "other"}

func kindOf(in *input) turnKind {
	switch {
	case in.module != "":
		return turnMessage
	case in.timerEvent != "":
		return turnTimer
	default:
		return turnOther
	}
}

// metrics is a Server's Prometheus metrics, in a registry of its own. Its methods do nothing on nil, which is a room or
// runtime used without a Server. The counters a hot path uses are looked up once, here, so counting takes no label
// lookup.
type metrics struct {
	registry *prometheus.Registry

	messages        map[handlerKey]*[resultCount]prometheus.Counter
	unknownMessages [resultCount]prometheus.Counter
	messageLatency  prometheus.Histogram
	turns           [turnKinds]prometheus.Observer
	viewBytes       []prometheus.Counter // by the room type's view index
	closes          map[CloseReason]prometheus.Counter
	closesUnknown   prometheus.Counter

	snapshotDelay    prometheus.Histogram
	snapshotFailures prometheus.Counter
	restores         *prometheus.CounterVec
	rateLimited      *prometheus.CounterVec
	admissionRefused *prometheus.CounterVec
	slowTurns        prometheus.Counter
	incidents        *prometheus.CounterVec
	matches          prometheus.Counter
	guestsDeleted    prometheus.Counter
}

func newMetrics(typ *RoomType) *metrics {
	m := &metrics{registry: prometheus.NewRegistry()}
	m.registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	messages := m.counterVec("messages_total", "Players' messages, by module, message ID and how they ended.",
		"module", "message", "result")
	m.messages = make(map[handlerKey]*[resultCount]prometheus.Counter, len(typ.handlers))
	for key := range typ.handlers {
		counters := &[resultCount]prometheus.Counter{}
		for result := range resultCount {
			counters[result] = messages.WithLabelValues(key.module, strconv.FormatUint(uint64(key.id), decimal), resultNames[result])
		}
		m.messages[key] = counters
	}
	for result := range resultCount {
		m.unknownMessages[result] = messages.WithLabelValues(unknownLabel, unknownLabel, resultNames[result])
	}
	m.messageLatency = m.histogram("message_latency_seconds",
		"From a player's message arriving to its answer, waiting in the room's queue included.",
		prometheus.ExponentialBuckets(messageBucketStart, bucketFactor, timeBuckets))
	turns := m.histogramVec("turn_duration_seconds", "How long a room's turn took, by what started it.",
		prometheus.ExponentialBuckets(turnBucketStart, bucketFactor, timeBuckets), "kind")
	for kind := range turnKinds {
		m.turns[kind] = turns.WithLabelValues(turnKindNames[kind])
	}
	viewBytes := m.counterVec("view_bytes_total", "Bytes of player views sent, by module.", "module")
	for i := range typ.views {
		m.viewBytes = append(m.viewBytes, viewBytes.WithLabelValues(typ.views[i].module))
	}
	closes := m.counterVec("rooms_closed_total",
		"Rooms that closed, by reason. A room restored after a crash counts here too.", "reason")
	m.closes = make(map[CloseReason]prometheus.Counter)
	for reason := CloseRequested; reason <= CloseMatchIncomplete; reason++ {
		m.closes[reason] = closes.WithLabelValues(strings.ReplaceAll(reason.String(), " ", "_"))
	}
	m.closesUnknown = closes.WithLabelValues(unknownLabel)

	m.snapshotDelay = m.histogram("snapshot_delay_seconds",
		"How long a room's change waited to be saved: how much a crash could have lost.",
		prometheus.ExponentialBuckets(snapshotBucketStart, bucketFactor, snapshotBuckets))
	m.snapshotFailures = m.counter("snapshot_write_failures_total", "Batches of room saves that failed and were retried.")
	m.restores = m.counterVec("room_restores_total",
		"Rooms replaced by a copy from their last save, by what happened to them.", "reason")
	m.rateLimited = m.counterVec("rate_limited_total", "Requests refused by a rate limit, by layer.", "layer")
	m.admissionRefused = m.counterVec("admission_refused_total",
		"New guests, rooms and seats refused while the server was overloaded.", "what")
	m.slowTurns = m.counter("slow_turns_total", "Turns that took longer than the time budget, Config.SlowTurn.")
	m.incidents = m.counterVec("incidents_total",
		"Turns that hurt the whole server, by kind: crashed with the process, or stuck and still running.", "kind")
	m.matches = m.counter("matches_total", "Groups the matchmaker formed, each given a new room.")
	m.guestsDeleted = m.counter("guests_expired_total", "Guests deleted for not being seen for longer than GuestExpiry.")
	return m
}

func (m *metrics) counter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Namespace: metricsNamespace, Name: name, Help: help})
	m.registry.MustRegister(c)
	return c
}

func (m *metrics) counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: metricsNamespace, Name: name, Help: help}, labels)
	m.registry.MustRegister(c)
	return c
}

func (m *metrics) histogram(name, help string, buckets []float64) prometheus.Histogram {
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: metricsNamespace, Name: name, Help: help, Buckets: buckets})
	m.registry.MustRegister(h)
	return h
}

func (m *metrics) histogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Namespace: metricsNamespace, Name: name, Help: help, Buckets: buckets}, labels)
	m.registry.MustRegister(h)
	return h
}

// gauge reports what read returns each time Prometheus asks.
func (m *metrics) gauge(name, help string, read func() float64) {
	m.registry.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{Namespace: metricsNamespace, Name: name, Help: help}, read))
}

// message counts a player's message once its result is known, and how long that took since it arrived.
func (m *metrics) message(key handlerKey, err error, arrived time.Time) {
	if m == nil {
		return
	}
	counters, ok := m.messages[key]
	if !ok {
		counters = &m.unknownMessages
	}
	counters[resultOf(err)].Inc()
	m.messageLatency.Observe(time.Since(arrived).Seconds())
}

func (m *metrics) turn(kind turnKind, took time.Duration) {
	if m != nil {
		m.turns[kind].Observe(took.Seconds())
	}
}

func (m *metrics) sentView(viewIndex, bytes int) {
	if m != nil && viewIndex < len(m.viewBytes) {
		m.viewBytes[viewIndex].Add(float64(bytes))
	}
}

func (m *metrics) closed(reason CloseReason) {
	if m == nil {
		return
	}
	if c, ok := m.closes[reason]; ok {
		c.Inc()
		return
	}
	m.closesUnknown.Inc()
}

func (m *metrics) snapshotWritten(delay time.Duration) {
	if m != nil {
		m.snapshotDelay.Observe(delay.Seconds())
	}
}

func (m *metrics) snapshotFailed() {
	if m != nil {
		m.snapshotFailures.Inc()
	}
}

func (m *metrics) restored(reason CloseReason) {
	if m != nil {
		m.restores.WithLabelValues(reason.String()).Inc()
	}
}

// limited counts a request refused by a rate limit: layer is connect, guest, frame or module.
func (m *metrics) limited(layer string) {
	if m != nil {
		m.rateLimited.WithLabelValues(layer).Inc()
	}
}

// refused counts what admission control refused: guest, room, seat or queue.
func (m *metrics) refused(what string) {
	if m != nil {
		m.admissionRefused.WithLabelValues(what).Inc()
	}
}

func (m *metrics) slowTurn() {
	if m != nil {
		m.slowTurns.Inc()
	}
}

func (m *metrics) guestsExpired(n int) {
	if m != nil {
		m.guestsDeleted.Add(float64(n))
	}
}

func (m *metrics) matched() {
	if m != nil {
		m.matches.Inc()
	}
}

func (m *metrics) incident(kind JournalState) {
	if m != nil {
		m.incidents.WithLabelValues(string(kind)).Inc()
	}
}

// MetricsHandler serves the server's metrics for Prometheus to scrape. ListenAndServe already serves it on a listener of
// its own; a server that doesn't use ListenAndServe can serve this one, on an address only its monitoring can reach.
func (s *Server) MetricsHandler() http.Handler {
	return promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{})
}
