package playholdtest

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/ny4rl4th0t3p/playhold"
	"github.com/ny4rl4th0t3p/playhold/internal/roomtest"
)

// Report is how a replay went.
type Report struct {
	// Turns is how many turns were replayed and ended as recorded.
	Turns int
	// Build and Revision say which build recorded the room: a hash of the server's executable, and the version control
	// revision Go stamped into it. A replay is only exact against the code of that revision.
	Build    string
	Revision string
	// SchemaMatches says the room type's modules save at the snapshot versions the recording's did.
	SchemaMatches bool
	// Diverged is the first turn that didn't end as recorded, or nil if every turn did.
	Diverged *Divergence
}

// Divergence is the first turn of a replay that didn't end as it was recorded.
type Divergence struct {
	// Segment counts from 0, one for each time the room started running, new or restored.
	Segment int
	// Turn counts from 1 within the segment; 0 means the room starting to run.
	Turn int
	// Input is the turn as recorded, empty for the start.
	Input playhold.RecordedTurn
	// Reason says what differs: the error, a panic, the room's state, or a timer that isn't pending.
	Reason string
	// WantHash and GotHash are the room's state after the turn, as recorded and as replayed, when that's what differs.
	WantHash string
	GotHash  string
	// WantError and GotError are the turn's error, as recorded and as replayed, when that's what differs.
	WantError string
	GotError  string
}

// String describes the divergence, for a test's failure.
func (d *Divergence) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "segment %d, turn %d: %s", d.Segment, d.Turn, d.Reason)
	if d.Turn > 0 {
		fmt.Fprintf(&b, "\n  input: %s", d.Input.String())
	}
	if d.WantHash != "" || d.GotHash != "" {
		fmt.Fprintf(&b, "\n  state: recorded %s, replayed %s", d.WantHash, d.GotHash)
	}
	if d.WantError != "" || d.GotError != "" {
		fmt.Fprintf(&b, "\n  error: recorded %q, replayed %q", d.WantError, d.GotError)
	}
	return b.String()
}

// Replay plays a room's recording again against typ, and fails the test at the first turn that doesn't end as it was
// recorded, with what differs and that turn's input. Run it against the code of the recording's revision. It needs no
// Run around it.
func Replay(t *testing.T, typ *playhold.RoomType, path string) *Report {
	t.Helper()
	report, err := TryReplay(t, typ, path)
	switch {
	case err != nil:
		t.Fatalf("playholdtest: replaying %s: %v", path, err)
	case report.Diverged != nil:
		t.Fatalf("playholdtest: %s doesn't replay as recorded (recorded by revision %q; schema versions match: %v)\n%s",
			path, report.Revision, report.SchemaMatches, report.Diverged)
	}
	return report
}

// TryReplay is Replay, returning the report instead of failing the test. The error is for a file it can't read.
func TryReplay(t *testing.T, typ *playhold.RoomType, path string) (*Report, error) {
	t.Helper()
	got, err := roomtest.Replay(typ, path, slog.New(slog.NewTextHandler(t.Output(), nil)))
	if err != nil {
		return nil, err
	}
	report := &Report{Turns: got.Turns, Build: got.Build, Revision: got.Revision, SchemaMatches: got.SchemaMatches}
	if d := got.Diverged; d != nil {
		input, _ := d.Input.(playhold.RecordedTurn) // the zero turn for the start
		report.Diverged = &Divergence{
			Segment: d.Segment, Turn: d.Turn, Input: input, Reason: d.Reason,
			WantHash: d.WantHash, GotHash: d.GotHash, WantError: d.WantError, GotError: d.GotError,
		}
	}
	return report, nil
}
