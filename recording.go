package playhold

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

const (
	defaultRecordingRetention = 30 * 24 * time.Hour
	defaultRecordingMaxSize   = 10 << 30
	recordingCleanupEvery     = time.Hour
	recordingFlushEvery       = 100 * time.Millisecond
	// maxRecordingWaiting is how many bytes of records wait to be written, across all rooms, before a recording whose
	// record doesn't fit is abandoned.
	maxRecordingWaiting = 8 << 20
	recordingFileMode   = 0o600 // only the server's user: it holds players' messages
	recordingDirMode    = 0o700
	recordHeaderSize    = 8 // the record's length, then its CRC-32
	stateHashBytes      = 8
)

// The names of a recording's files, after the room's ID. A recording in progress is written to, or waits for its room to
// be restored; a finished one is never written to again, so a tool can move or delete it.
const (
	recordingExt        = ".rec"         // in progress
	recordingDoneExt    = ".done.rec"    // finished, its room closed normally
	recordingProblemExt = ".problem.rec" // finished, after its room panicked, got stuck or the process died
	recordingFlagExt    = ".flag"        // next to one in progress, after its room panicked or got stuck
)

// RecordingConfig says where the server keeps recordings, and for how long. A room type turns recording on for its rooms
// with RoomConfig.Record; this is the server's side of it. Each room type's recordings go in a folder of their own, named
// after the room type, one file per room, named by its ID. A recording holds every player's messages, so keep it as
// private as the database.
//
// A room's file is <room ID>.rec while it runs, or while it waits to be restored after a restart. Once the room is
// finished for good, it's renamed <room ID>.done.rec, or <room ID>.problem.rec if the room panicked, got stuck, or the
// process died while it ran. The server never writes to a finished file again, so a process of the game's own can move
// finished files elsewhere, to keep a history of every game.
//
// Every recording on disk replays from its first record to its last. When some of a room's records can't be written,
// because they come faster than the disk takes them, the recordings are over MaxSize, or a write failed, the room's
// recording is abandoned instead: its file is deleted, and nothing more is recorded for it. The server logs it.
type RecordingConfig struct {
	// Dir is where recordings go. Empty turns recording off, and NewServer then refuses a room type that records.
	Dir string
	// Retention deletes a finished recording this long after it was last written to, checked every hour. The default is
	// 30 days. Off keeps finished recordings until MaxSize deletes them, and the server logs a warning.
	Retention Timeout
	// MaxSize is the most bytes the recordings in Dir may take, finished and in progress, across all room types. Past it,
	// the oldest finished recordings are deleted, down to 90% of it, those of rooms that panicked, got stuck or crashed
	// last. Recordings in progress are never deleted for this: if they alone are over it, they're abandoned, and no room
	// is recorded until the recordings fit again. The default is 10 GiB. A negative value means no limit, and the server
	// logs a warning.
	MaxSize int64
}

// validRecordingName reports whether a room type's name can be the folder of its recordings: letters, digits, - and _.
func validRecordingName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		ok := c == '-' || c == '_' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

// buildRevision is the version control revision Go stamped into the build, with -modified for uncommitted changes, or
// empty if it has none.
var buildRevision = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision != "" && modified {
		revision += "-modified"
	}
	return revision
})

// recordingWriter appends rooms' records to their files in the background, so a room never waits on the disk. Every
// recordingFlushEvery, each file with records waiting is opened, appended to in one write, and closed again; then the
// files of rooms finished for good are renamed.
//
// A recording on disk always replays whole, so a room's records are never dropped one by one: when one can't be written,
// the room's recording is abandoned. What's waiting for it is dropped, its file is deleted, and its later records are
// ignored, until its room finishes or starts a new segment, which begins a whole recording again.
type recordingWriter struct {
	dir      string
	build    string
	revision string
	logger   *slog.Logger
	limit    int       // maxRecordingWaiting; tests lower it
	maxSize  int64     // the most bytes the recordings may take, negative for no limit; set before any room uses it
	started  time.Time // files last written before this are the last process's

	mu        sync.Mutex
	pending   map[string][]byte // records waiting, by file
	waiting   int
	flags     map[string]bool // files to make a marker next to: their room panicked or got stuck
	finishing map[string]bool // files to rename once what's waiting is written: their room is finished for good
	removing  map[string]bool // files of abandoned recordings, to delete with their markers
	abandoned map[string]bool // recordings given up on, whose records are ignored
	size      int64           // bytes of recordings in dir: the last cleanup's count, plus what was written since
	full      bool            // over maxSize with nothing finished left to delete: no room is recorded
	abandons  int             // recordings abandoned since the last flush
	warned    bool            // abandoning was logged, once until a flush passes with none
	failing   bool            // a write failed, logged once until one works

	cleanup  chan struct{} // asks the cleanup for a pass; holds one
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

func newRecordingWriter(dir, build string, logger *slog.Logger) *recordingWriter {
	w := &recordingWriter{
		dir: dir, build: build, revision: buildRevision(), logger: logger, limit: maxRecordingWaiting, maxSize: -1,
		started: time.Now(),
		pending: make(map[string][]byte), flags: make(map[string]bool), finishing: make(map[string]bool),
		removing: make(map[string]bool), abandoned: make(map[string]bool),
		cleanup: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go w.run()
	return w
}

// path is the file of a room's recording, while it's in progress.
func (w *recordingWriter) path(roomType string, room RoomID) string {
	return filepath.Join(w.dir, roomType, string(room)+recordingExt)
}

// markerPath is the marker next to a recording in progress, saying its room panicked or got stuck.
func markerPath(path string) string {
	return strings.TrimSuffix(path, recordingExt) + recordingFlagExt
}

// finishedPath is what a recording in progress is renamed to once its room is finished for good.
func finishedPath(path string, problem bool) string {
	if problem {
		return strings.TrimSuffix(path, recordingExt) + recordingProblemExt
	}
	return strings.TrimSuffix(path, recordingExt) + recordingDoneExt
}

// add queues a record for the file. It never waits on the disk.
func (w *recordingWriter) add(path string, record *wire.RecordingRecord) {
	data, err := proto.Marshal(record)
	if err != nil {
		w.logger.Error("a recording's record can't be encoded, so the recording is abandoned", "error", err)
		w.mu.Lock()
		w.abandon(path, "a record can't be encoded")
		w.mu.Unlock()
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.abandoned[path] {
		if record.GetSegment() == nil || w.full {
			return
		}
		delete(w.abandoned, path) // a new segment begins a whole recording again; the old file goes first, at the flush
	}
	switch {
	case w.full:
		w.abandon(path, "the recordings are over RecordingConfig.MaxSize, and only rooms still running are left")
		return
	case w.waiting+recordHeaderSize+len(data) > w.limit:
		w.abandon(path, "rooms make records faster than the disk takes them")
		return
	}
	w.pending[path] = appendRecord(w.pending[path], data)
	w.waiting += recordHeaderSize + len(data)
}

// abandon gives up on a recording: what's waiting for it is dropped, its file and marker are deleted at the next flush,
// and its later records are ignored. The caller holds mu.
func (w *recordingWriter) abandon(path, why string) {
	if data, ok := w.pending[path]; ok {
		w.waiting -= len(data)
		delete(w.pending, path)
	}
	delete(w.flags, path)
	w.removing[path] = true
	if w.finishing[path] {
		delete(w.finishing, path) // its room is finished, so no record follows
	} else {
		w.abandoned[path] = true
	}
	w.abandons++
	if !w.warned {
		w.warned = true
		w.logger.Warn("recordings abandoned, and their files deleted, so every recording left replays whole", "why", why)
	}
}

// flag marks a recording as one whose room panicked or got stuck, so it's renamed as a problem once finished, and kept
// longest when space runs out. The marker survives a restart, as the recording does.
func (w *recordingWriter) flag(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.abandoned[path] {
		w.flags[path] = true
	}
}

// finish renames a recording once what's waiting for it is written: its room is finished for good, and won't be restored
// by this process or the next.
func (w *recordingWriter) finish(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.abandoned[path] {
		delete(w.abandoned, path) // its file is gone, and nothing more comes for it
		return
	}
	w.finishing[path] = true
}

// signalCleanup asks the cleanup for a pass, without waiting. The caller holds mu.
func (w *recordingWriter) signalCleanup() {
	select {
	case w.cleanup <- struct{}{}:
	default: // one is already asked for
	}
}

func appendRecord(buf, data []byte) []byte {
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(data))) //nolint:gosec // at most a message and a room's state
	buf = binary.LittleEndian.AppendUint32(buf, crc32.ChecksumIEEE(data))
	return append(buf, data...)
}

func (w *recordingWriter) run() {
	defer close(w.done)
	ticker := time.NewTicker(recordingFlushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.flush()
		case <-w.stop:
			w.flush()
			return
		}
	}
}

// flush deletes the files of abandoned recordings, writes what's waiting, each file in one write, makes the markers, and
// renames the files of rooms finished for good, in that order: a marker never exists without its file, and a file is
// renamed only once its last records are in it. A process that dies in between leaves whole records, so the file can
// still be read up to there. A file whose write failed may end in part of a record, so its recording is abandoned.
func (w *recordingWriter) flush() {
	b := w.take()
	freed := removeRecordings(b.removing)
	out := b.writeOut()
	w.wrote(b, out, freed)
}

// flushBatch is what a flush takes from the writer at once.
type flushBatch struct {
	pending   map[string][]byte
	flags     map[string]bool
	finishing map[string]bool
	removing  map[string]bool
}

// flushOutcome is what writing a batch did.
type flushOutcome struct {
	written int64
	failed  map[string]bool // files whose write failed
	err     error           // the last error
}

// take returns what's waiting, and starts over with nothing.
func (w *recordingWriter) take() flushBatch {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := flushBatch{pending: w.pending, flags: w.flags, finishing: w.finishing, removing: w.removing}
	w.pending, w.waiting = make(map[string][]byte), 0
	w.flags, w.finishing, w.removing = make(map[string]bool), make(map[string]bool), make(map[string]bool)
	if w.abandons == 0 {
		w.warned = false
	}
	w.abandons = 0
	return b
}

// writeOut writes the batch's records, then makes its markers and renames its finished files, skipping the files whose
// write failed. It runs without the writer's lock, so rooms never wait on the disk.
func (b *flushBatch) writeOut() flushOutcome {
	out := flushOutcome{failed: make(map[string]bool)}
	for path, data := range b.pending {
		if err := appendToFile(path, data); err != nil {
			out.err, out.failed[path] = err, true
			continue
		}
		out.written += int64(len(data))
	}
	for path := range b.flags {
		if out.failed[path] {
			continue
		}
		if err := makeMarker(path); err != nil {
			out.err = err
		}
	}
	for path := range b.finishing {
		if out.failed[path] {
			continue
		}
		if err := finishFile(path); err != nil {
			out.err = err
		}
	}
	return out
}

// wrote abandons the recordings whose write failed, counts what was written, asks for a cleanup if that's due, and logs
// a failure once until a flush works.
func (w *recordingWriter) wrote(b flushBatch, out flushOutcome, freed bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for path := range out.failed {
		if b.finishing[path] {
			w.removing[path] = true // its room is finished, so nothing more comes for it
		} else {
			w.abandon(path, "writing it failed")
		}
	}
	before := w.size
	w.size += out.written
	if (w.maxSize >= 0 && before <= w.maxSize && w.size > w.maxSize) || (w.full && freed) {
		w.signalCleanup() // once, as the recordings pass the limit; and to count again after abandoning freed some
	}
	switch {
	case out.err != nil && !w.failing:
		w.failing = true
		w.logger.Error("writing recordings failed; rooms go on, but those recordings are abandoned", "error", out.err)
	case out.err == nil && len(b.pending) > 0:
		w.failing = false
	}
}

// removeRecordings deletes the files of abandoned recordings, and their markers, and reports whether any file was there.
func removeRecordings(paths map[string]bool) bool {
	freed := false
	for path := range paths {
		if os.Remove(path) == nil {
			freed = true
		}
		_ = os.Remove(markerPath(path)) // most have none
	}
	return freed
}

// makeMarker makes the empty marker next to a recording in progress.
func makeMarker(path string) error {
	f, err := os.OpenFile(markerPath(path), os.O_CREATE|os.O_WRONLY, recordingFileMode)
	if err != nil {
		return err
	}
	return f.Close()
}

// finishFile renames a recording in progress whose room is finished for good, as a problem if it has a marker, and then
// deletes the marker. A rename is atomic, so a tool never sees a finished file before its last record is in it.
func finishFile(path string) error {
	marker := markerPath(path)
	_, err := os.Stat(marker)
	problem := err == nil
	if err := os.Rename(path, finishedPath(path, problem)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// appendToFile appends data to the file, creating it, and its folder if that's gone, such as after someone cleaned the
// recordings up while the server ran.
func appendToFile(path string, data []byte) error {
	path = filepath.Clean(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, recordingFileMode)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), recordingDirMode); err != nil {
			return err
		}
		f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, recordingFileMode)
	}
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}

// close writes what's waiting, then ends the writer. Rooms must have stopped first.
func (w *recordingWriter) close() {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

// startCleanup runs in the background until ctx ends. First it finishes the recordings of roomType the last process
// left in progress, unless keep holds their room, one left in the store to be restored. Then, unless Retention and
// MaxSize are both off, it runs a cleanup pass at once, every hour, and whenever the writer asks for one.
func (w *recordingWriter) startCleanup(ctx context.Context, roomType string, keep map[RoomID]bool, retention Timeout) {
	retention = retention.orDefault(defaultRecordingRetention)
	switch {
	case retention.Off && w.maxSize < 0:
		w.logger.Warn("recordings are never deleted; set RecordingConfig.Retention or MaxSize, or delete old ones yourself")
	case retention.Off:
		w.logger.Warn("finished recordings are kept however old, until they're over RecordingConfig.MaxSize")
	case w.maxSize < 0:
		w.logger.Warn("recordings have no size limit, so a busy server can fill its disk before Retention deletes them; " +
			"set RecordingConfig.MaxSize")
	}
	go func() {
		finished, err := finishLeftovers(w.dir, roomType, keep, w.started)
		switch {
		case err != nil:
			w.logger.Error("finishing the recordings the last process left failed", "error", err)
		case finished > 0:
			w.logger.Info("finished the recordings the last process left", "recordings", finished)
		}
		if retention.Off && w.maxSize < 0 {
			return
		}
		ticker := time.NewTicker(recordingCleanupEvery)
		defer ticker.Stop()
		for {
			w.cleanOnce(retention)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-w.cleanup:
			}
		}
	}()
}

// cleanOnce runs one cleanup pass, and tells the writer what's left.
func (w *recordingWriter) cleanOnce(retention Timeout) {
	var before time.Time
	if !retention.Off {
		before = time.Now().Add(-retention.After)
	}
	started := time.Now()
	result, err := cleanRecordings(w.dir, before, w.maxSize)
	if err != nil {
		w.logger.Error("deleting recordings failed; trying again within an hour", "error", err)
	}
	if result.deleted > 0 {
		w.logger.Info("deleted recordings", "took", time.Since(started), "files_looked_at", result.files,
			"recordings", result.deleted, "bytes", result.freed, "bytes_left", result.size)
	}
	w.cleaned(result.size)
}

// cleaned takes a cleanup pass's count of what's left. If that's still over maxSize, only recordings in progress are
// left, and no room is recorded until a pass finds them under it again.
func (w *recordingWriter) cleaned(size int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.size = size
	wasFull := w.full
	w.full = w.maxSize >= 0 && size > w.maxSize
	switch {
	case w.full && !wasFull:
		w.logger.Warn("the recordings in progress alone are over RecordingConfig.MaxSize, so they're abandoned, and no "+
			"room is recorded until the recordings fit again", "bytes", size, "max_size", w.maxSize)
	case wasFull && !w.full:
		w.logger.Info("the recordings fit in RecordingConfig.MaxSize again; new rooms are recorded", "bytes", size)
	}
}

// recordingFileKind is what a file in a room type's folder is, by its name.
type recordingFileKind int

const (
	notARecording recordingFileKind = iota
	recordingInProgress
	recordingDone
	recordingProblem
	recordingMarker
)

func kindOfRecordingFile(name string) recordingFileKind {
	switch {
	case strings.HasSuffix(name, recordingDoneExt):
		return recordingDone
	case strings.HasSuffix(name, recordingProblemExt):
		return recordingProblem
	case strings.HasSuffix(name, recordingExt):
		return recordingInProgress
	case strings.HasSuffix(name, recordingFlagExt):
		return recordingMarker
	}
	return notARecording
}

// cleanupResult is what a cleanup pass found and did.
type cleanupResult struct {
	size    int64 // bytes of recordings left, finished and in progress
	files   int   // recording files and markers looked at
	deleted int
	freed   int64
}

// finishedRecording is a finished recording a cleanup pass may delete.
type finishedRecording struct {
	path    string
	size    int64
	written time.Time
	problem bool
}

// cleanRecordings is one cleanup pass over each room type's folder in dir. It deletes the finished recordings last
// written before before, unless that's zero; then, while the recordings take more than maxSize bytes, the oldest
// finished ones, until they take 90% of it, those of rooms with a problem last; and markers whose recording is gone. A
// negative maxSize is no limit. Recordings in progress are never deleted, and anything that isn't a recording stays.
func cleanRecordings(dir string, before time.Time, maxSize int64) (cleanupResult, error) {
	found, err := listRecordings(dir)
	if found == nil {
		return cleanupResult{}, err
	}
	errs := []error{err}
	result := cleanupResult{size: found.size, files: found.files}
	remove := func(f *finishedRecording) {
		if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			return
		}
		result.size -= f.size
		result.deleted++
		result.freed += f.size
	}
	kept := found.finished[:0]
	for i := range found.finished {
		if !before.IsZero() && found.finished[i].written.Before(before) {
			remove(&found.finished[i])
			continue
		}
		kept = append(kept, found.finished[i])
	}
	if maxSize >= 0 && result.size > maxSize {
		slices.SortFunc(kept, func(a, b finishedRecording) int {
			if a.problem != b.problem {
				if a.problem {
					return 1 // problems last
				}
				return -1
			}
			return a.written.Compare(b.written)
		})
		target := maxSize / 10 * 9
		for i := range kept {
			if result.size <= target {
				break
			}
			remove(&kept[i])
		}
	}
	for _, marker := range found.markers {
		if found.inProgress[strings.TrimSuffix(marker, recordingFlagExt)+recordingExt] {
			continue
		}
		if err := os.Remove(marker); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return result, errors.Join(errs...)
}

// recordingListing is what a cleanup pass finds in the room types' folders.
type recordingListing struct {
	size       int64 // bytes of recordings, finished and in progress
	files      int   // recording files and markers
	finished   []finishedRecording
	markers    []string
	inProgress map[string]bool
}

// listRecordings walks each room type's folder in dir once. It returns nil if it can't read dir; a folder it can't read
// is skipped, and returned as an error with what the others hold.
func listRecordings(dir string) (*recordingListing, error) {
	types, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	found := &recordingListing{inProgress: make(map[string]bool)}
	var errs []error
	for _, typ := range types {
		if !typ.IsDir() {
			continue
		}
		folder := filepath.Join(dir, typ.Name())
		files, err := os.ReadDir(folder)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, file := range files {
			found.add(folder, file)
		}
	}
	return found, errors.Join(errs...)
}

// add counts one file of a room type's folder, if it's a recording or a marker.
func (l *recordingListing) add(folder string, file fs.DirEntry) {
	kind := kindOfRecordingFile(file.Name())
	if kind == notARecording || !file.Type().IsRegular() {
		return
	}
	l.files++
	path := filepath.Join(folder, file.Name())
	if kind == recordingMarker {
		l.markers = append(l.markers, path)
		return
	}
	info, err := file.Info()
	if err != nil {
		return // gone already
	}
	l.size += info.Size()
	if kind == recordingInProgress {
		l.inProgress[path] = true
		return
	}
	l.finished = append(l.finished, finishedRecording{
		path: path, size: info.Size(), written: info.ModTime(), problem: kind == recordingProblem,
	})
}

// finishLeftovers finishes the recordings in progress in roomType's folder that the last process left, written to
// before started, and that no room will append to again: their room isn't in keep, the rooms left in the store. Each is
// renamed by how it ended: done if its last segment closed normally, a problem if it has a marker, its room panicked or
// got stuck, or the process died while it ran. A file that holds no recording can't be replayed, so it's deleted. It
// returns how many it finished.
func finishLeftovers(dir, roomType string, keep map[RoomID]bool, started time.Time) (int, error) {
	folder := filepath.Join(dir, roomType)
	files, err := os.ReadDir(folder)
	if err != nil {
		return 0, err
	}
	finished := 0
	var errs []error
	for _, file := range files {
		name := file.Name()
		if kindOfRecordingFile(name) != recordingInProgress || !file.Type().IsRegular() ||
			keep[RoomID(strings.TrimSuffix(name, recordingExt))] {
			continue
		}
		info, err := file.Info()
		if err != nil || !info.ModTime().Before(started) {
			continue // gone, or this process's
		}
		path := filepath.Join(folder, name)
		segments, err := readRecordingFile(path)
		switch {
		case errors.Is(err, errNoRecording):
			removeRecordings(map[string]bool{path: true})
			continue
		case err != nil:
			errs = append(errs, err)
			continue
		}
		last := segments[len(segments)-1].close
		if last == nil || CloseReason(last.GetReason()) == ClosePanic || CloseReason(last.GetReason()) == CloseStuck {
			if err := makeMarker(path); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := finishFile(path); err != nil {
			errs = append(errs, err)
			continue
		}
		finished++
	}
	return finished, errors.Join(errs...)
}

// record notes the input of the turn running, to write once the turn ends. The callers check r.recorder first, so a room
// that isn't recorded doesn't build the record.
func (r *Room) record(turn *wire.RecordedTurn) {
	r.turnRecord = turn
}

// writeTurnRecord writes the running turn's record, if it has one, with its clock, error and the room's state after it.
func (r *Room) writeTurnRecord(err error, panicked bool) {
	turn := r.turnRecord
	if turn == nil {
		return
	}
	r.turnRecord = nil
	turn.UnixNanos = r.now.UnixNano()
	if err != nil {
		turn.Error = err.Error()
	}
	turn.Panicked = panicked
	if !panicked {
		turn.Hash = r.stateHash()
	}
	r.publish(func() {
		r.recorder.add(r.recordPath, &wire.RecordingRecord{Body: &wire.RecordingRecord_Turn{Turn: turn}})
	})
}

// startSegment writes the start of a segment, when the room starts running: its seed and Init's clock if it's new, its
// state as restored otherwise, and its state now, after the turn that gave it its sessions.
func (r *Room) startSegment(s roomSessions) {
	if r.recorder == nil {
		return
	}
	segment := &wire.RecordingSegment{
		RoomId:         string(r.id),
		RoomType:       r.typ.cfg.Name,
		Build:          r.recorder.build,
		Revision:       r.recorder.revision,
		Modules:        r.typ.recordedModules(),
		GraceNanos:     int64(s.grace),
		InactiveNanos:  int64(s.inactive),
		StartUnixNanos: r.now.UnixNano(),
		Hash:           r.stateHash(),
	}
	if r.restored != nil {
		segment.Restored, r.restored = r.restored, nil
	} else {
		segment.Seed, segment.InitUnixNanos = r.seed[:], r.initNow.UnixNano()
	}
	r.segmentStarted.Store(true)
	r.publish(func() {
		r.recorder.add(r.recordPath, &wire.RecordingRecord{Body: &wire.RecordingRecord_Segment{Segment: segment}})
	})
}

// recordClose ends the segment. It runs from finish, which can be the watchdog's goroutine, so it reads only the clock.
func (r *Room) recordClose(reason CloseReason) {
	if r.recorder == nil || !r.segmentStarted.Load() {
		return
	}
	r.recorder.add(r.recordPath, &wire.RecordingRecord{Body: &wire.RecordingRecord_Close{
		Close: &wire.RecordedClose{UnixNanos: r.clock().UnixNano(), Reason: int32(reason)}, //nolint:gosec // a small enum
	}})
}

// finishRecording tells the recorder, once the room has closed, how its recording goes on. A panic or a stuck turn marks
// it as having a problem. A room finished for good finishes it; goesOn says the room was restored, or stays saved for the
// next start, which appends to the same file.
func (r *Room) finishRecording(reason CloseReason, goesOn bool) {
	if r.recorder == nil || !r.segmentStarted.Load() {
		return
	}
	if reason == ClosePanic || reason == CloseStuck {
		r.recorder.flag(r.recordPath)
	}
	if !goesOn {
		r.recorder.finish(r.recordPath)
	}
}

// stateHash is the start of a SHA-256 of the room's state, encoded as a snapshot would be: this turn's snapshot if one
// was just encoded, or a new encoding. A recorded room encodes deterministically, so the same state gives the same hash.
func (r *Room) stateHash() []byte {
	data := r.encodedThisTurn
	if data == nil {
		data = r.encodeSnapshot()
	}
	sum := sha256.Sum256(data)
	return sum[:stateHashBytes]
}

// recordedModules lists each module's snapshot version.
func (t *RoomType) recordedModules() []*wire.RecordedModule {
	modules := make([]*wire.RecordedModule, 0, len(t.snapshots))
	for i := range t.snapshots {
		modules = append(modules, &wire.RecordedModule{
			Module: t.snapshots[i].module, Version: uint32(t.snapshots[i].version), //nolint:gosec // a small number
		})
	}
	return modules
}

// RecordingSegment is part of a room's recording: from when the room started running, new or restored, to when it
// stopped. A room restarted or restored after a crash has several, in order.
type RecordingSegment struct {
	Room     RoomID
	RoomType string
	// Build is a hash of the server's executable, and Revision the version control revision Go stamped into it, with
	// -modified for uncommitted changes. Replay the recording against the code of that revision.
	Build    string
	Revision string
	// Modules is each module's snapshot version.
	Modules map[string]int
	// Restored says the room was restored, after a restart, a deploy or a crash, rather than new.
	Restored bool
	Start    time.Time
	Turns    []RecordedTurn
	// Closed says the segment ended with the room closing, for Reason. A segment that wasn't closed was cut short: the
	// process died, or the file was copied while the room ran.
	Closed bool
	Reason CloseReason
}

// RecordedTurn is one turn of a recording: what started it, when by the room's clock, and how it ended.
type RecordedTurn struct {
	Time time.Time
	// Kind is what started the turn: "message", "seat", "leave", "timer", "grace", "reserve" or "reserve end".
	Kind      string
	Player    PlayerID
	Module    string
	MessageID uint32
	Payload   []byte
	Timer     TimerID
	// Players and MinPlayers are matchmaking's reservation.
	Players    []PlayerID
	MinPlayers int
	// RefusedBy is set when the server refused a seat while draining or overloaded, before the room was asked.
	RefusedBy string
	// Error is the turn's error, empty if it had none.
	Error    string
	Panicked bool
}

// The kinds of a RecordedTurn.
const (
	recordKindMessage    = "message"
	recordKindSeat       = "seat"
	recordKindLeave      = "leave"
	recordKindTimer      = "timer"
	recordKindGrace      = "grace"
	recordKindReserve    = "reserve"
	recordKindReserveEnd = "reserve end"
)

// String describes the turn, for a report.
func (t *RecordedTurn) String() string {
	var b strings.Builder
	b.WriteString(t.Kind)
	if t.Player != "" {
		fmt.Fprintf(&b, " from %s", t.Player)
	}
	switch t.Kind {
	case recordKindMessage:
		fmt.Fprintf(&b, ": module %q message %d, %d bytes", t.Module, t.MessageID, len(t.Payload))
	case recordKindTimer:
		fmt.Fprintf(&b, " %d", t.Timer)
	}
	fmt.Fprintf(&b, " at %s", t.Time.UTC().Format(time.RFC3339Nano))
	return b.String()
}

// ReadRecording reads a room's recording, a file from RecordingConfig.Dir, in progress or finished. A record the file
// ends in the middle of, or one damaged, ends the reading: what comes before is returned. Replaying needs the game's room
// type, with playholdtest.Replay.
func ReadRecording(path string) ([]RecordingSegment, error) {
	segments, err := readRecordingFile(path)
	if err != nil {
		return nil, err
	}
	out := make([]RecordingSegment, 0, len(segments))
	for i := range segments {
		out = append(out, segments[i].public())
	}
	return out, nil
}

// recordedSegment is a segment as read from the file.
type recordedSegment struct {
	start *wire.RecordingSegment
	turns []*wire.RecordedTurn
	close *wire.RecordedClose
}

// readRecordingFile reads a recording's records into segments, for ReadRecording and replays. A record cut off or
// damaged ends the reading, since nothing after it can be trusted; a file with no segment at all is an error.
func readRecordingFile(path string) ([]recordedSegment, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var segments []recordedSegment
	for len(data) >= recordHeaderSize {
		n := int(binary.LittleEndian.Uint32(data))
		sum := binary.LittleEndian.Uint32(data[4:])
		if len(data) < recordHeaderSize+n || crc32.ChecksumIEEE(data[recordHeaderSize:recordHeaderSize+n]) != sum {
			break // cut off or damaged: nothing after it can be trusted
		}
		record := &wire.RecordingRecord{}
		if err := proto.Unmarshal(data[recordHeaderSize:recordHeaderSize+n], record); err != nil {
			break
		}
		data = data[recordHeaderSize+n:]
		segments = addRecord(segments, record)
	}
	if len(segments) == 0 {
		return nil, fmt.Errorf("playhold: %s %w", path, errNoRecording)
	}
	return segments, nil
}

// errNoRecording means a file holds no segment, so there's nothing to replay.
var errNoRecording = errors.New("holds no recording")

// addRecord adds a record to the segments read so far. Records before the first segment, which a file can only have if
// it was damaged, are skipped.
func addRecord(segments []recordedSegment, record *wire.RecordingRecord) []recordedSegment {
	if start := record.GetSegment(); start != nil {
		return append(segments, recordedSegment{start: start})
	}
	if len(segments) == 0 {
		return segments
	}
	last := &segments[len(segments)-1]
	switch {
	case record.GetTurn() != nil:
		last.turns = append(last.turns, record.GetTurn())
	case record.GetClose() != nil:
		last.close = record.GetClose()
	}
	return segments
}

// public converts a segment as read from the file into the exported RecordingSegment that ReadRecording returns. It's
// unexported itself: only its result is part of playhold's API.
func (s *recordedSegment) public() RecordingSegment {
	modules := make(map[string]int, len(s.start.GetModules()))
	for _, module := range s.start.GetModules() {
		modules[module.GetModule()] = int(module.GetVersion())
	}
	out := RecordingSegment{
		Room:     RoomID(s.start.GetRoomId()),
		RoomType: s.start.GetRoomType(),
		Build:    s.start.GetBuild(),
		Revision: s.start.GetRevision(),
		Modules:  modules,
		Restored: len(s.start.GetRestored()) > 0,
		Start:    time.Unix(0, s.start.GetStartUnixNanos()).UTC(),
		Turns:    make([]RecordedTurn, 0, len(s.turns)),
	}
	for _, turn := range s.turns {
		out.Turns = append(out.Turns, publicTurn(turn))
	}
	if s.close != nil {
		out.Closed, out.Reason = true, CloseReason(s.close.GetReason())
	}
	return out
}

// publicTurn converts a turn as read from the file into the exported RecordedTurn, for ReadRecording and for a replay's
// report. It's unexported itself: only its result is part of playhold's API.
func publicTurn(turn *wire.RecordedTurn) RecordedTurn {
	out := RecordedTurn{Time: time.Unix(0, turn.GetUnixNanos()).UTC(), Error: turn.GetError(), Panicked: turn.GetPanicked()}
	switch {
	case turn.GetMessage() != nil:
		msg := turn.GetMessage()
		out.Kind, out.Player, out.Module, out.MessageID, out.Payload = recordKindMessage, PlayerID(msg.GetPlayer()),
			msg.GetModule(), msg.GetId(), msg.GetPayload()
	case turn.GetSeat() != nil:
		out.Kind, out.Player, out.RefusedBy = recordKindSeat, PlayerID(turn.GetSeat().GetPlayer()), turn.GetSeat().GetRefusedBy()
	case turn.GetLeave() != nil:
		out.Kind, out.Player = recordKindLeave, PlayerID(turn.GetLeave().GetPlayer())
	case turn.GetTimer() != nil:
		out.Kind, out.Timer = recordKindTimer, TimerID(turn.GetTimer().GetId())
	case turn.GetGrace() != nil:
		out.Kind, out.Player = recordKindGrace, PlayerID(turn.GetGrace().GetPlayer())
	case turn.GetReserve() != nil:
		out.Kind, out.MinPlayers = recordKindReserve, int(turn.GetReserve().GetMinPlayers())
		for _, player := range turn.GetReserve().GetPlayers() {
			out.Players = append(out.Players, PlayerID(player))
		}
	case turn.GetReserveEnd() != nil:
		out.Kind = recordKindReserveEnd
	}
	return out
}
