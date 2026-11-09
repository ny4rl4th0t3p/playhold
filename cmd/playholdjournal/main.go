// Command playholdjournal prints a crash journal, oldest turn first, one line per turn:
//
//	go run github.com/ny4rl4th0t3p/playhold/cmd/playholdjournal journal-file
//
// A turn still running or crashed is the likeliest cause of a crash. With the room's last save, its message is what it
// takes to reproduce it. Payloads are printed in base64.
package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ny4rl4th0t3p/playhold"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: playholdjournal journal-file")
		os.Exit(2)
	}
	if err := run(os.Stdout, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(w io.Writer, path string) error {
	entries, err := playhold.ReadCrashJournal(path)
	if err != nil {
		return err
	}
	for i := range entries {
		if _, err := fmt.Fprintln(w, line(&entries[i])); err != nil {
			return err
		}
	}
	return nil
}

func line(e *playhold.JournalEntry) string {
	what := fmt.Sprintf("message=%s/%d", e.Module, e.MessageID)
	if e.Event != "" {
		what = "event=" + e.Event
	}
	cut := ""
	if len(e.Payload) < e.Size {
		cut = " (cut)"
	}
	return fmt.Sprintf("%d %s state=%q room=%s room_type=%s player=%s %s size=%d%s payload=%s",
		e.Seq, e.Time.UTC().Format(time.RFC3339Nano), e.State, e.Room, e.RoomType, e.Player, what, e.Size, cut,
		base64.StdEncoding.EncodeToString(e.Payload))
}
