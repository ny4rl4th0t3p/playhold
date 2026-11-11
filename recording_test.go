package playhold

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold/internal/wire"
	"github.com/ny4rl4th0t3p/playhold/store/memory"
)

// recCounter is a recorded module: players add to a count, a timer adds one, and a message panics. With double, it adds
// twice as much, as a changed build would.
type recCounter struct{ double bool }

type recCounterState struct{ count int64 }

var recCounterKey = NewKey[recCounterState]("rec-counter")

const (
	recAdd   uint32 = 1
	recTimer uint32 = 2
	recPanic uint32 = 3
)

var errRecTooBig = errors.New("too big")

func (recCounter) Name() string { return "rec-counter" }

func (m recCounter) Register(r *Registry) {
	State(r, recCounterKey, func() *recCounterState { return &recCounterState{} })
	Snapshot(r, recCounterKey, 1,
		func(state *recCounterState) *wrapperspb.Int64Value { return wrapperspb.Int64(state.count) },
		func(msg *wrapperspb.Int64Value) *recCounterState { return &recCounterState{count: msg.GetValue()} })
	Handle(r, recAdd, func(rc *RoomContext, msg *wrapperspb.Int64Value) error {
		if msg.GetValue() > 10 {
			return errRecTooBig
		}
		add := msg.GetValue()
		if m.double {
			add *= 2
		}
		recCounterKey.Get(rc).count += add
		return nil
	})
	Handle(r, recTimer, func(rc *RoomContext, _ *wrapperspb.Int64Value) error {
		rc.After(time.Minute, &wrapperspb.StringValue{Value: "add one"})
		return nil
	})
	Handle(r, recPanic, func(_ *RoomContext, _ *wrapperspb.Int64Value) error { panic("boom") })
	On(r, func(rc *RoomContext, _ *wrapperspb.StringValue) { recCounterKey.Get(rc).count++ })
}

func recordedType(t *testing.T, module recCounter) *RoomType {
	t.Helper()
	return newTestRoomType(t, RoomConfig{Name: "rec", Record: true, Modules: []Module{module}})
}

// newRecordingRuntime returns a runtime that records to a folder of the test's, with its recorder. Call it inside a
// bubble.
func newRecordingRuntime(t *testing.T) (*Runtime, *recordingWriter) {
	t.Helper()
	return recordingRuntime(t, newTestRuntime(t))
}

// recordingRuntime gives rt a recorder writing to a folder of the test's.
func recordingRuntime(t *testing.T, rt *Runtime) (*Runtime, *recordingWriter) {
	t.Helper()
	w := newRecordingWriter(t.TempDir(), "test-build", slog.New(slog.DiscardHandler))
	t.Cleanup(w.close)
	rt.useRecorder(w)
	return rt, w
}

// newTestRecordingWriter returns a writer to a folder of the test's, with the room type's folder made.
func newTestRecordingWriter(t *testing.T) *recordingWriter {
	t.Helper()
	w := newRecordingWriter(t.TempDir(), "test-build", slog.New(slog.DiscardHandler))
	t.Cleanup(w.close)
	require.NoError(t, os.MkdirAll(filepath.Join(w.dir, "rec"), recordingDirMode))
	return w
}

// setMaxSize sets the writer's limit after it has started, which a server does before; the lock keeps the race detector
// content.
func setMaxSize(w *recordingWriter, maxSize int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.maxSize = maxSize
}

// startRecordedRoom starts a room as the lobby does, which starts its recording.
func startRecordedRoom(t *testing.T, rt *Runtime, typ *RoomType) *Room {
	t.Helper()
	room, err := rt.CreateRoom(typ)
	require.NoError(t, err)
	room.startSessions(roomSessions{})
	return room
}

func sendAndWait(t *testing.T, room *Room, id uint32, value int64) error {
	t.Helper()
	payload, err := proto.Marshal(wrapperspb.Int64(value))
	require.NoError(t, err)
	results := make(chan error, 1)
	require.NoError(t, room.dispatch("alice", "rec-counter", id, payload, func(err error) { results <- err }))
	synctest.Wait()
	select {
	case err := <-results:
		return err
	default:
		return ErrRoomClosed // a panic, which closes the room
	}
}

// playRecorded plays a short game in a recorded room: a seat, two moves, one refused, and a timer that fires.
func playRecorded(t *testing.T, room *Room) {
	t.Helper()
	require.NoError(t, seatAndWait(t, room, "alice"))
	require.NoError(t, sendAndWait(t, room, recAdd, 2))
	require.ErrorIs(t, sendAndWait(t, room, recAdd, 20), errRecTooBig)
	require.NoError(t, sendAndWait(t, room, recTimer, 0))
	time.Sleep(time.Minute)
	synctest.Wait()
}

// flushed lets the writer write what's waiting.
func flushed() {
	time.Sleep(recordingFlushEvery)
	synctest.Wait()
}

func kinds(segment *RecordingSegment) []string {
	out := make([]string, 0, len(segment.Turns))
	for i := range segment.Turns {
		out = append(out, segment.Turns[i].Kind)
	}
	return out
}

// segmentRecord, turnRecord and closeRecord are records as rooms write them, for tests of the writer alone.
func segmentRecord(room string) *wire.RecordingRecord {
	return &wire.RecordingRecord{Body: &wire.RecordingRecord_Segment{Segment: &wire.RecordingSegment{RoomId: room}}}
}

func turnRecord(timer uint64) *wire.RecordingRecord {
	return &wire.RecordingRecord{Body: &wire.RecordingRecord_Turn{Turn: &wire.RecordedTurn{
		Input: &wire.RecordedTurn_Timer{Timer: &wire.RecordedTimer{Id: timer}},
	}}}
}

func closeRecord(reason CloseReason) *wire.RecordingRecord {
	return &wire.RecordingRecord{Body: &wire.RecordingRecord_Close{Close: &wire.RecordedClose{
		Reason: int32(reason), //nolint:gosec // a small enum
	}}}
}

func TestARecordedRoomReplaysTurnForTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, w := newRecordingRuntime(t)
		typ := recordedType(t, recCounter{})
		room := startRecordedRoom(t, rt, typ)
		playRecorded(t, room)
		room.Close()
		waitFor(t, room.Done())
		w.close()
		path := w.path("rec", room.ID())

		assert.NoFileExists(t, path, "finished, so renamed")
		done := finishedPath(path, false)
		segments, err := ReadRecording(done)
		require.NoError(t, err)
		require.Len(t, segments, 1)
		assert.Equal(t, []string{"seat", "message", "message", "message", "timer"}, kinds(&segments[0]))
		assert.Equal(t, errRecTooBig.Error(), segments[0].Turns[2].Error, "a turn's error isn't a problem")
		assert.True(t, segments[0].Closed)
		assert.Equal(t, CloseRequested, segments[0].Reason)
		assert.Equal(t, "test-build", segments[0].Build)
		assert.False(t, segments[0].Restored)

		report, err := replayRecording(typ, done, nil)
		require.NoError(t, err)
		assert.Nil(t, report.Diverged)
		assert.Equal(t, 5, report.Turns)
		assert.True(t, report.SchemaMatches)
	})
}

func TestAReplayFindsTheFirstTurnThatDiffers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, w := newRecordingRuntime(t)
		room := startRecordedRoom(t, rt, recordedType(t, recCounter{}))
		playRecorded(t, room)
		room.Close()
		waitFor(t, room.Done())
		w.close()

		report, err := replayRecording(recordedType(t, recCounter{double: true}), finishedPath(w.path("rec", room.ID()), false), nil)

		require.NoError(t, err)
		require.NotNil(t, report.Diverged)
		assert.Equal(t, 2, report.Diverged.Turn, "the first move, after the seat")
		assert.Equal(t, "message", report.Diverged.Input.(RecordedTurn).Kind)
		assert.Contains(t, report.Diverged.Reason, "state")
		assert.NotEqual(t, report.Diverged.WantHash, report.Diverged.GotHash)
		assert.Equal(t, 1, report.Turns, "the seat replayed as recorded")
	})
}

func TestARoomRestoredAfterAPanicGoesOnInTheSameRecording(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		snapshots := newTestWriter(t, memory.New()) // first, so it closes after the runtime has shut down
		rt, w := newRecordingRuntime(t)
		typ := recordedType(t, recCounter{})
		l := newLobby(rt, typ, testIdle, testMaxRooms, snapshots)
		room := enterRoom(t, l, testCode)
		playRecorded(t, room)
		path := w.path("rec", room.ID())

		require.ErrorIs(t, sendAndWait(t, room, recPanic, 0), ErrMessageFailed)
		waitFor(t, room.Done())
		next := l.replacement(testCode, room)
		require.NotNil(t, next, "restored from its save")
		require.NoError(t, sendAndWait(t, next, recAdd, 1))
		flushed()
		assert.FileExists(t, path, "still in progress")
		assert.FileExists(t, markerPath(path), "marked as having a problem")

		next.Close()
		waitFor(t, next.Done())
		w.close()

		assert.NoFileExists(t, path)
		assert.NoFileExists(t, markerPath(path), "the marker goes with the rename")
		problem := finishedPath(path, true)
		segments, err := ReadRecording(problem)
		require.NoError(t, err)
		require.Len(t, segments, 2)
		assert.True(t, segments[0].Turns[len(segments[0].Turns)-1].Panicked)
		assert.Equal(t, ClosePanic, segments[0].Reason)
		assert.True(t, segments[1].Restored)
		assert.Equal(t, CloseRequested, segments[1].Reason)

		report, err := replayRecording(typ, problem, nil)
		require.NoError(t, err)
		assert.Nil(t, report.Diverged)
	})
}

func TestAStuckRoomsRecordingIsAProblem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		rt, w := recordingRuntime(t, newConfiguredRuntime(t, Config{StuckTimeout: timeout}))
		room := startRecordedRoom(t, rt, recordedType(t, recCounter{}))
		require.NoError(t, seatAndWait(t, room, "alice"))

		release := make(chan struct{})
		require.NoError(t, room.submit(input{run: func(_ *RoomContext) error {
			<-release
			return nil
		}}))
		time.Sleep(timeout)
		synctest.Wait()
		close(release)
		synctest.Wait()
		w.close()

		segments, err := ReadRecording(finishedPath(w.path("rec", room.ID()), true))
		require.NoError(t, err)
		assert.Equal(t, CloseStuck, segments[0].Reason)
	})
}

func TestARoomShutDownWithASaveStaysInProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		snapshots := newTestWriter(t, memory.New())
		rt, w := newRecordingRuntime(t)
		l := newLobby(rt, recordedType(t, recCounter{}), testIdle, testMaxRooms, snapshots)
		room := enterRoom(t, l, testCode)
		require.NoError(t, seatAndWait(t, room, "alice"))

		stopProcess(t, rt)
		w.close()

		assert.FileExists(t, w.path("rec", room.ID()), "the next start restores the room, and appends to it")
	})
}

func TestARecordingCutOffStillReplaysUpToTheCut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, w := newRecordingRuntime(t)
		typ := recordedType(t, recCounter{})
		room := startRecordedRoom(t, rt, typ)
		playRecorded(t, room)
		room.Close()
		waitFor(t, room.Done())
		w.close()
		path := finishedPath(w.path("rec", room.ID()), false)
		info, err := os.Stat(path)
		require.NoError(t, err)

		require.NoError(t, os.Truncate(path, info.Size()-3)) // the close record, cut off

		segments, err := ReadRecording(path)
		require.NoError(t, err)
		assert.False(t, segments[0].Closed, "the cut-off close is ignored")
		assert.Len(t, segments[0].Turns, 5)
		report, err := replayRecording(typ, path, nil)
		require.NoError(t, err)
		assert.Nil(t, report.Diverged)
	})
}

func TestARecordingThatFallsBehindIsAbandonedWhole(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newTestRecordingWriter(t)
		kept, dropped := w.path("rec", "kept"), w.path("rec", "dropped")
		w.add(kept, segmentRecord("kept"))
		w.add(dropped, segmentRecord("dropped"))
		flushed()
		require.FileExists(t, dropped)

		w.mu.Lock()
		w.limit = 0 // the disk can't keep up
		w.mu.Unlock()
		w.add(dropped, turnRecord(1))
		w.mu.Lock()
		w.limit = maxRecordingWaiting
		w.mu.Unlock()
		w.add(dropped, turnRecord(2)) // fits, but its recording is already abandoned
		w.add(kept, turnRecord(1))
		w.close()

		assert.NoFileExists(t, dropped, "deleted, not left with a gap")
		segments, err := ReadRecording(kept)
		require.NoError(t, err)
		assert.Len(t, segments[0].Turns, 1, "the other room's recording stays whole")
	})
}

func TestANewSegmentRecordsAnAbandonedRoomAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newTestRecordingWriter(t)
		path := w.path("rec", "room")
		w.add(path, segmentRecord("room"))
		w.mu.Lock()
		w.abandon(path, "a test")
		w.mu.Unlock()

		w.add(path, turnRecord(1))
		w.add(path, segmentRecord("room")) // restored from its save: a whole recording again
		w.add(path, turnRecord(2))
		w.close()

		segments, err := ReadRecording(path)
		require.NoError(t, err)
		require.Len(t, segments, 1)
		assert.Equal(t, []TimerID{2}, []TimerID{segments[0].Turns[0].Timer})
	})
}

func TestAFailedWriteAbandonsTheRecording(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newTestRecordingWriter(t)
		blocked := filepath.Join(w.dir, "blocked")
		require.NoError(t, os.WriteFile(blocked, nil, recordingFileMode)) // a file where the room type's folder should be
		path := w.path("blocked", "room")

		w.add(path, segmentRecord("room"))
		flushed()

		w.mu.Lock()
		defer w.mu.Unlock()
		assert.True(t, w.abandoned[path])
		assert.True(t, w.failing)
	})
}

func TestTheWriterMakesAFolderThatsGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newRecordingWriter(t.TempDir(), "test-build", slog.New(slog.DiscardHandler))
		path := w.path("rec", "room") // its folder was never made, as after someone deleted it

		w.add(path, segmentRecord("room"))
		w.close()

		segments, err := ReadRecording(path)
		require.NoError(t, err)
		assert.Equal(t, RoomID("room"), segments[0].Room)
	})
}

func TestAFinishedRecordingIsRenamedByHowItEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newTestRecordingWriter(t)
		done, problem := w.path("rec", "done"), w.path("rec", "problem")
		w.add(done, segmentRecord("done"))
		w.add(problem, segmentRecord("problem"))
		w.flag(problem)
		flushed()
		require.FileExists(t, markerPath(problem))

		w.add(done, closeRecord(CloseRequested))
		w.add(problem, closeRecord(CloseRequested)) // restored after its panic, then closed normally
		w.finish(done)
		w.finish(problem)
		w.close()

		assert.FileExists(t, finishedPath(done, false))
		assert.FileExists(t, finishedPath(problem, true), "the marker survives until the end")
		assert.NoFileExists(t, markerPath(problem))
		segments, err := ReadRecording(finishedPath(done, false))
		require.NoError(t, err)
		assert.True(t, segments[0].Closed, "renamed only after its last record")
	})
}

func TestOverMaxSizeWithOnlyRoomsRunningNothingIsRecorded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newTestRecordingWriter(t)
		setMaxSize(w, 1000)
		running, later := w.path("rec", "running"), w.path("rec", "later")
		w.add(running, segmentRecord("running"))
		flushed()

		w.cleaned(cleanupResult{size: 2000}) // a pass found only recordings in progress, over the limit
		w.add(running, turnRecord(1))
		w.add(later, segmentRecord("later"))
		flushed()

		assert.NoFileExists(t, running, "abandoned")
		assert.NoFileExists(t, later, "never started")
		select {
		case <-w.cleanup:
		default:
			t.Error("abandoning freed space, so the writer asks for a pass to count again")
		}

		w.cleaned(cleanupResult{})
		w.add(later, segmentRecord("later"))
		w.close()
		assert.FileExists(t, later, "recorded again once the recordings fit")
	})
}

func TestTheWriterAsksForACleanupPastMaxSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newTestRecordingWriter(t)
		setMaxSize(w, 10)
		path := w.path("rec", "room")

		w.add(path, segmentRecord("a room whose segment is over ten bytes"))
		flushed()

		select {
		case <-w.cleanup:
		default:
			t.Fatal("no pass asked for")
		}
		w.add(path, turnRecord(1))
		flushed()
		select {
		case <-w.cleanup:
			t.Error("asked again, while still over the limit the last pass counted")
		default:
		}
	})
}

// writeAged writes a file of size bytes, last written age ago.
func writeAged(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, make([]byte, size), recordingFileMode))
	at := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, at, at))
}

// smallPace makes cleanup passes work two files at a time, with no pause, so a test with a few files covers several
// batches.
var smallPace = cleanupPace{batch: 2}

func TestACleanupDeletesTheOldestFinishedRecordingsProblemsLast(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "rec")
	require.NoError(t, os.MkdirAll(folder, recordingDirMode))
	in := func(name string) string { return filepath.Join(folder, name) }
	writeAged(t, in("oldest"+recordingDoneExt), 100, 5*time.Hour)
	writeAged(t, in("old"+recordingDoneExt), 100, 4*time.Hour)
	writeAged(t, in("older-problem"+recordingProblemExt), 100, 6*time.Hour)
	writeAged(t, in("new"+recordingDoneExt), 100, time.Hour)
	writeAged(t, in("running"+recordingExt), 100, 7*time.Hour)
	writeAged(t, in("running"+recordingFlagExt), 0, 7*time.Hour)
	writeAged(t, in("gone"+recordingFlagExt), 0, time.Hour)
	writeAged(t, in("notes.txt"), 1000, 8*time.Hour)

	result, err := cleanRecordings(dir, time.Time{}, 400, 500, smallPace) // 500 bytes of recordings; 90% of 400 is 360

	require.NoError(t, err)
	assert.Equal(t, int64(300), result.size)
	assert.Equal(t, 2, result.deleted)
	assert.Equal(t, 2, result.finishedLeft)
	assert.NoFileExists(t, in("oldest"+recordingDoneExt))
	assert.NoFileExists(t, in("old"+recordingDoneExt))
	assert.FileExists(t, in("new"+recordingDoneExt))
	assert.FileExists(t, in("older-problem"+recordingProblemExt), "problems go last, however old")
	assert.FileExists(t, in("running"+recordingExt), "never a recording in progress")
	assert.FileExists(t, in("running"+recordingFlagExt), "nor its marker")
	assert.NoFileExists(t, in("gone"+recordingFlagExt), "a marker left alone goes")
	assert.FileExists(t, in("notes.txt"), "only recordings are deleted")
}

func TestACleanupFromACountTooLowFreesTheRestNextTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newTestRecordingWriter(t)
		setMaxSize(w, 400)
		in := func(name string) string { return filepath.Join(w.dir, "rec", name) }
		for i, name := range []string{"a", "b", "c", "d", "e"} { // a is the oldest
			writeAged(t, in(name+recordingDoneExt), 100, time.Duration(5-i)*time.Hour)
		}

		// The first pass after a start: the writer hasn't counted anything yet.
		first, err := cleanRecordings(w.dir, time.Time{}, 400, 0, smallPace)
		require.NoError(t, err)
		assert.Zero(t, first.deleted, "it didn't know it had to free anything")
		assert.Equal(t, int64(500), first.size)
		w.cleaned(first)

		select {
		case <-w.cleanup:
		default:
			t.Fatal("over the limit with finished recordings left, so it asks for another pass")
		}
		w.mu.Lock()
		assert.False(t, w.full, "finished recordings can still go, so rooms are still recorded")
		count := w.size
		w.mu.Unlock()

		second, err := cleanRecordings(w.dir, time.Time{}, 400, count, smallPace)
		require.NoError(t, err)
		assert.Equal(t, int64(300), second.size)
		assert.NoFileExists(t, in("a"+recordingDoneExt))
		assert.NoFileExists(t, in("b"+recordingDoneExt))
		assert.FileExists(t, in("c"+recordingDoneExt))
	})
}

func TestACountTooHighStillStopsAtNinetyPercent(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "rec")
	require.NoError(t, os.MkdirAll(folder, recordingDirMode))
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		writeAged(t, filepath.Join(folder, name+recordingDoneExt), 100, time.Duration(5-i)*time.Hour)
	}

	result, err := cleanRecordings(dir, time.Time{}, 400, 5000, smallPace)

	require.NoError(t, err)
	assert.Equal(t, int64(300), result.size, "the pass's own count decides when it has freed enough")
	assert.Equal(t, 2, result.deleted)
}

func TestRetentionDeletesOnlyOldFinishedRecordings(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "rec")
	require.NoError(t, os.MkdirAll(folder, recordingDirMode))
	in := func(name string) string { return filepath.Join(folder, name) }
	writeAged(t, in("old"+recordingDoneExt), 1, 48*time.Hour)
	writeAged(t, in("old-problem"+recordingProblemExt), 1, 48*time.Hour)
	writeAged(t, in("new"+recordingDoneExt), 1, time.Hour)
	writeAged(t, in("waiting"+recordingExt), 1, 48*time.Hour)
	writeAged(t, in("notes.txt"), 1, 48*time.Hour)

	result, err := cleanRecordings(dir, time.Now().Add(-24*time.Hour), -1, 0, smallPace)

	require.NoError(t, err)
	assert.Equal(t, 2, result.deleted)
	assert.NoFileExists(t, in("old"+recordingDoneExt))
	assert.NoFileExists(t, in("old-problem"+recordingProblemExt), "a problem doesn't extend retention")
	assert.FileExists(t, in("new"+recordingDoneExt))
	assert.FileExists(t, in("waiting"+recordingExt), "never a recording in progress, such as one waiting to be restored")
	assert.FileExists(t, in("notes.txt"), "only recordings are deleted")
}

// Outside a bubble, whose clock would be older than the files' real times.
func TestTheLastProcessesRecordingsAreFinishedAtStart(t *testing.T) {
	w := newTestRecordingWriter(t)
	crashed, shutDown, saved, panicked := w.path("rec", "crashed"), w.path("rec", "shut-down"),
		w.path("rec", "saved"), w.path("rec", "panicked")
	w.add(crashed, segmentRecord("crashed"))
	w.add(crashed, turnRecord(1)) // then the process died
	w.add(shutDown, segmentRecord("shut-down"))
	w.add(shutDown, closeRecord(CloseShutdown)) // with no store, so nothing restores it
	w.add(saved, segmentRecord("saved"))
	w.add(saved, closeRecord(CloseShutdown))
	w.add(panicked, segmentRecord("panicked"))
	w.add(panicked, closeRecord(ClosePanic))
	w.close()
	empty := w.path("rec", "empty")
	require.NoError(t, os.WriteFile(empty, []byte("not a recording"), recordingFileMode))

	nextStart := time.Now().Add(time.Minute)
	finished, err := finishLeftovers(w.dir, "rec", map[RoomID]bool{"saved": true}, nextStart, smallPace)

	require.NoError(t, err)
	assert.Equal(t, 3, finished)
	assert.FileExists(t, finishedPath(crashed, true), "a crash is a problem")
	assert.FileExists(t, finishedPath(shutDown, false))
	assert.FileExists(t, finishedPath(panicked, true))
	assert.FileExists(t, saved, "its room is in the store, to be restored")
	assert.NoFileExists(t, empty, "it can't be replayed")
	assert.NoFileExists(t, markerPath(crashed))
}

func TestTheRunningProcessesRecordingsArentLeftovers(t *testing.T) {
	w := newTestRecordingWriter(t)
	path := w.path("rec", "running")
	w.add(path, segmentRecord("running"))
	w.close()

	finished, err := finishLeftovers(w.dir, "rec", nil, time.Now().Add(-time.Minute), smallPace)

	require.NoError(t, err)
	assert.Zero(t, finished)
	assert.FileExists(t, path, "written since this process started")
}

func TestARecordedRoomTypeMustBeAbleToRecord(t *testing.T) {
	tests := []struct {
		name    string
		cfg     RoomConfig
		wantErr string
	}{
		{name: "a name that isn't a folder name", cfg: RoomConfig{Name: "my game", Modules: []Module{recCounter{}}},
			wantErr: "folder name"},
		{name: "no name", cfg: RoomConfig{Modules: []Module{recCounter{}}}, wantErr: "folder name"},
		{name: "state with no snapshot", cfg: RoomConfig{Name: "rec", Modules: []Module{recCounter{}, unsavedCount{}}},
			wantErr: "needs a snapshot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.Record = true

			_, err := NewRoomType(tt.cfg)

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestARecordedRoomTypeNeedsTheServersFolder(t *testing.T) {
	cfg := &ServerConfig{
		RoomType:    recordedType(t, recCounter{}),
		MemoryLimit: -1,
		Logger:      slog.New(slog.DiscardHandler),
		Development: true,
		Admission:   Admission{Off: true},
	}
	_, err := NewServer(newTestRuntime(t), cfg)
	require.ErrorContains(t, err, "Recording.Dir")

	cfg.Recording.Dir = t.TempDir()
	server, err := NewServer(newTestRuntime(t), cfg)
	require.NoError(t, err)
	t.Cleanup(server.Close)
	assert.DirExists(t, filepath.Join(cfg.Recording.Dir, "rec"), "a folder for the room type")
	assert.Equal(t, int64(defaultRecordingMaxSize), server.recorder.maxSize)
}
