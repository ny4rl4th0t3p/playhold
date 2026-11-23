# 7. A second module

A room is built from modules, and so far it has one. This chapter adds a second, which keeps the score across games
and starts another game once one is over. It never imports the board: it hears each result through an event, and asks
for a new game through the board's keeper. That's how modules grow into a game without depending on each other.

The finished code is `chapter7/` in
[playhold-tictactoe](https://github.com/ny4rl4th0t3p/playhold-tictactoe). Its Go paths start with
`github.com/ny4rl4th0t3p/playhold-tictactoe/chapter7`; in your module, they're `example.com/tictactoe`.

## The score's messages

The score has messages of its own, in a second `.proto` file of the same package: a rematch, the score as each player
sees it, and how it's saved. Message IDs belong to their module, so the score's can start at 1 again:

<<< ../.tictactoe/chapter7/proto/tictactoe/v1/score.proto

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.75.0 generate
```

## An event for the result

When a game ends, the board tells whoever is listening. An event is any Go type, emitted with `rc.Emit` and heard with
`playhold.On`, matched by its exact type. It lives in a package of its own, `events/`, which both modules import, so
neither imports the other:

<<< ../.tictactoe/chapter7/events/events.go#game-ended

Only a timer's event has to be a Protobuf message, because it's saved with the room. This one is heard in the same turn
it's emitted, so it's never saved.

The board emits it wherever a game ends, by a line, a draw or the clock:

<<< ../.tictactoe/chapter7/tictactoe.go#end

## A keeper for the board

A module that needs to call another declares what it needs as an interface: here, whether the game is over, and a new
game. The score's package says so, in its own terms:

<<< ../.tictactoe/chapter7/score/score.go#keeper

The board provides it with its `Keeper`, a type with no state of its own: each call reaches the room's state through
the board's key, which only the board's package holds.

<<< ../.tictactoe/chapter7/tictactoe.go#keeper

## The score module

The module takes the keeper as a field, and `r.Needs` declares it. Building the room type fails if the keeper is nil,
or if the board isn't one of the room's modules, so a wiring mistake stops the server at startup:

<<< ../.tictactoe/chapter7/score/score.go#module

It counts each result by seat, in the order the players sat down:

<<< ../.tictactoe/chapter7/score/score.go#game-ended

A rematch goes through the keeper, and only once the game is over:

<<< ../.tictactoe/chapter7/score/score.go#rematch

Each player sees the score from their side. A view can return nil to show nothing, as it does until both players have
sat down:

<<< ../.tictactoe/chapter7/score/score.go#view

The score is saved too, at its own version 1: each module's snapshot has its own version. A server with a store refuses
a module's state with no snapshot.

::: details The whole of score/score.go
<<< ../.tictactoe/chapter7/score/score.go
:::

## Wire them together

The room type is the only place that knows both modules. It hands the board's keeper to the score:

<<< ../.tictactoe/chapter7/tictactoe.go#room-type

Events reach the modules in this list's order. More on modules, keepers and events:
[Modules, keepers and events](../concepts/modules.md).

## Test them together

The tests play through both modules, as players would, with the score's own message:

<<< ../.tictactoe/chapter7/score_test.go#test

The chapter's other tests check that a rematch waits for the game to end, that draws and timeouts count, that the score
survives `Room.Restart`, and that a player alone has no score.

```bash
go test ./...
```

## The page

Each state the page gets has one view per module, so the score is one more view, next to the board's. A rematch is one
more message, to the score module:

<<< ../.tictactoe/chapter7/web/src/main.ts#score

`index.html` gets a line for the score, `<p id="score">`, and a **Play again** button, `<button id="rematch" hidden>`,
under the board. The page shows the button once the board's status says the game is over.

## Play it

As in [chapter 6](./time.md), with PostgreSQL running:

```bash
DATABASE_URL='postgres://postgres:tictactoe@localhost:5432/postgres?sslmode=disable' go run ./server
```

```bash
npm --prefix web run dev
```

Win a game, choose **Play again**, and win or lose another: the score keeps count from each player's side, and a
restart keeps it.

## Next

The game is complete. For a client in Godot instead of the browser, see [A Godot client](./a-godot-client.md).
[A bonus page](./for-players.md), which you can skip, runs it for players: over TLS, out of development mode, with the
page on a host of its own.