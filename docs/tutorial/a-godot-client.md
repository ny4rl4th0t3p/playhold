# A Godot client

This page builds the game's client in Godot instead of the browser: the same game as chapter 7's page, on chapter 7's
server, with the [Godot SDK](../reference/sdk-godot.md). One player starts a game and shares its code, the other
pastes it, and they play, against each other or against chapter 7's page, over the same server.

It needs the server from chapters 1-3 and 5-7, and Godot 4.7.2. [Chapter 4](./a-page.md) is the browser's version of
this page: worth a read for what a client does, but its code isn't needed here.

The finished code is `sample/` in [playhold-sdk-godot](https://github.com/ny4rl4th0t3p/playhold-sdk-godot), where the
repository itself is the Godot project. Its paths start with `res://sample/`; in your project, they're your own.

## The project

Make a Godot project, here in `godot/` next to the game's Go code, with the Compatibility renderer, the one a web
export needs. Copy the SDK into it, from its release: download the
[`v0.5.0` zip](https://github.com/ny4rl4th0t3p/playhold-sdk-godot/archive/refs/tags/v0.5.0.zip) from GitHub, and copy
its `addons/playhold/` into `godot/addons/`. With git:

```bash
git clone --branch v0.5.0 https://github.com/ny4rl4th0t3p/playhold-sdk-godot.git
mkdir -p godot/addons && cp -R playhold-sdk-godot/addons/playhold godot/addons/
```

Then add `addons/playhold/client.gd` as an autoload named `Playhold`, in Project Settings, Globals, Autoload. An
autoload is one node the whole game shares, kept across changes of scene, which is what a connection to the server
should be. The client reconnects by itself after a lost connection, and joins the same room again.

## The messages, in GDScript

The client uses the same `.proto` files as the server. [Godobuf](https://github.com/oniksan/godobuf) generates GDScript
from them, at the commit the SDK uses, run headless from the project's folder:

```bash
git clone https://github.com/oniksan/godobuf.git
git -C godobuf checkout 523a66e2388c4235ded0aa5f2fecf91cf7e7a861
cp -R godobuf/addons/godobuf godot/addons/
cp -R proto godot/
cd godot
godot --headless -s addons/godobuf/godobuf_cmdln.gd --input=proto/tictactoe/v1/tictactoe.proto --output=tictactoe.gd
godot --headless -s addons/godobuf/godobuf_cmdln.gd --input=proto/tictactoe/v1/score.proto --output=score.gd
```

Copy `proto/` again when a `.proto` file changes, and generate again. Each file has the messages as classes, `Play`,
`BoardView`, `ScoreView`, and the enums without their prefix: `MARK_X` is `Mark.X`, `MESSAGE_PLAY` is `Message.PLAY`,
`SCORE_MESSAGE_REMATCH` is `ScoreMessage.REMATCH`, the same numbers as the Go side.

## The scene

`main.tscn` is a lobby, with **New game**, a field for a room's code and **Join**, and the game: the room's code to
share, with **Copy**, a status, a clock, the board, the score and **Play again**. Each node the script uses has a
unique name, such as `%Board`, so the script finds it wherever it sits in the scene:

::: details sample/main.tscn
<<< ../.godot-sdk/sample/main.tscn{ini}
:::

## The client

`main.gd` is the scene's script. When it starts, it connects its handlers to the autoload's signals, then connects to
the server:

<<< ../.godot-sdk/sample/main.gd#client{gdscript}

- `status_changed` reports the connection: connecting, connected, joined, or lost and reconnecting.
- `room_joined` gets the room's code, once the client is in the room.
- `views_received` gets everything the player can see in the room, one view per module, after every turn.
- `server_error` gets a refused move's error, with the handler's own text.
- `room_closed` says the room is gone for good. The client stays connected, and the game goes back to the lobby. It also
  goes back when an error comes while it's `CONNECTED` with a game on screen: coming back after a lost connection, the
  room refused the player, such as one a server lost when it restarted without a store. Chapter 7's page doesn't do
  either.

The server's address is `ws://localhost:8080/ws`, where `go run ./server` listens, unless the command line says
otherwise, as [Play it](#play-it) shows.

## The views

The board's view is one of the views, decoded with the generated class:

<<< ../.godot-sdk/sample/main.gd#views{gdscript}

A cell can only be pressed on the player's turn, and when it's empty, but that's only for the player's convenience: the
server checks every move again. The texts for each status are in the `text` region of the whole file.

## Moves, and the room's code

A press sends a move, with the move's ID from the `.proto`. **New game** asks the server for a room, and shows the code
it gets in a field that can be selected, with **Copy**, for the other player to paste:

<<< ../.godot-sdk/sample/main.gd#moves{gdscript}

## The score, and the clock

The score is one more view, next to the board's, and a rematch one more message, to the score module:

<<< ../.godot-sdk/sample/main.gd#score{gdscript}

The clock counts down from the time left that each view carries, as chapter 6's page does:

<<< ../.godot-sdk/sample/main.gd#clock{gdscript}

::: details The whole of sample/main.gd
<<< ../.godot-sdk/sample/main.gd{gdscript}
:::

## Play it

As in [chapter 7](./a-second-module.md), from the game's folder: PostgreSQL first, with the `compose.yaml` from
[chapter 5](./saving.md), then the server:

```bash
docker compose up -d
DATABASE_URL='postgres://postgres:tictactoe@localhost:5432/postgres?sslmode=disable' go run ./server
```

Then the game, from the Godot editor with **Run Project**, or from the command line, twice. Two copies on one machine
share one guest identity, so the newer would take the older's place: the second gets an identity of its own with
`--player`, read by the script after the command line's `--`:

```bash
godot --path godot
godot --path godot -- --player=2
```

1. Choose **New game** in one, then **Copy**.
2. Paste the code in the other's field, and choose **Join**.
3. Play, and choose **Play again** once a game is over.

Chapter 7's page plays against it too, over the same server: open `http://localhost:5173/?room=` and the code, or paste
the code from the page's link into the Godot client. Another server is `-- --server=ws://192.168.1.20:8080/ws`.

Stop the server in the middle of a game, and start it again: the client reconnects by itself, and the game is still
there.

## On the web

The same project exports to the web. The exported page connects from its own address, which the server has to allow,
as chapter 4's page does, and a public server uses TLS, as the [bonus page](./for-players.md) sets up. What Godot's web
export itself needs, such as the Compatibility renderer, is on Godot's
[web export page](https://docs.godotengine.org/en/stable/tutorials/export/exporting_for_web.html).

## Next

The game is complete, with a client of your choice. [A bonus page](./for-players.md), which you can skip, runs it for
players: over TLS, out of development mode, with the page on a host of its own.
