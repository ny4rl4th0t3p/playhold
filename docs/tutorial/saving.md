# 5. Saving

So far a game lives only in the server's memory, and stopping the server loses it. This chapter saves the board in
PostgreSQL, so a game survives a restart, a crash or a deploy, and the players find it as they left it.

The finished code is `chapter5/` in
[playhold-tictactoe](https://github.com/ny4rl4th0t3p/playhold-tictactoe). Its Go paths start with
`github.com/ny4rl4th0t3p/playhold-tictactoe/chapter5`; in your module, they're `example.com/tictactoe`. This chapter
also needs Docker with Compose, for PostgreSQL.

## What's saved

The server saves a room with its seats and timers, and asks each module for its own state, as a Protobuf message. The
board's state is a plain Go struct, so it gets a message of its own, next to the others in the `.proto`:

<<< ../.tictactoe/chapter5/proto/tictactoe/v1/tictactoe.proto#saved

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.75.0 generate
```

## The snapshot

`playhold.Snapshot` declares how the module's state is saved: a version, which starts at 1, and two functions, one
each way:

<<< ../.tictactoe/chapter5/tictactoe.go#register

<<< ../.tictactoe/chapter5/tictactoe.go#snapshot

- `save` runs after every change to the room, and `load` when the server restores it. The state itself stays a plain
  struct; only the saved form is Protobuf.
- The version says which shape the saved bytes have. A later change to what's saved raises it, and tells the server
  how to turn games saved at the old version into the new one. That's chapter 6.
- A server with a store refuses to start if a module's state has no snapshot, so a game can't quietly lose part of
  itself at a restart.

More on snapshots: [Saving and timers](../concepts/saving.md).

## Test a restart

`Room.Restart` in `playholdtest` saves the room as a server stopping would, and brings it back from that save, as the
next server would. Everyone is disconnected, and joins again:

<<< ../.tictactoe/chapter5/tictactoe_test.go#restart

The chapter's second test does the same with a finished game, which comes back finished.

```bash
go test ./...
```

## A store for the server

The server opens PostgreSQL with `postgres.Open`, from playhold's `store/postgres` package, when `DATABASE_URL` is set,
and gives it to the server as its `Store`:

<<< ../.tictactoe/chapter5/server/main.go#server

- `postgres.Open` creates the store's tables, or brings them up to date.
- With a store, `NewServer` restores every saved room before it serves, and `ListenAndServe` saves every room before it
  returns, so `records.Close` comes after it.
- Without `DATABASE_URL`, the server runs as in chapter 4, and says so in its log: games live in memory only.

PostgreSQL runs in Docker Compose, on this machine only:

<<< ../.tictactoe/chapter5/compose.yaml

The module needs the store's PostgreSQL driver now, so tidy it:

```bash
go mod tidy
```

## Play it, and stop it

From the module's folder:

```bash
docker compose up -d
DATABASE_URL='postgres://postgres:tictactoe@localhost:5432/postgres?sslmode=disable' go run ./server
```

```bash
npm --prefix web run dev
```

The server logs `saving games in PostgreSQL`. Start a game in two browsers, as in [chapter 4](./a-page.md#play-it),
and make a few moves. Then stop the server with Ctrl+C, and start it again with the same command: both pages reconnect
by themselves, to the same board, with the same player to move.

To see what's saved, ask PostgreSQL. There's a row per room, with its code, and `updated_at` moves on with every move:

```bash
docker compose exec postgres psql -U postgres -c 'SELECT code, room_type, length(data), updated_at FROM rooms'
```

::: tip "No room has this code" after a restart
The server was started without `DATABASE_URL`, so it saved nothing: its log says `no DATABASE_URL`. Start it with the
variable, and start a new game.
:::

What each table holds, and what to back up: [Deploy](../guides/deploy.md#backups).

## Next

Chapter 6 gives each turn a time limit, which changes what the board saves, so it raises the snapshot's version and
moves games saved at version 1 to it.