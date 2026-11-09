package playhold

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testJournal(t *testing.T, cfg JournalConfig, logger *slog.Logger) *journal {
	t.Helper()
	if cfg.Path == "" {
		cfg.Path = filepath.Join(t.TempDir(), "journal")
	}
	cfg, err := cfg.withDefaults()
	require.NoError(t, err)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	j, err := openJournal(cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = j.close() })
	return j
}

// write journals one turn and marks it, like a room does.
func write(j *journal, e *JournalEntry, states ...JournalState) uint64 {
	_ = j.begin(nil, e)
	for _, state := range states {
		j.mark(e.Seq%j.slots, e.Seq, state)
	}
	return e.Seq
}

func TestJournalConfigDefaultsAndLimits(t *testing.T) {
	cfg, err := JournalConfig{Path: "j"}.withDefaults()
	require.NoError(t, err)
	assert.Equal(t, JournalConfig{Path: "j", Entries: 1024, EntrySize: 4096}, cfg)

	_, err = JournalConfig{EntrySize: 100}.withDefaults()
	require.ErrorContains(t, err, "EntrySize")
	_, err = JournalConfig{Entries: maxJournalEntries + 1}.withDefaults()
	require.ErrorContains(t, err, "Entries")
}

func TestJournalKeepsOnlyTheLastEntriesAndNeverGrows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, JournalConfig{Path: path, Entries: 4, EntrySize: 512}, nil)
	info, err := os.Stat(path)
	require.NoError(t, err)

	for i := range 6 {
		write(j, &JournalEntry{Room: "r", Player: "alice", Module: "deck", MessageID: uint32(i)}, JournalDone)
	}

	entries, err := ReadCrashJournal(path)
	require.NoError(t, err)
	require.Len(t, entries, 4, "the two oldest were overwritten")
	for i := range entries {
		assert.Equal(t, uint64(i+3), entries[i].Seq)
		assert.Equal(t, JournalDone, entries[i].State)
		assert.Equal(t, PlayerID("alice"), entries[i].Player)
	}
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), after.Size())
}

func TestJournalCutsALongPayloadAndKeepsItsSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, JournalConfig{Path: path, Entries: 2, EntrySize: 512}, nil)
	payload := bytes.Repeat([]byte("x"), 2000)

	write(j, &JournalEntry{Module: "chat", Size: len(payload), Payload: payload})

	entries, err := ReadCrashJournal(path)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, 2000, entries[0].Size)
	assert.Less(t, len(entries[0].Payload), 2000)
	assert.Equal(t, payload[:len(entries[0].Payload)], entries[0].Payload)
}

func TestJournalSkipsAnEntryCutOffMidWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, JournalConfig{Path: path, Entries: 2, EntrySize: 512}, nil)
	seq := write(j, &JournalEntry{Module: "deck", Payload: []byte("move")})

	// A crash in the middle of the write leaves a body that doesn't match its checksum.
	offset := int64(journalHeaderSize) + int64(seq%2)*512 + slotHeaderSize + 4
	_, err := j.f.WriteAt([]byte{0xff}, offset)
	require.NoError(t, err)

	entries, err := ReadCrashJournal(path)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestALateMarkOnAReusedSlotLeavesTheNewEntryUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, JournalConfig{Path: path, Entries: 2, EntrySize: 512}, nil)
	stuck := write(j, &JournalEntry{Module: "slow"})
	write(j, &JournalEntry{Module: "other"}, JournalDone)
	write(j, &JournalEntry{Module: "newer"}, JournalDone) // takes the slow turn's slot

	j.mark(stuck%j.slots, stuck, JournalStuck) // the watchdog, late

	entries, err := ReadCrashJournal(path)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "newer", entries[1].Module)
	assert.Equal(t, JournalUnknown, entries[1].State, "not blamed as stuck")
}

func TestTurnsInFlightAtACrashAreReportedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	cfg := JournalConfig{Path: path, Entries: 8, EntrySize: 512}
	first := testJournal(t, cfg, nil)
	// The process died while a's turn ran, after b's handler finished, and after c's turn was done.
	write(first, &JournalEntry{Room: "a", Module: "deck", MessageID: 1})
	write(first, &JournalEntry{Room: "b", Module: "deck", MessageID: 2}, JournalHandled)
	write(first, &JournalEntry{Room: "c", Module: "deck", MessageID: 3}, JournalDone)
	require.NoError(t, first.close())

	logs := &lockedBuffer{}
	second := testJournal(t, cfg, slog.New(slog.NewTextHandler(logs, nil)))

	assert.Contains(t, logs.String(), "the server died while this turn's handler ran")
	assert.Contains(t, logs.String(), "room=a")
	assert.Contains(t, logs.String(), "after this turn's handler finished")
	assert.NotContains(t, logs.String(), "room=c")
	entries, err := ReadCrashJournal(path)
	require.NoError(t, err)
	states := map[RoomID]JournalState{}
	for i := range entries {
		states[entries[i].Room] = entries[i].State
	}
	assert.Equal(t, map[RoomID]JournalState{"a": JournalCrashed, "b": JournalCrashedAfter, "c": JournalDone}, states)
	require.NoError(t, second.close())

	again := &lockedBuffer{}
	testJournal(t, cfg, slog.New(slog.NewTextHandler(again, nil)))
	assert.NotContains(t, again.String(), "the server died", "each crash is reported once")
}

func TestAJournalOfAnotherSizeIsReportedThenStartedOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	first := testJournal(t, JournalConfig{Path: path, Entries: 8, EntrySize: 512}, nil)
	write(first, &JournalEntry{Room: "a", Module: "deck"})
	require.NoError(t, first.close())

	logs := &lockedBuffer{}
	testJournal(t, JournalConfig{Path: path, Entries: 16, EntrySize: 512}, slog.New(slog.NewTextHandler(logs, nil)))

	assert.Contains(t, logs.String(), "room=a", "the crash it showed isn't lost")
	entries, err := ReadCrashJournal(path)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestAFileThatIsntAJournalIsLeftAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("my notes"), 0o600))

	_, err := openJournal(JournalConfig{Path: path, Entries: 2, EntrySize: 512}, slog.New(slog.DiscardHandler))

	require.ErrorContains(t, err, "isn't a crash journal")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "my notes", string(data))
}

func TestManyRoomsWriteTheJournalAtOnce(t *testing.T) {
	const writers, each = 8, 200
	path := filepath.Join(t.TempDir(), "journal")
	j := testJournal(t, JournalConfig{Path: path, Entries: writers * each, EntrySize: 512}, nil)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			var buf []byte
			for range each {
				e := JournalEntry{Room: RoomID(strings.Repeat("r", w+1)), Module: "deck"}
				buf = j.begin(buf, &e)
				j.mark(e.Seq%j.slots, e.Seq, JournalHandled)
				j.mark(e.Seq%j.slots, e.Seq, JournalDone)
			}
		})
	}
	wg.Wait()

	entries, err := ReadCrashJournal(path)
	require.NoError(t, err)
	require.Len(t, entries, writers*each)
	for i := range entries {
		assert.Equal(t, uint64(i+1), entries[i].Seq)
		assert.Equal(t, JournalDone, entries[i].State)
	}
}

func TestRoomsJournalEachTurnAndHowItEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 2 * time.Second
		path := filepath.Join(t.TempDir(), "journal")
		j := testJournal(t, JournalConfig{Path: path, Entries: 64, EntrySize: 512}, nil)
		release := make(chan struct{})
		var releaseOnce sync.Once
		t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
		module := registerFunc{name: "m", register: func(r *Registry) {
			Handle(r, msgAdd, func(rc *RoomContext, _ *addMsg) error {
				rc.After(time.Minute, wrapperspb.String("tick"))
				return nil
			})
			Handle(r, msgFail, func(_ *RoomContext, _ *addMsg) error { panic("boom") })
			Handle(r, msgBlock, func(_ *RoomContext, _ *addMsg) error {
				<-release
				return nil
			})
			On(r, func(_ *RoomContext, _ *wrapperspb.StringValue) {})
		}}
		rt := newConfiguredRuntime(t, Config{StuckTimeout: timeout})
		rt.useJournal(j)
		typ := newTestRoomType(t, RoomConfig{Modules: []Module{module}})

		played, err := rt.CreateRoom(typ)
		require.NoError(t, err)
		require.NoError(t, dispatchAndWait(t, played, "m", msgAdd, &addMsg{Value: 7}))
		time.Sleep(time.Minute) // the timer's turn
		synctest.Wait()

		crashed, err := rt.CreateRoom(typ)
		require.NoError(t, err)
		require.NoError(t, crashed.dispatch("bob", "m", msgFail, mustMarshal(t, &addMsg{}), nil))
		waitFor(t, crashed.Done())

		stuck, err := rt.CreateRoom(typ)
		require.NoError(t, err)
		require.NoError(t, stuck.dispatch("carol", "m", msgBlock, mustMarshal(t, &addMsg{}), nil))
		time.Sleep(timeout)
		synctest.Wait()

		entries, err := ReadCrashJournal(path)
		require.NoError(t, err)
		require.Len(t, entries, 4)
		assert.Equal(t, JournalDone, entries[0].State)
		assert.Equal(t, msgAdd, entries[0].MessageID)
		assert.Equal(t, mustMarshal(t, &addMsg{Value: 7}), entries[0].Payload, "the full message, to replay it")
		assert.Equal(t, "google.protobuf.StringValue", entries[1].Event, "timer turns too")
		assert.Equal(t, JournalDone, entries[1].State)
		assert.Equal(t, JournalPanicked, entries[2].State)
		assert.Equal(t, PlayerID("bob"), entries[2].Player)
		assert.Equal(t, JournalStuck, entries[3].State)
		assert.Equal(t, PlayerID("carol"), entries[3].Player)
	})
}
