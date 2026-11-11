# Deploy

playhold is a library, so what runs on a server is the game's own program. This page lists what that program needs
from whatever starts it, a container platform, a service manager or a shell, and what to back up. The guides it links
to cover each setting.

## Ports

- The game listens on `:443` with TLS and on `:8080` without, unless `ListenConfig.Addr` says otherwise. The players'
  connections and the health checks share it.
- Automatic certificates answer Let's Encrypt's challenge on the TLS port itself, so nothing needs to listen on port
  80. See [TLS](./production.md#tls).
- The Prometheus metrics have a listener of their own, on `127.0.0.1:9464` by default, so only the machine itself can
  read them. An address other machines can reach logs a warning outside development, and `Metrics.Off` serves none.
  See
  [Watch the server](./observability.md).

## Starting and stopping

- `ListenAndServe` stops when its context ends: in the `run` function of [Run in production](./production.md), on
  SIGINT or SIGTERM. It drains, saves every room, and only then disconnects players, who reconnect to the next server.
  See [Health checks, drain and shutdown](./keeping-games-running.md#health-checks-drain-and-shutdown).
- Saving may take up to `ListenConfig.ShutdownTimeout`, 8 seconds by default. Whatever stops the program must wait
  longer than that before killing it.
- Something has to start the program again after it dies, so the server, with a store, restores its rooms. With
  `RestartWhenStuck`, the server also exits on purpose, with `ErrRestartNeeded`, for that same restart. See
  [Restart when stuck](./keeping-games-running.md#restart-when-stuck).

## Health checks

- `/healthz` always answers 200 while the process is up: use it to decide whether to restart the program.
- `/readyz` answers 503 while the server drains or can't reach its store: use it to decide whether to send it players.
  Restarting the program wouldn't fix either, so don't restart on `/readyz`.

## Memory

With `MemoryLimit` left at zero, the server sets Go's soft memory limit to 80% of the memory it can use: the machine's
total, from `/proc/meminfo`, or the container's limit, from `/sys/fs/cgroup/memory.max` or
`/sys/fs/cgroup/memory/memory.limit_in_bytes`, whichever is lower. Where it can read neither, as outside Linux, it sets
no limit and logs a warning. A `GOMEMLIMIT` environment variable always wins, and a negative `MemoryLimit` leaves the
limit alone. So a container's memory limit is all the server needs to stay within it.

## Folders that must outlast the program

Each only if the server uses it. On a container platform, these go on a volume.

- **`ListenConfig.AutocertCacheDir`**, for automatic certificates, so a restart doesn't ask Let's Encrypt for new
  ones.
- **`CrashJournal.Path`**, a file of about `Entries` times `EntrySize`, 4 MiB by default. Its folder must exist; the
  server creates the file, readable only by its own user. See [The crash journal](./crashes.md#the-crash-journal).
- **`Recording.Dir`**. The server makes a folder in it for the room type, readable only by its own user, and keeps the
  recordings under `MaxSize`, 10 GiB by default. See [Record and replay](./recording.md).

Recordings hold players' messages in full, and the crash journal holds them up to `EntrySize`: keep both, and any copy,
as private as the database.
See [What playhold keeps about players](./privacy.md).

## PostgreSQL

- `postgres.Open` connects and brings the store's tables up to date. If another server is updating them at the same
  time, it waits for that, for up to 5 minutes.
- `NewServer` then restores every saved room, and fails if it can't read them, so the database has to be reachable
  when the server starts.
- While the server runs, `/readyz` answers 503 whenever the database can't be reached.

## Backups

Back up the database; the crash journal and recordings aren't needed to restore games. What each table holds, and what
losing it costs:

- **`guests`**: each guest's ID, a hash of their secret, when they were created and when last seen. Without it, every
  player starts again as a new guest, and loses their seat in the games they were playing.
- **`rooms`**: each running room's saved state, with its code and room type. Without it, every game in progress is
  gone. Finished games aren't kept anyway.
- **`incidents`**: the breaker's record of the turns that crashed the server or kept running when stuck. Without it,
  the messages it turned off come back on, and rooms that kept crashing the server are restored again. See
  [The breaker](./crashes.md#the-breaker).

A server started on a backup restores every room as it was then, so the games in it go back to that moment, and guests
created since are unknown and start again. A backup also holds everything the database does about players, for as long
as the backup is kept.

How to back up PostgreSQL is its own subject: see
[Backup and restore](https://www.postgresql.org/docs/current/backup.html) in PostgreSQL's documentation.
