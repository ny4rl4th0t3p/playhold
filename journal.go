package playhold

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"
)

// JournalState is how far a journaled turn got.
type JournalState string

// The states of a journaled turn.
const (
	// JournalRunning means the turn's handler started and nothing more was written: the process stopped while it ran.
	JournalRunning JournalState = "running"
	// JournalHandled means the handler and its events finished, and the turn was saving or sending views.
	JournalHandled JournalState = "handled"
	// JournalDone means the turn finished.
	JournalDone JournalState = "done"
	// JournalPanicked means the turn panicked, and the room was restored from its last save or closed.
	JournalPanicked JournalState = "panicked"
	// JournalStuck means the watchdog gave up on the turn.
	JournalStuck JournalState = "stuck"
	// JournalCrashed means the turn was running when the process died, found and reported at the next start.
	JournalCrashed JournalState = "crashed"
	// JournalCrashedAfter means the process died after the turn's handler finished, while saving or sending views.
	JournalCrashedAfter JournalState = "crashed after handling"
	// JournalUnknown means a newer turn reused the slot before this one's state was written.
	JournalUnknown JournalState = "unknown"
)

var journalStateBytes = map[JournalState]byte{
	JournalRunning: 1, JournalHandled: 2, JournalDone: 3, JournalPanicked: 4, JournalStuck: 5, JournalCrashed: 6,
	JournalCrashedAfter: 7,
}

// JournalEntry is one turn in the crash journal: the message or timer that started it, and how far it got.
type JournalEntry struct {
	Seq      uint64
	Time     time.Time // the room's clock when the turn started
	State    JournalState
	Room     RoomID
	RoomType string
	Player   PlayerID
	// Module and MessageID name a player's message. Event names the event of a timer or grace period instead.
	Module    string
	MessageID uint32
	Event     string
	// Size is the payload's full size. Payload holds only the start of it if it didn't fit in the slot.
	Size    int
	Payload []byte
}

// The journal is one file of fixed size: a header, then slots, reused in turn, so it never grows. Each slot holds:
//
//	[0:8]   the sequence number the state belongs to
//	[8]     the state
//	[12:16] CRC-32 of the body
//	[16:20] the body's length
//	[20:]   the body: the entry
//
// A state is written again as the turn goes on, with its sequence number, so a late write for a turn whose slot a newer
// turn has taken is recognized, and that newer entry reads as unknown.
const (
	journalMagic      = "playhold-journal"
	journalVersion    = 1
	journalHeaderSize = 4096
	slotHeaderSize    = 20
	maxJournalString  = 256
	journalFileMode   = 0o600 // only the server's user: it holds players' messages

	defaultJournalEntries   = 1024
	defaultJournalEntrySize = 4096
	minJournalEntrySize     = 512 // room for the fields, with some payload
	maxJournalEntrySize     = 1 << 20
	maxJournalEntries       = 1 << 24
)

// JournalConfig turns on the crash journal: before each turn, the message or timer that started it is written to a file,
// and marked as the turn goes on. After a crash, even one that kills the process, the file shows the last turns and which
// were still running; with the room's last save, that's what it takes to reproduce the crash. The file has a fixed size,
// set by Entries and EntrySize, and the oldest entries are overwritten, so it never grows. It holds players' messages, so
// keep it as private as the database.
type JournalConfig struct {
	// Path is the journal's file. Empty turns the journal off. It survives restarts only on lasting storage, such as a
	// container volume.
	Path string
	// Entries is how many turns the journal keeps. The default is 1,024. A busy server overwrites them in well under a
	// second at that size: raise it to keep a panic's entry long enough to copy, such as 100,000 for about half a minute
	// at 3,000 turns a second.
	Entries int
	// EntrySize is the space for one turn, in bytes, from 512 to 1 MiB. The default is 4 KiB. A longer payload is cut,
	// and the entry still records its full size. The file is about Entries times EntrySize.
	EntrySize int
}

func (c JournalConfig) withDefaults() (JournalConfig, error) {
	if c.Entries <= 0 {
		c.Entries = defaultJournalEntries
	}
	if c.EntrySize <= 0 {
		c.EntrySize = defaultJournalEntrySize
	}
	switch {
	case c.Entries > maxJournalEntries:
		return c, fmt.Errorf("playhold: crash journal Entries %d is over the limit of %d", c.Entries, maxJournalEntries)
	case c.EntrySize < minJournalEntrySize || c.EntrySize > maxJournalEntrySize:
		return c, fmt.Errorf("playhold: crash journal EntrySize %d is outside %d to %d bytes",
			c.EntrySize, minJournalEntrySize, maxJournalEntrySize)
	}
	return c, nil
}

// journal writes turns to the crash journal. Rooms write from their own goroutines, each to its own slot.
type journal struct {
	f        *os.File
	slots    uint64
	slotSize int
	next     atomic.Uint64 // the last sequence number given out
	logger   *slog.Logger
	failed   atomic.Bool // a write failed, which is logged once
	// crashed holds the turns that were running when the process last died, found at open, for the breaker to count.
	crashed []JournalEntry
}

// openJournal opens the journal at cfg.Path, creating it if needed, reports the turns it shows were in flight when the
// process last died, and marks them so the next start doesn't report them again. A file that isn't a journal is refused
// rather than overwritten. A journal of another size is reported, then started over.
func openJournal(cfg JournalConfig, logger *slog.Logger) (*journal, error) {
	f, err := os.OpenFile(filepath.Clean(cfg.Path), os.O_RDWR|os.O_CREATE, journalFileMode)
	if err != nil {
		return nil, fmt.Errorf("playhold: opening the crash journal: %w", err)
	}
	j := &journal{f: f, slots: uint64(cfg.Entries), slotSize: cfg.EntrySize, logger: logger} //nolint:gosec // checked
	entries, slots, slotSize, err := readJournal(f)
	switch {
	case errors.Is(err, errNotJournal):
		_ = f.Close()
		return nil, fmt.Errorf("playhold: %s isn't a crash journal; choose another path or remove it", cfg.Path)
	case errors.Is(err, errEmptyJournal):
		entries = nil
	case err != nil:
		_ = f.Close()
		return nil, err
	}
	sameLayout := err == nil && slots == cfg.Entries && slotSize == cfg.EntrySize
	j.reportCrashed(entries, sameLayout)
	if !sameLayout {
		if err := createJournal(f, cfg.Entries, cfg.EntrySize); err != nil {
			_ = f.Close()
			return nil, err
		}
		return j, nil
	}
	for i := range entries {
		j.next.Store(max(j.next.Load(), entries[i].Seq))
	}
	return j, nil
}

var (
	errNotJournal   = errors.New("not a crash journal")
	errEmptyJournal = errors.New("empty crash journal")
)

func journalHeader(slots, slotSize int) []byte {
	header := make([]byte, journalHeaderSize)
	copy(header, journalMagic)
	binary.LittleEndian.PutUint32(header[16:], journalVersion)
	binary.LittleEndian.PutUint32(header[20:], uint32(slots))    //nolint:gosec // checked against maxJournalEntries
	binary.LittleEndian.PutUint32(header[24:], uint32(slotSize)) //nolint:gosec // checked against maxJournalEntrySize
	return header
}

func createJournal(f *os.File, slots, slotSize int) error {
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("playhold: creating the crash journal: %w", err)
	}
	if err := f.Truncate(int64(journalHeaderSize + slots*slotSize)); err != nil {
		return fmt.Errorf("playhold: creating the crash journal: %w", err)
	}
	if _, err := f.WriteAt(journalHeader(slots, slotSize), 0); err != nil {
		return fmt.Errorf("playhold: creating the crash journal: %w", err)
	}
	return nil
}

// ReadCrashJournal returns the entries in the crash journal at path, oldest first. Copy the file from the server and
// read it with this, or with cmd/playholdjournal, to see the last turns before a crash: with the room's last save, they
// let you reproduce it.
func ReadCrashJournal(path string) ([]JournalEntry, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries, _, _, err := readJournal(f)
	if errors.Is(err, errNotJournal) || errors.Is(err, errEmptyJournal) {
		return nil, fmt.Errorf("playhold: %s: %w", path, err)
	}
	return entries, err
}

// readJournal returns a journal's entries, oldest first, and its layout from its header.
func readJournal(r io.ReaderAt) (entries []JournalEntry, slots, slotSize int, err error) {
	header := make([]byte, journalHeaderSize)
	if n, err := r.ReadAt(header, 0); n == 0 && errors.Is(err, io.EOF) {
		return nil, 0, 0, errEmptyJournal
	} else if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, 0, err
	}
	if !bytes.HasPrefix(header, []byte(journalMagic)) {
		return nil, 0, 0, errNotJournal
	}
	slots = int(binary.LittleEndian.Uint32(header[20:]))
	slotSize = int(binary.LittleEndian.Uint32(header[24:]))
	if binary.LittleEndian.Uint32(header[16:]) != journalVersion || slots <= 0 || slots > maxJournalEntries ||
		slotSize < minJournalEntrySize || slotSize > maxJournalEntrySize {
		return nil, 0, 0, errEmptyJournal // from another version: nothing in it can be read
	}
	slot := make([]byte, slotSize)
	for i := range slots {
		if _, err := r.ReadAt(slot, int64(journalHeaderSize)+int64(i)*int64(slotSize)); err != nil {
			return nil, 0, 0, fmt.Errorf("playhold: reading the crash journal: %w", err)
		}
		if entry, ok := parseSlot(slot); ok {
			entries = append(entries, entry)
		}
	}
	slices.SortFunc(entries, func(a, b JournalEntry) int { return compareSeq(a.Seq, b.Seq) })
	return entries, slots, slotSize, nil
}

func compareSeq(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// begin writes a turn as running, giving it its sequence number, and returns buf, which the caller keeps to reuse.
func (j *journal) begin(buf []byte, e *JournalEntry) []byte {
	e.Seq = j.next.Add(1)
	if cap(buf) < j.slotSize {
		buf = make([]byte, 0, j.slotSize)
	}
	buf = appendEntry(buf[:slotHeaderSize], e, j.slotSize)
	body := buf[slotHeaderSize:]
	binary.LittleEndian.PutUint64(buf[0:], e.Seq)
	buf[8] = journalStateBytes[JournalRunning]
	buf[9], buf[10], buf[11] = 0, 0, 0
	binary.LittleEndian.PutUint32(buf[12:], crc32.ChecksumIEEE(body))
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(body))) //nolint:gosec // at most slotSize
	j.write(buf, e.Seq%j.slots)
	return buf
}

// mark writes a turn's new state, with its sequence number.
func (j *journal) mark(slot, seq uint64, state JournalState) {
	var b [9]byte
	binary.LittleEndian.PutUint64(b[:], seq)
	b[8] = journalStateBytes[state]
	j.write(b[:], slot)
}

func (j *journal) write(b []byte, slot uint64) {
	offset := int64(journalHeaderSize) + int64(slot)*int64(j.slotSize) //nolint:gosec // slot < slots, a small number
	if _, err := j.f.WriteAt(b, offset); err != nil && j.failed.CompareAndSwap(false, true) {
		j.logger.Error("writing the crash journal failed; turns go on, but it may miss some", "error", err)
	}
}

func (j *journal) close() error { return j.f.Close() }

// appendEntry encodes the entry after buf's slot header, cutting the payload to fit in size bytes in all.
func appendEntry(buf []byte, e *JournalEntry, size int) []byte {
	buf = binary.LittleEndian.AppendUint64(buf, e.Seq)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(e.Time.UnixNano()))
	buf = binary.LittleEndian.AppendUint32(buf, e.MessageID)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(e.Size)) //nolint:gosec // at most MaxMessageBytes
	buf = appendJournalString(buf, string(e.Room))
	buf = appendJournalString(buf, e.RoomType)
	buf = appendJournalString(buf, string(e.Player))
	buf = appendJournalString(buf, e.Module)
	buf = appendJournalString(buf, e.Event)
	room := max(size-len(buf), 0)
	return append(buf, e.Payload[:min(len(e.Payload), room)]...)
}

func appendJournalString(buf []byte, s string) []byte {
	s = s[:min(len(s), maxJournalString)]
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(s))) //nolint:gosec // at most maxJournalString
	return append(buf, s...)
}

// parseSlot decodes a slot, and reports false for one never written, or cut off by a crash mid-write.
func parseSlot(slot []byte) (JournalEntry, bool) {
	markSeq := binary.LittleEndian.Uint64(slot[0:])
	state := slot[8]
	sum := binary.LittleEndian.Uint32(slot[12:])
	n := int(binary.LittleEndian.Uint32(slot[16:]))
	if state == 0 || n == 0 || slotHeaderSize+n > len(slot) {
		return JournalEntry{}, false
	}
	body := slot[slotHeaderSize : slotHeaderSize+n]
	if crc32.ChecksumIEEE(body) != sum {
		return JournalEntry{}, false
	}
	e, ok := decodeEntry(body)
	if !ok {
		return JournalEntry{}, false
	}
	e.State = JournalUnknown
	if markSeq == e.Seq {
		for name, b := range journalStateBytes {
			if b == state {
				e.State = name
			}
		}
	}
	return e, true
}

func decodeEntry(b []byte) (JournalEntry, bool) {
	const fixed = 24
	if len(b) < fixed {
		return JournalEntry{}, false
	}
	e := JournalEntry{
		Seq:       binary.LittleEndian.Uint64(b[0:]),
		Time:      time.Unix(0, int64(binary.LittleEndian.Uint64(b[8:]))), //nolint:gosec // written from a UnixNano
		MessageID: binary.LittleEndian.Uint32(b[16:]),
		Size:      int(binary.LittleEndian.Uint32(b[20:])),
	}
	rest := b[fixed:]
	var fields [5]string
	for i := range fields {
		if len(rest) < 2 {
			return JournalEntry{}, false
		}
		n := int(binary.LittleEndian.Uint16(rest))
		if len(rest) < 2+n {
			return JournalEntry{}, false
		}
		fields[i] = string(rest[2 : 2+n])
		rest = rest[2+n:]
	}
	e.Room, e.RoomType, e.Player, e.Module, e.Event = RoomID(fields[0]), fields[1], PlayerID(fields[2]), fields[3], fields[4]
	e.Payload = slices.Clone(rest)
	return e, true
}

// reportCrashed logs the turns that were in flight when the process last died, keeps those whose handler was running,
// and marks them so they're reported once, unless the file is about to start over anyway.
func (j *journal) reportCrashed(entries []JournalEntry, mark bool) {
	for i := range entries {
		e := &entries[i]
		var next JournalState
		switch e.State {
		case JournalRunning:
			next = JournalCrashed
			j.logger.Error("the server died while this turn's handler ran; it may have caused the crash",
				journalAttrs(e)...)
			j.crashed = append(j.crashed, *e)
		case JournalHandled:
			next = JournalCrashedAfter
			j.logger.Warn("the server died after this turn's handler finished, while saving or sending views",
				journalAttrs(e)...)
		default:
			continue
		}
		if mark {
			j.mark(e.Seq%j.slots, e.Seq, next)
		}
	}
}

// journalBegin writes the turn about to run to the crash journal. Callers check r.journal first, so a server without a
// journal doesn't build the entry.
func (r *Room) journalBegin(e *JournalEntry) {
	e.Time, e.Room, e.RoomType = r.now, r.id, r.typ.cfg.Name
	r.journalBuf = r.journal.begin(r.journalBuf, e)
	r.journalSeq.Store(e.Seq)
}

// journalMark writes how far the current turn got. A final state ends the turn's entry: whichever of the room's
// goroutine and the watchdog gets there first writes it, and the other writes nothing.
func (r *Room) journalMark(state JournalState, final bool) {
	if r.journal == nil {
		return
	}
	var seq uint64
	if final {
		seq = r.journalSeq.Swap(0)
	} else {
		seq = r.journalSeq.Load()
	}
	if seq != 0 {
		r.journal.mark(seq%r.journal.slots, seq, state)
	}
}

func journalAttrs(e *JournalEntry) []any {
	return []any{"seq", e.Seq, "room", string(e.Room), "room_type", e.RoomType, "player", string(e.Player),
		"module", e.Module, "message_id", e.MessageID, "event", e.Event, "size", e.Size}
}
