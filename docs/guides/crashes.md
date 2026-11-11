# When something crashes

A panic only closes its room, which, with a store, comes back from its last save. Some failures take the whole server
down: a fatal error that kills the process, such as a stack overflow from endless recursion, or a handler that never
returns and keeps running after the watchdog closed its room. This
page covers the crash journal, which shows what was running when that happened, and the breaker, which stops the same
message from doing it again and again.

## The crash journal

With `CrashJournal.Path` set, each room writes the message or timer that starts a turn to the journal before running it,
then marks how far the turn got. After a crash, even one that kills the process, the journal shows the last turns and
which were still running. With the room's last save, that's what it takes to reproduce the crash.

<<< ../../example_test.go#crash-journal

The journal is one file of fixed size, `Entries` times `EntrySize`: 1,024 entries of 4 KiB by default, about 4 MB. The
oldest entries are overwritten, so it never grows, and a busy server overwrites them quickly: raise `Entries` to keep a
crash's entry long enough to copy. A longer payload is cut, and the entry still records its full size.

It's written to the operating system's cache, with no sync to disk, so it's cheap per turn and survives the process
dying. A whole machine failing can lose its last entries.

It holds players' messages, so keep it as private as the database. The file is readable only by the server's user.

### What it shows

Each turn ends in one of these states:

| State                    | Meaning                                                                           |
|--------------------------|-----------------------------------------------------------------------------------|
| `running`                | The turn's handler started, and nothing more was written                          |
| `handled`                | The handler finished, and the turn was saving or sending views                    |
| `done`                   | The turn finished                                                                 |
| `panicked`               | The turn panicked, and the room was restored from its last save or closed         |
| `stuck`                  | The watchdog gave up on the turn                                                  |
| `crashed`                | The turn was running when the process died, found at the next start               |
| `crashed after handling` | The process died after the turn's handler finished, while saving or sending views |
| `unknown`                | A newer turn reused the entry before this one's state was written                 |

At its next start, the server logs each turn that was running when it died, once. A `crashed` turn is the likeliest
cause.

### Read it

Copy the file, then print it with `playholdjournal`, oldest turn first, one line per turn, payloads in base64. From a
clone of playhold:

```bash
go run ./cmd/playholdjournal path/to/journal
```

`ReadCrashJournal` reads it from Go, to replay a
message in a test.

## The breaker

An incident is a turn that hurt the whole server:

- it was `crashed` in the journal: the process died while its handler ran;
- or its room got stuck, and its handler was still running 30 seconds after the watchdog gave up on it.

A panic isn't an incident, since only its room is affected. Incidents are saved in the store, so they're still counted
after the restart a crash causes. Without a store, they last only until the process ends.

**A message that keeps doing it is turned off.** Three incidents from the same player message within 10 minutes turn it
off for an hour after the last one. The server answers it with a `TURNED_OFF` error before it reaches the room, logs
`message turned off` once, and tries it again after the hour. Other messages still work. Without this, a message that
kills the process would keep the server down for every game, restart after restart.

**A room that keeps doing it isn't restored.** A room with two incidents in the 10 minutes before a start isn't
restored, so a timer that kills the process can't keep the server down either. The room stays in the store and is
logged. A later start brings it back, with a new build or once its incidents are older than 10 minutes. Meanwhile its
code stays taken: no new room gets it, and joining it is refused with `ROOM_NOT_FOUND`. With `AllowClientCodes`, joining
it starts a new game instead, and at the next start the newest saved room keeps the code and the older one is deleted.

A new build of the server starts with no incidents: the build is identified by a hash of the server's executable.
Incidents are also deleted after 7 days.

### Turn a message back on

To clear the incidents without a new build, delete them, then restart the server, which reads them only when it starts.
With the PostgreSQL store:

```sql
DELETE
FROM incidents;
```

To clear one message only, add `WHERE module = 'deck' AND message_id = 3`, with the module and message the log named.