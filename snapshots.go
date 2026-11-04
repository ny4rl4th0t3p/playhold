package playhold

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ny4rl4th0t3p/playhold/store"
)

const (
	snapshotWriteTimeout = 10 * time.Second
	snapshotRetryDelay   = time.Second
)

// pendingRoom is the newest thing waiting to be written for a room.
type pendingRoom struct {
	record store.RoomRecord
	forget bool
}

// snapshotWriter saves room snapshots in the background, so a room never waits on the database. It keeps only the
// newest snapshot per room, so rapid changes combine, and memory stays bounded however slow the store gets. If a write
// fails, the writer keeps retrying: rooms carry on, and a crash in that window loses more.
type snapshotWriter struct {
	records store.Store
	logger  *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingRoom

	wake      chan struct{} // says pending may have something in it
	stop      chan struct{} // asks the writer to finish what's pending, then end
	stopOnce  sync.Once
	done      chan struct{}
	ctx       context.Context // ends the writer's work, even if something is still pending
	cancelCtx context.CancelFunc
}

func newSnapshotWriter(records store.Store, logger *slog.Logger) *snapshotWriter {
	ctx, cancel := context.WithCancel(context.Background())
	w := &snapshotWriter{
		records:   records,
		logger:    logger,
		pending:   make(map[string]pendingRoom),
		wake:      make(chan struct{}, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		ctx:       ctx,
		cancelCtx: cancel,
	}
	go w.run()
	return w
}

// save queues a room's snapshot, replacing any not yet written. It never blocks on the store.
func (w *snapshotWriter) save(record store.RoomRecord) {
	w.queue(record.ID, &pendingRoom{record: record})
}

// forget queues deleting a room's snapshot, replacing anything not yet written for it. It never blocks on the store.
func (w *snapshotWriter) forget(id string) {
	w.queue(id, &pendingRoom{forget: true})
}

func (w *snapshotWriter) queue(id string, room *pendingRoom) {
	w.mu.Lock()
	w.pending[id] = *room
	w.mu.Unlock()

	select {
	case w.wake <- struct{}{}:
	default: // already signaled
	}
}

// close writes what's pending and ends the writer, giving up when ctx does.
func (w *snapshotWriter) close(ctx context.Context) {
	w.stopOnce.Do(func() { close(w.stop) })
	select {
	case <-w.done:
	case <-ctx.Done():
		w.cancelCtx()
		<-w.done
	}
	w.cancelCtx()
}

func (w *snapshotWriter) run() {
	defer close(w.done)
	for {
		select {
		case <-w.wake:
			w.writePending()
		case <-w.stop:
			w.writePending()
			return
		}
	}
}

// writePending writes until nothing is pending, retrying failures, or until the writer's context ends.
func (w *snapshotWriter) writePending() {
	for {
		batch := w.takePending()
		if len(batch) == 0 {
			return
		}
		err := w.write(batch)
		if err == nil {
			continue
		}
		w.putBack(batch)
		if w.ctx.Err() != nil {
			w.logger.Error("giving up on room snapshots", "rooms", len(batch), "error", err)
			return
		}
		w.logger.Error("writing room snapshots failed, retrying", "rooms", len(batch), "error", err)
		select {
		case <-time.After(snapshotRetryDelay):
		case <-w.ctx.Done():
		}
	}
}

func (w *snapshotWriter) takePending() map[string]pendingRoom {
	w.mu.Lock()
	defer w.mu.Unlock()
	batch := w.pending
	w.pending = make(map[string]pendingRoom)
	return batch
}

// putBack returns a failed batch, except for rooms that have something newer by now.
func (w *snapshotWriter) putBack(batch map[string]pendingRoom) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for id, room := range batch {
		if _, newer := w.pending[id]; !newer {
			w.pending[id] = room
		}
	}
}

func (w *snapshotWriter) write(batch map[string]pendingRoom) error {
	saves := make([]store.RoomRecord, 0, len(batch))
	var deletes []string
	for id, room := range batch {
		if room.forget {
			deletes = append(deletes, id)
		} else {
			saves = append(saves, room.record)
		}
	}
	ctx, cancel := context.WithTimeout(w.ctx, snapshotWriteTimeout)
	defer cancel()
	return w.records.WriteRooms(ctx, saves, deletes)
}
