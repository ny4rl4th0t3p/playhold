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
	rt := newTestRuntime(t)
	w := newRecordingWriter(t.TempDir(), "test-build", slog.New(slog.DiscardHandler))
	t.Cleanup(w.close)
	rt.useRecorder(w)
	return rt, w
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

func kinds(segment *RecordingSegment) []string {
	out := make([]string, 0, len(segment.Turns))
	for i := range segment.Turns {
		out = append(out, segment.Turns[i].Kind)
	}
	return out
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

		segments, err := ReadRecording(path)
		require.NoError(t, err)
		require.Len(t, segments, 1)
		assert.Equal(t, []string{"seat", "message", "message", "message", "timer"}, kinds(&segments[0]))
		assert.Equal(t, errRecTooBig.Error(), segments[0].Turns[2].Error)
		assert.True(t, segments[0].Closed)
		assert.Equal(t, CloseRequested, segments[0].Reason)
		assert.Equal(t, "test-build", segments[0].Build)
		assert.False(t, segments[0].Restored)

		report, err := replayRecording(typ, path, nil)
		require.NoError(t, err)
		assert.Nil(t, report.Diverged)
		assert.Equal(t, 5, report.Turns)
		assert.True(t, report.SchemaMatches)
		assert.False(t, report.Incomplete)
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

		report, err := replayRecording(recordedType(t, recCounter{double: true}), w.path("rec", room.ID()), nil)

		require.NoError(t, err)
		require.NotNil(t, report.Diverged)
		assert.Equal(t, 2, report.Diverged.Turn, "the first move, after the seat")
		assert.Equal(t, "message", report.Diverged.Input.(RecordedTurn).Kind)
		assert.Contains(t, report.Diverged.Reason, "state")
		assert.NotEqual(t, report.Diverged.WantHash, report.Diverged.GotHash)
		assert.Equal(t, 1, report.Turns, "the seat replayed as recorded")
	})
}

func TestACrashAndARestoreAreSegmentsOfOneRecording(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, w := newRecordingRuntime(t)
		typ := recordedType(t, recCounter{})
		room := startRecordedRoom(t, rt, typ)
		playRecorded(t, room)
		var saved []byte
		runInRoom(t, room, func(rc *RoomContext) { saved = rc.room.encodeSnapshot() })
		require.ErrorIs(t, sendAndWait(t, room, recPanic, 0), ErrRoomClosed)
		waitFor(t, room.Done())

		restored, err := rt.restoreRoom(typ, room.ID(), "", saved, nil, nil, TimersKeepRunning)
		require.NoError(t, err)
		restored.startSessions(roomSessions{})
		require.NoError(t, sendAndWait(t, restored, recAdd, 1))
		restored.Close()
		waitFor(t, restored.Done())
		w.close()
		path := w.path("rec", room.ID())

		segments, err := ReadRecording(path)
		require.NoError(t, err)
		require.Len(t, segments, 2)
		assert.True(t, segments[0].Turns[len(segments[0].Turns)-1].Panicked)
		assert.Equal(t, ClosePanic, segments[0].Reason)
		assert.True(t, segments[1].Restored)

		report, err := replayRecording(typ, path, nil)
		require.NoError(t, err)
		assert.Nil(t, report.Diverged)
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
		path := w.path("rec", room.ID())
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

func TestDroppedRecordsLeaveAGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newRecordingWriter(t.TempDir(), "test-build", slog.New(slog.DiscardHandler))
		path := w.path("rec", "room")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), recordingDirMode))
		turn := func(id uint64) *wire.RecordingRecord {
			return &wire.RecordingRecord{Body: &wire.RecordingRecord_Turn{Turn: &wire.RecordedTurn{
				Input: &wire.RecordedTurn_Timer{Timer: &wire.RecordedTimer{Id: id}},
			}}}
		}
		w.add(path, &wire.RecordingRecord{Body: &wire.RecordingRecord_Segment{Segment: &wire.RecordingSegment{RoomId: "room"}}})
		w.add(path, turn(1))
		w.mu.Lock()
		w.limit = 0 // the disk can't keep up
		w.mu.Unlock()
		w.add(path, turn(2))
		w.mu.Lock()
		w.limit = maxRecordingWaiting
		w.mu.Unlock()
		w.add(path, turn(3))
		w.close()

		segments, err := ReadRecording(path)

		require.NoError(t, err)
		assert.True(t, segments[0].Gap)
		assert.Equal(t, []TimerID{1, 3}, []TimerID{segments[0].Turns[0].Timer, segments[0].Turns[1].Timer})
	})
}

func TestTheWriterMakesAFolderThatsGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newRecordingWriter(t.TempDir(), "test-build", slog.New(slog.DiscardHandler))
		path := w.path("rec", "room") // its folder was never made, as after someone deleted it

		w.add(path, &wire.RecordingRecord{Body: &wire.RecordingRecord_Segment{Segment: &wire.RecordingSegment{RoomId: "room"}}})
		w.close()

		segments, err := ReadRecording(path)
		require.NoError(t, err)
		assert.Equal(t, RoomID("room"), segments[0].Room)
	})
}

func TestRetentionDeletesOnlyOldRecordings(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "rec")
	require.NoError(t, os.MkdirAll(folder, recordingDirMode))
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"old.rec", "new.rec", "notes.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(folder, name), []byte("x"), recordingFileMode))
	}
	require.NoError(t, os.Chtimes(filepath.Join(folder, "old.rec"), old, old))
	require.NoError(t, os.Chtimes(filepath.Join(folder, "notes.txt"), old, old))

	deleted, err := deleteOldRecordings(dir, time.Now().Add(-24*time.Hour))

	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	assert.NoFileExists(t, filepath.Join(folder, "old.rec"))
	assert.FileExists(t, filepath.Join(folder, "new.rec"))
	assert.FileExists(t, filepath.Join(folder, "notes.txt"), "only recordings are deleted")
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
}
