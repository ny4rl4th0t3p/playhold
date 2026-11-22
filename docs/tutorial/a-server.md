# 3. A server

So far the game has only run inside tests. This chapter serves it: a program that takes connections, gives each
player a guest identity, and puts them in rooms. Then a test plays a game against it over real connections, through
the Go SDK, as any client would.

The finished code is `chapter3/` in
[playhold-tictactoe](https://github.com/ny4rl4th0t3p/playhold-tictactoe). Its paths start with
`github.com/ny4rl4th0t3p/playhold-tictactoe/chapter3`; in your module, they're `example.com/tictactoe`.

## The server

A server is the room type, a runtime that runs the rooms, and `ListenAndServe`. It goes in its own program,
`server/main.go`:

<<< ../.tictactoe/chapter3/server/main.go#server

- `playhold.NewRuntime` runs the rooms, each on a goroutine of its own. `playhold.Config{}` takes the defaults.
- `Development` turns off the checks that keep a public server safe, so it serves plain HTTP on your own machine. A
  server players reach needs TLS: see [Run in production](../guides/production.md).
- `ListenAndServe` serves until its context ends, here on Ctrl+C, and then stops without losing a game. With an empty
  `ListenConfig`, it listens on port 8080, and clients connect at `/ws`.

::: details The whole of server/main.go
<<< ../.tictactoe/chapter3/server/main.go
:::

```bash
go run ./server
```

It logs that development mode is on, then where it listens. There's nothing to open in a browser yet: that's
[chapter 4](./a-page.md). Stop it with Ctrl+C.

## How a game starts

A client connects and says hello, and the server answers with a guest identity, which the client keeps to come back as
the same player. To play together, one player asks for a new room, and the server makes it a code. The other joins
with that code, which the first shares, such as in a link. The board module's rules are the same as in the tests: once
two players are seated, X plays first.

See [Players and connections](../concepts/players.md) and [Find a game](../guides/finding-a-game.md).

## Play it over the network

The Go SDK is a client for programs, such as bots and tests. Add it, at the version that goes with playhold's:

```bash
go get github.com/ny4rl4th0t3p/playhold-sdk-go@v0.5.0
```

The test starts the game's server on a port of its own, connects two players, and plays a move:

<<< ../.tictactoe/chapter3/server_test.go#test

- `CreateRoom` asks for a new room, and returns the code the server made. `Join` enters a room with its code.
- `Send` sends a move to a module, by name and message ID: the same `"board"` and `MsgPlay` as the module's own.
- `Next` waits for the player's next views. `waitFor` decodes the board's until it shows what the test waits for, since a
  view arrives after every turn, including the other player's.

A move the game refuses comes back to its sender as an error, with the code `REJECTED`, the handler's own text, and
the number of the frame it's about. The chapter's second test checks that.

::: details The whole of server_test.go
<<< ../.tictactoe/chapter3/server_test.go
:::

```bash
go test ./...
```

More on the Go SDK: [Go SDK](../reference/sdk-go.md).

## Next

Two people can't play yet without writing Go. [Chapter 4](./a-page.md) gives the game a page in the browser.