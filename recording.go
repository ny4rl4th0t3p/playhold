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
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
)

const (
	defaultRecordingRetention = 30 * 24 * time.Hour
	recordingRetentionEvery   = time.Hour
	recordingFlushEvery       = 100 * time.Millisecond
	// maxRecordingWaiting is how many bytes of records wait to be written, across all rooms, before new ones are dropped.
	maxRecordingWaiting = 8 << 20
	recordingFileMode   = 0o600 // only the server's user: it holds players' messages
	recordingDirMode    = 0o700
	recordingExt        = ".rec"
	recordHeaderSize    = 8 // the record's length, then its CRC-32
	stateHashBytes      = 8
)

// RecordingConfig says where the server keeps recordings, and for how long. A room type turns recording on for its rooms
// with RoomConfig.Record; this is the server's side of it. Each room type's recordings go in a folder of their own, named
// after the room type, one file per room, named by its ID. A recording holds every player's messages, so keep it as
// private as the database.
type RecordingConfig struct {
	// Dir is where recordings go. Empty turns recording off, and NewServer then refuses a room type that records.
	Dir string
	// Retention deletes a recording this long after it was last written to, checked every hour, so a long game keeps
	// its whole history while it runs. The default is 30 days. Off keeps every recording, and the server logs a warning.
	Retention Timeout
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
// recordingFlushEvery, each file with records waiting is opened, appended to in one write, and closed again. Past
// maxRecordingWaiting bytes waiting, new records are dropped, and the next one kept is preceded by a gap.
type recordingWriter struct {
	dir      string
	build    string
	revision string
	logger   *slog.Logger
	limit    int // maxRecordingWaiting; tests lower it

	mu       sync.Mutex
	pending  map[string][]byte // by file
	waiting  int
	gaps     map[string]uint32 // records dropped since a file's last kept one
	dropping bool              // logged once until records are kept again
	failing  bool              // a write failed, logged once until one works

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

func newRecordingWriter(dir, build string, logger *slog.Logger) *recordingWriter {
	w := &recordingWriter{
		dir: dir, build: build, revision: buildRevision(), logger: logger, limit: maxRecordingWaiting,
		pending: make(map[string][]byte), gaps: make(map[string]uint32),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go w.run()
	return w
}

// path is the file of a room's recording.
func (w *recordingWriter) path(roomType string, room RoomID) string {
	return filepath.Join(w.dir, roomType, string(room)+recordingExt)
}

// add queues a record for the file. It never waits on the disk.
func (w *recordingWriter) add(path string, record *wire.RecordingRecord) {
	data, err := proto.Marshal(record)
	if err != nil {
		w.logger.Error("a recording's record can't be encoded, so it's dropped", "error", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.waiting+recordHeaderSize+len(data) > w.limit {
		w.gaps[path]++
		if !w.dropping {
			w.dropping = true
			w.logger.Warn("recordings are being written slower than rooms make them; records are dropped, and those "+
				"recordings can't be replayed past the gap", "waiting_bytes", w.waiting)
		}
		return
	}
	w.dropping = false
	if dropped := w.gaps[path]; dropped > 0 {
		delete(w.gaps, path)
		gap, _ := proto.Marshal(&wire.RecordingRecord{Body: &wire.RecordingRecord_Gap{Gap: &wire.RecordedGap{Dropped: dropped}}})
		w.append(path, gap)
	}
	w.append(path, data)
}

// append adds one record to the file's waiting bytes. The caller holds mu.
func (w *recordingWriter) append(path string, data []byte) {
	w.pending[path] = appendRecord(w.pending[path], data)
	w.waiting += recordHeaderSize + len(data)
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

// flush writes what's waiting, each file in one write. A process that dies in between leaves whole records, so the file
// can still be read up to there.
func (w *recordingWriter) flush() {
	w.mu.Lock()
	batch := w.pending
	w.pending, w.waiting = make(map[string][]byte), 0
	w.mu.Unlock()

	var failed error
	for path, data := range batch {
		if err := appendToFile(path, data); err != nil {
			failed = err
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case failed != nil && !w.failing:
		w.failing = true
		w.logger.Error("writing recordings failed; rooms go on, but their recordings miss these turns", "error", failed)
	case failed == nil && len(batch) > 0:
		w.failing = false
	}
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

// startRecordingRetention deletes recordings not written to for longer than the retention, at once and then every hour,
// until ctx ends, unless it's off.
func startRecordingRetention(ctx context.Context, cfg *RecordingConfig, logger *slog.Logger) {
	retention := cfg.Retention.orDefault(defaultRecordingRetention)
	if retention.Off {
		logger.Warn("recordings are never deleted; set RecordingConfig.Retention, or delete old ones yourself")
		return
	}
	go func() {
		ticker := time.NewTicker(recordingRetentionEvery)
		defer ticker.Stop()
		for {
			deleted, err := deleteOldRecordings(cfg.Dir, time.Now().Add(-retention.After))
			switch {
			case err != nil:
				logger.Error("deleting old recordings failed; trying again in an hour", "error", err)
			case deleted > 0:
				logger.Info("deleted old recordings", "recordings", deleted, "older_than", retention.After)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// deleteOldRecordings deletes the recordings in each room type's folder last written before the time. It only touches
// recording files, so anything else in the folders stays.
func deleteOldRecordings(dir string, before time.Time) (int, error) {
	types, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	deleted := 0
	var errs []error
	for _, typ := range types {
		if !typ.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, typ.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, file := range files {
			if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), recordingExt) {
				continue
			}
			info, err := file.Info()
			if err != nil || !info.ModTime().Before(before) {
				continue // gone already, or recent
			}
			if err := os.Remove(filepath.Join(dir, typ.Name(), file.Name())); err != nil {
				errs = append(errs, err)
				continue
			}
			deleted++
		}
	}
	return deleted, errors.Join(errs...)
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
	// Gap says records were dropped, because the disk was too slow: the segment can't be replayed past it.
	Gap bool
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

// ReadRecording reads a room's recording, a file from RecordingConfig.Dir. A record the file ends in the middle of, or
// one damaged, ends the reading: what comes before is returned. Replaying needs the game's room type, with
// playholdtest.Replay.
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
	gapAt int // how many turns came before the first gap, or -1 without one
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
		return nil, fmt.Errorf("playhold: %s holds no recording", path)
	}
	return segments, nil
}

// addRecord adds a record to the segments read so far. Records before the first segment, which a file can only have if
// it was damaged, are skipped.
func addRecord(segments []recordedSegment, record *wire.RecordingRecord) []recordedSegment {
	if start := record.GetSegment(); start != nil {
		return append(segments, recordedSegment{start: start, gapAt: -1})
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
	case record.GetGap() != nil && last.gapAt < 0:
		last.gapAt = len(last.turns)
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
		Gap:      s.gapAt >= 0,
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
