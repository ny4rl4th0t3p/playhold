package playhold

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ny4rl4th0t3p/playhold/store"
)

// An incident is a turn that hurt the whole server: it was running when the process died, or its room's goroutine was
// still running long after the watchdog gave up on it. A panic isn't one, since only its room is affected, and the room
// goes back to its last save. Incidents are saved in the store, so they're still counted after the restart a crash causes.
const (
	breakerIncidents = 3 // of one player message within incidentWindow turn that message off for turnedOffFor
	roomIncidents    = 2 // of one room within incidentWindow before a start keep it from being restored
	incidentWindow   = 10 * time.Minute
	turnedOffFor     = time.Hour          // after the last incident, then the message is tried again
	incidentHistory  = 7 * 24 * time.Hour // older incidents are deleted
	incidentTimeout  = 5 * time.Second    // to save one
	buildIDBytes     = 16
)

// ErrTurnedOff means the message kept crashing the server or hanging rooms, so the server refuses it for a while.
var ErrTurnedOff = errors.New("playhold: this message is turned off for a while because it kept crashing the server")

// breaker counts incidents and turns off a player message that keeps causing them, for an hour after the last one. A new
// build starts with no incidents, so it turns every message back on.
type breaker struct {
	records store.Store
	build   string
	logger  *slog.Logger
	now     func() time.Time
	saving  sync.WaitGroup
	metrics *metrics // nil counts nothing

	mu        sync.Mutex
	incidents []store.Incident // this build's, oldest first
	// off maps the messages turned off to when they're tried again. It's replaced, never changed, so reading it needs no
	// lock.
	off atomic.Pointer[map[handlerKey]time.Time]
}

// newBreaker deletes the incidents of other builds and the old ones, loads the rest, and adds the turns the crash journal
// shows were running when the process last died.
func newBreaker(ctx context.Context, records store.Store, build string, logger *slog.Logger, crashed []JournalEntry) (*breaker, error) {
	b := &breaker{records: records, build: build, logger: logger, now: time.Now}
	now := b.now()
	if err := records.DeleteIncidents(ctx, build, now.Add(-incidentHistory)); err != nil {
		return nil, fmt.Errorf("playhold: deleting old incidents: %w", err)
	}
	incidents, err := records.LoadIncidents(ctx)
	if err != nil {
		return nil, fmt.Errorf("playhold: loading incidents: %w", err)
	}
	for i := range crashed {
		e := &crashed[i]
		incident := store.Incident{
			Build: build, Kind: string(JournalCrashed), Room: string(e.Room), Player: string(e.Player),
			Module: e.Module, MessageID: e.MessageID, Event: e.Event, At: now,
		}
		if err := records.SaveIncident(ctx, &incident); err != nil {
			return nil, fmt.Errorf("playhold: saving an incident: %w", err)
		}
		incidents = append(incidents, incident)
	}
	b.incidents = incidents

	off := make(map[handlerKey]time.Time)
	for i := range b.incidents {
		key := handlerKey{module: b.incidents[i].Module, id: b.incidents[i].MessageID}
		if _, done := off[key]; done || key.module == "" {
			continue
		}
		if until := b.offUntil(key); until.After(now) {
			off[key] = until
			b.reportOff(key, until)
		}
	}
	b.off.Store(&off)
	return b, nil
}

// offUntil returns when this message is tried again: turnedOffFor after the last of breakerIncidents incidents within
// incidentWindow of each other, or the zero time if it never had that many. The caller holds mu, or nothing else uses b
// yet.
func (b *breaker) offUntil(key handlerKey) time.Time {
	var times []time.Time
	for i := range b.incidents {
		if b.incidents[i].Module == key.module && b.incidents[i].MessageID == key.id {
			times = append(times, b.incidents[i].At)
		}
	}
	slices.SortFunc(times, time.Time.Compare)
	for i := len(times) - 1; i >= breakerIncidents-1; i-- {
		if times[i].Sub(times[i-breakerIncidents+1]) <= incidentWindow {
			return times[i].Add(turnedOffFor)
		}
	}
	return time.Time{}
}

func (b *breaker) reportOff(key handlerKey, until time.Time) {
	b.logger.Error("message turned off: it kept crashing the server or hanging rooms; it's tried again at the time given, "+
		"or with a new build", "module", key.module, "message_id", key.id, "incidents", breakerIncidents,
		"window", incidentWindow, "until", until)
}

// turnedOff reports whether the breaker refuses this message now. It's called for every message, so it takes no lock.
func (b *breaker) turnedOff(key handlerKey) bool {
	if b == nil {
		return false
	}
	off := *b.off.Load()
	if len(off) == 0 {
		return false
	}
	until, ok := off[key]
	return ok && b.now().Before(until)
}

// keepsCrashing reports whether the room had enough incidents in the last incidentWindow that restoring it would likely
// crash the server again.
func (b *breaker) keepsCrashing(room RoomID) bool {
	if b == nil {
		return false
	}
	since := b.now().Add(-incidentWindow)
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for i := range b.incidents {
		if b.incidents[i].Room == string(room) && !b.incidents[i].At.Before(since) {
			n++
		}
	}
	return n >= roomIncidents
}

// record counts an incident, turns its message off if that's one too many, and saves it in the background, since it
// comes from the watchdog's side of the server.
func (b *breaker) record(incident *store.Incident) {
	incident.Build, incident.At = b.build, b.now()
	b.metrics.incident(JournalState(incident.Kind))
	key := handlerKey{module: incident.Module, id: incident.MessageID}
	b.mu.Lock()
	b.incidents = slices.DeleteFunc(b.incidents, func(old store.Incident) bool {
		return old.At.Before(incident.At.Add(-incidentHistory))
	})
	b.incidents = append(b.incidents, *incident)
	var until time.Time
	if key.module != "" {
		until = b.offUntil(key)
	}
	turnsOff := until.After(incident.At)
	if turnsOff {
		off := make(map[handlerKey]time.Time, len(*b.off.Load())+1)
		for k, v := range *b.off.Load() {
			if v.After(incident.At) {
				off[k] = v
			}
		}
		off[key] = until
		b.off.Store(&off)
	}
	b.mu.Unlock()
	if turnsOff {
		b.reportOff(key, until)
	}

	saved := *incident
	b.saving.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), incidentTimeout)
		defer cancel()
		if err := b.records.SaveIncident(ctx, &saved); err != nil {
			b.logger.Error("saving an incident failed; it won't count after a restart", "error", err)
		}
	})
}

// turnedOffNow returns how many messages the breaker refuses now.
func (b *breaker) turnedOffNow() int {
	if b == nil {
		return 0
	}
	now, n := b.now(), 0
	for _, until := range *b.off.Load() {
		if now.Before(until) {
			n++
		}
	}
	return n
}

// wait waits for the incidents being saved.
func (b *breaker) wait() {
	if b != nil {
		b.saving.Wait()
	}
}

// currentBuild identifies the running build by a hash of its executable, read once.
var currentBuild = sync.OnceValues(func() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)[:buildIDBytes]), nil
})
