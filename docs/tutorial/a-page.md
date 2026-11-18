# 4. A page

This chapter gives the game a page in the browser, on the TypeScript SDK: one player starts a game and sends its link,
the other opens it, and they play. The page is hosted on its own, not by the game's server.

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

`web/src/main.ts` makes one client for the page. The server's address is set when the page is built, with
`VITE_SERVER_URL`, or by default port 8080 of the machine the page came from:

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

## Let the page in

The page comes from Vite's development server, at `localhost:5173`, and the game's server is at port 8080: another
address. Browsers say which page opened a connection, and the server refuses a page it doesn't know, development mode
included, so it lists the page's address in `OriginPatterns`. The addresses come from the `ORIGINS` environment
variable, so no deploy's address is written in the code, and by default it's Vite's on this machine:

<<< ../.tictactoe/chapter4/server/main.go#server

On a public server, `ORIGINS` is the domain that hosts the page, such as `game.example`. See
[Web origins](../guides/production.md#web-origins).

::: tip "Connection lost. Reconnecting…" in every browser
The server is refusing the page's address. Opened from another machine, the page's address isn't `localhost:5173`:
start Vite with `npm --prefix web run dev -- --host`, and the server with the address Vite shows, such as
`ORIGINS=192.168.1.20:5173 go run ./server`.
:::

## Why the page isn't served by the game server

`ListenConfig.Handler` could serve the built page from the game's own server, but don't: host the page on its own,
here on Vite's development server, and for players on any static host, a CDN or a site such as itch.io.

- The page is files that never change between releases, which a static host serves faster and closer to each player.
- The game's server then does one job, the rooms, and keeps its CPU, memory and connections for them.
- Each is deployed on its own: a new page doesn't restart the server, which disconnects every player until they're
  back, and a new server doesn't need a new page.

For a public page, build it with the server's address, such as `VITE_SERVER_URL=wss://game.example/ws npm --prefix web
run build`, and upload `web/dist`.

## Play it

From the module's folder, in two terminals:

```bash
go run ./server
```

```bash
npm --prefix web run dev
```

1. Open `http://localhost:5173`, and choose **New game**.
2. Open the game's link in a private window or another browser. Two tabs of the same browser share one guest identity,
   so the newer would take the older's place.
3. Play. Vite reloads the page each time you save a change to it.

To play from another machine on your network, see the tip in [Let the page in](#let-the-page-in).

Stop the server in the middle of a game, and start it again: the page reconnects by itself, but the game is gone. A
server with no store keeps its rooms and guests only in memory.

## Next

[Chapter 5](./saving.md) saves the game, so it survives the server stopping.
