# 4. A page

This chapter gives the game a page in the browser, on the TypeScript SDK: one player starts a game and sends its link,
the other opens it, and they play. The game's server serves the page too.

The finished code is `chapter4/` in
[playhold-tictactoe](https://github.com/ny4rl4th0t3p/playhold-tictactoe). Its Go paths start with
`github.com/ny4rl4th0t3p/playhold-tictactoe/chapter4`; in your module, they're `example.com/tictactoe`. This chapter
also needs Node 24 or later, and `git`, which npm uses to fetch the SDK.

## The page's packages

The page lives in `web/`, next to the Go code. Its `package.json` lists the TypeScript SDK, installed from its own
repository by tag, at the version that goes with playhold's, and protobuf-es, which decodes and encodes the game's
messages:

<<< ../.tictactoe/chapter4/web/package.json

[Vite](https://vite.dev) builds the page. The SDK ships its TypeScript source, so the page's build compiles it along
with the page's own code. `tsconfig.json` sets up TypeScript for that:

::: details web/tsconfig.json
<<< ../.tictactoe/chapter4/web/tsconfig.json
:::

```bash
npm --prefix web install
```

## The messages, in TypeScript

The page uses the same `.proto` as the server. `buf.gen.yaml` gains a second plugin, protobuf-es's, from the page's
packages, which writes TypeScript into `web/src/gen`:

<<< ../.tictactoe/chapter4/buf.gen.yaml

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.75.0 generate
```

That writes `web/src/gen/tictactoe/v1/tictactoe_pb.ts`, with `PlaySchema`, `BoardViewSchema`, and the `Message`, `Mark`
and `Status` enums, the same numbers as the Go side.

## The page

`index.html` is a button for a new game, a line for the link to send, a status, and an empty board:

::: details web/index.html
<<< ../.tictactoe/chapter4/web/index.html
:::

`web/src/main.ts` makes one client for the page. It connects to the server that served the page, at `/ws`:

<<< ../.tictactoe/chapter4/web/src/main.ts#client

- `onStatus` reports the connection: connecting, connected, joined, or lost and reconnecting. The SDK reconnects by
  itself after a dropped connection, and joins the same room again.
- `onJoined` gets the room's code, once the client is in the room.
- `onState` gets everything the player can see in the room, one view per module, after every turn.
- `onError` gets a refused move's error, with the handler's own text.

The board's view is one of those views, decoded with protobuf-es:

<<< ../.tictactoe/chapter4/web/src/main.ts#views

A cell can only be clicked on the player's turn, and when it's empty, but that's only for the player's convenience:
the server checks every move again, and a page someone changed can't play out of turn.

A click sends a move, with the move's ID from the `.proto`. A new game asks the server for a room, and the page puts the
code it gets in a link to itself, for the other player to open:

<<< ../.tictactoe/chapter4/web/src/main.ts#moves

The texts for each status are in the `text` region of the whole file.

::: details The whole of web/src/main.ts
<<< ../.tictactoe/chapter4/web/src/main.ts
:::

## Serve it

The server serves the built page, from `web/dist`, on every path but `/ws`, through `ListenConfig.Handler`. The page
then reaches the game at the address it came from:

<<< ../.tictactoe/chapter4/server/main.go#server

## Play it

From the module's folder:

```bash
npm --prefix web run build
go run ./server
```

1. Open `http://localhost:8080`, and choose **New game**.
2. Open the game's link in a private window or another browser. Two tabs of the same browser share one guest identity,
   so the newer would take the older's place.
3. Play.

Stop the server in the middle of a game, and start it again: the page reconnects by itself, but the game is gone. A
server with no store keeps its rooms and guests only in memory.

## Next

Chapter 5 saves the game, so it survives the server stopping, and gives each turn a time limit.
