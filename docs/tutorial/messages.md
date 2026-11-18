# 2. Its own messages

Chapter 1's move was protobuf's `Int32Value`, and its view a string like `....X.... your turn`. Fine for a test, but a
page would have to pick the string apart, and nothing would say which ID the move has. This chapter gives the game
messages of its own, in a `.proto` file, with Go code generated from it. The rules don't change.

The finished code is `chapter2/` in
[playhold-tictactoe](https://github.com/ny4rl4th0t3p/playhold-tictactoe). Its paths start with
`github.com/ny4rl4th0t3p/playhold-tictactoe/chapter2`; in your module, they're `example.com/tictactoe`.

## The messages

The game's messages go in `proto/tictactoe/v1/tictactoe.proto`. First the move, and the IDs of the board's messages:

<<< ../.tictactoe/chapter2/proto/tictactoe/v1/tictactoe.proto#messages

The IDs are an enum, so any client generated from this file uses the same numbers as the server. Then what each player
sees:

<<< ../.tictactoe/chapter2/proto/tictactoe/v1/tictactoe.proto#view

A cell's zero value, `MARK_UNSPECIFIED`, is an empty cell, so a new board needs nothing set.

::: details The whole of tictactoe.proto
<<< ../.tictactoe/chapter2/proto/tictactoe/v1/tictactoe.proto
:::

Its `go_package` option says which Go package the generated code goes in. In yours, that's `example.com/tictactoe/pb;pb`.

## Generate the Go code

[buf](https://buf.build) generates it. Two files in the module's folder configure it. `buf.yaml` says where the `.proto`
files are, and checks them against buf's standard rules:

<<< ../.tictactoe/chapter2/buf.yaml

`buf.gen.yaml` says what to generate. Here it's Go, with protobuf's own plugin, at the same version playhold uses. In
yours, `module=` is `example.com/tictactoe`, so the code lands in `pb/`:

<<< ../.tictactoe/chapter2/buf.gen.yaml

Then, with buf run through Go, so there's nothing to install:

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.75.0 lint
go run github.com/bufbuild/buf/cmd/buf@v1.75.0 generate
```

That writes `pb/tictactoe.pb.go`. Run `generate` again after every change to the `.proto`, and never edit the
generated file.

## The module, on its own messages

The state stays a plain Go struct. Only its types change: a cell is now a `pb.Mark`, and the move's ID comes from the
`.proto`:

<<< ../.tictactoe/chapter2/tictactoe.go#state

The handler takes the game's own `*pb.Play`. `playhold.Handle` decodes the payload into it, and a payload that doesn't
decode is refused with `BAD_MESSAGE` before the handler runs:

<<< ../.tictactoe/chapter2/tictactoe.go#play

The view returns a `*pb.BoardView`. It copies the cells instead of handing over the state's own array, so nothing the
view returns ever points into the module's state:

<<< ../.tictactoe/chapter2/tictactoe.go#view

::: details The whole of tictactoe.go
<<< ../.tictactoe/chapter2/tictactoe.go
:::

## The tests

The tests are chapter 1's, reading the view's fields instead of a string:

<<< ../.tictactoe/chapter2/tictactoe_test.go#test

```bash
go test ./...
```

::: details The whole of tictactoe_test.go
<<< ../.tictactoe/chapter2/tictactoe_test.go
:::

To see everything this chapter changed, compare the two folders in the tutorial's repository with
`diff -r chapter1 chapter2`.

## Next

The game still only runs in a test. [Chapter 3](./a-server.md) serves it.