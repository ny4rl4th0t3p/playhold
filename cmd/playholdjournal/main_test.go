package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ny4rl4th0t3p/playhold"
)

func TestLine(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		entry playhold.JournalEntry
		want  string
	}{
		{
			name: "a message",
			entry: playhold.JournalEntry{
				Seq: 7, Time: at, State: playhold.JournalCrashed, Room: "r1", RoomType: "cards", Player: "alice",
				Module: "hands", MessageID: 2, Size: 3, Payload: []byte("abc"),
			},
			want: `7 2026-11-09T12:00:00Z state="crashed" room=r1 room_type=cards player=alice message=hands/2 size=3 payload=YWJj`,
		},
		{
			name: "a timer, with its payload cut",
			entry: playhold.JournalEntry{
				Seq: 8, Time: at, State: playhold.JournalDone, Room: "r1", RoomType: "cards",
				Event: "turns.TurnTimeout", Size: 10, Payload: []byte("ab"),
			},
			want: `8 2026-11-09T12:00:00Z state="done" room=r1 room_type=cards player= event=turns.TurnTimeout size=10 (cut) payload=YWI=`,
		},
	}
	for i := range tests {
		tt := &tests[i]
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, line(&tt.entry))
		})
	}
}

func TestRunReportsAMissingJournal(t *testing.T) {
	var out bytes.Buffer

	err := run(&out, filepath.Join(t.TempDir(), "missing"))

	require.Error(t, err)
	assert.Empty(t, out.String())
}
