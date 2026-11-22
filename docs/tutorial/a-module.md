# 1. A module

This tutorial builds tic-tac-toe on playhold, one idea at a time, from an empty folder to a game two people play in
their browsers. Each chapter ends with code that builds and passes its tests, and each chapter's finished code is in
[playhold-tictactoe](https://github.com/ny4rl4th0t3p/playhold-tictactoe), in a folder of its own, to compare yours with.

This chapter writes the game's rules as a module, and tests them, with no server yet. It assumes Go 1.27.4 or later,
and helps if you've read [Modules, keepers and events](../concepts/modules.md).

## Start a module

```bash
mkdir tictactoe && cd tictactoe
go mod init example.com/tictactoe
go get github.com/ny4rl4th0t3p/playhold@v0.5.0 google.golang.org/protobuf github.com/stretchr/testify
```

Everything in this chapter goes in one package, `tictactoe`, in two files: `tictactoe.go` and `tictactoe_test.go`.

The code on these pages comes from the tutorial's repository, so its imports of the game's own packages start with
`github.com/ny4rl4th0t3p/playhold-tictactoe/chapter1`. In your module, that's `example.com/tictactoe`.

## The state

A module keeps its state in a plain Go struct, reached through a key. The board is nine cells, each `X`, `O` or empty,
and how many moves have been made, which says whose turn it is:

<<< ../.tictactoe/chapter1/tictactoe.go#state

- `playhold.NewKey` makes the key, once, in a package-level variable. It stays unexported, so only this module can reach
  the board.
- `MsgPlay` is the module's one message, by an ID local to the module. Its payload is the cell to mark.
- The errors are what a refused move answers. Only the player who tried it hears it.

## The module

A module has a name, unique in its room type, and a `Register` method that declares everything it does:

<<< ../.tictactoe/chapter1/tictactoe.go#register

`playhold.State` gives each room its own board, `playhold.Handle` says which function handles `MsgPlay`, and
`playhold.View` declares what each player sees.

## A move

A handler gets the room's context and the message, already decoded. This chapter uses protobuf's ready-made
`Int32Value` for the cell, so it needs no `.proto` file yet:

<<< ../.tictactoe/chapter1/tictactoe.go#play

- `rc.Player()` is who sent the move, and `rc.Players()` the room's seated players, in the order they arrived: the first
  plays X.
- Every check comes before anything changes. A handler that returns an error changes nothing only if it hasn't changed
  anything yet.
- The room runs one turn at a time, so there's no lock: nothing else touches the board while `play` runs.

## What each player sees

A player never receives the module's state, only its view, built for them alone. Here it's the board, with a `.` for
each empty cell, and how the game stands for that player:

<<< ../.tictactoe/chapter1/tictactoe.go#view

Both players see the same board, but not the same status: the view of the player whose turn it is says `your turn`,
the other's `their turn`. A view can show each player something different, which is how a card game hides each
player's hand. See [Views](../concepts/views.md).

## The room type

The game is a room type: rooms of two players, each with a board.

<<< ../.tictactoe/chapter1/tictactoe.go#room-type

`NewRoomType` checks how the modules fit together and fails at startup on a mistake, instead of in the middle of a game.

::: details The whole of tictactoe.go
<<< ../.tictactoe/chapter1/tictactoe.go
:::

## Test it

`playholdtest` runs a room of the game in a test, with no server and no network: players join, send moves, and read
their views. A test runs inside `playholdtest.Run`, where every call returns once the room has nothing left to do:

<<< ../.tictactoe/chapter1/tictactoe_test.go#test

```bash
go test ./...
```

The chapter's tests also play a win, a draw, every refused move, and a player alone. See
[Test a game](../guides/testing.md).

::: details The whole of tictactoe_test.go
<<< ../.tictactoe/chapter1/tictactoe_test.go
:::

## Check it stays reproducible

A room's random numbers and clock come from the room, so the same moves always give the same game. playhold's
`playholdvet` flags game code that would break that, such as reading the real clock. Add it as a tool of the module,
on the same playhold version, and run it with `go vet`:

```bash
go get -tool github.com/ny4rl4th0t3p/playhold/cmd/playholdvet
go vet -vettool=$(go tool -n playholdvet) ./...
```

## Next

The view is a string a page would have to pick apart. [Chapter 2](./messages.md) gives the game messages of its own.