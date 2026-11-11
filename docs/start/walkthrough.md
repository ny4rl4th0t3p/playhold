# Walkthrough

Run the sample game, play it in two browsers, and stop the server in the middle of a game to watch it come back. Then a
map of the code that did each part. You need Git and Docker with Compose; nothing else is installed.

## Run it

```bash
git clone https://github.com/ny4rl4th0t3p/playhold.git
cd playhold/examples/cards
docker compose up --build
```

This starts the game's server and PostgreSQL, which keeps the games. The server runs in development mode, over plain
HTTP, so keep it on your own machine.

## Play

1. Open `http://localhost:8080` and choose **New game**. The page shows the room's code, and puts it in its own address
   as `?room=<code>`.
2. Open that address in a private window, or in another browser, and the game starts. Two tabs in the same browser
   share one identity, so the newer one takes over and the older one says it stopped.
3. Each window shows only its own hand, and how many cards the other player holds: the server sends each player
   their own view, so the other hand never reaches the browser.

**Play with someone**, in both windows instead, puts each in a queue, and the server pairs them in a new room.

## Stop the server mid-game

In another terminal, from the same folder:

```bash
docker compose restart game
```

The page says it lost the connection, reconnects by itself, and the game carries on with the same hands and the same
turn. The server saved the game after every move, and brought it back when it started. Stopping it harder works too:

```bash
docker compose kill game && docker compose up -d game
```

The server's health checks are at `http://localhost:8080/healthz` and `http://localhost:8080/readyz`, and its
Prometheus metrics at `http://localhost:9464/metrics`.

## What did what

All paths are in `examples/cards`:

| Path              | What it is                                                                                 |
|-------------------|--------------------------------------------------------------------------------------------|
| `server/main.go`  | The server: turns its flags into a `ServerConfig`, then `NewServer` and `ListenAndServe`   |
| `cards.go`        | The room type: the three modules below, wired together, with two seats per room            |
| `deck/`           | A module: the draw and discard piles                                                       |
| `turns/`          | A module: whose turn it is, the turn timer, and starting and ending the game               |
| `hands/`          | A module: each player's cards and moves, reaching the piles and turns through keepers      |
| `rules/`          | The card rules, and the events the modules share                                           |
| `proto/cards/v1/` | The game's messages and views, generated into `pb/` for Go and `web/src/gen/` for the page |
| `web/`            | The page, on the TypeScript SDK, showing what the server sends                             |
| `bot/`            | A bot on the Go SDK, for load tests and the survival test                                  |
| `compose.yaml`    | The server and PostgreSQL, with the settings in the sample's README                        |

The modules never import each other: `hands` declares what it needs from the piles and the turns, and `cards.go` hands
it those. [Modules, keepers and events](../concepts/modules.md) covers how that works.

## Next

- To put the server under load and measure it, see the sample's
  [README](https://github.com/ny4rl4th0t3p/playhold/tree/main/examples/cards).
- To run a server for players, see [Run in production](../guides/production.md) and [Deploy](../guides/deploy.md).
