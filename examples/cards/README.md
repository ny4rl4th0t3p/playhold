# Crazy Eights

A sample game for two players, built from three modules that don't import each other:

- `deck`: the draw and discard piles
- `turns`: whose turn it is, and starting and ending the game
- `hands`: each player's cards and moves, reaching the other two through keepers

`cards.go` wires them together, and `web/` is the browser page, which uses `sdk/ts`.

## Rules

Each player starts with 7 cards. Play a card matching the top card's rank or the suit in play. An eight can always be
played, and its player chooses the suit to continue with. If you don't want to play or can't, draw one card, then play
or pass. The first player with no cards wins.

## Run, play and measure it

With Docker Compose, from this folder:

```bash
docker compose up --build
```

Open <http://localhost:8080> and start a new game, then open the room's link in a private window or another browser.
Two tabs in the same browser share one identity, so the newest tab replaces the older one.

The game runs limited to 1 CPU and 1 GB of memory, the size of a small VPS. To play from another machine on your
network, start it with `BIND=0.0.0.0`. There's no TLS or rate limiting yet, so don't expose it to the internet.

To put it under load, add a bot that plays games over real connections, and watch the server:

```bash
GAMES=500 docker compose --profile load up --build
```

In another terminal, record the server's CPU and memory to `stats.csv` every 5 seconds. A single reading can mislead, so
compare the trend with the bot's reports, which use the same UTC times:

```bash
sh record-stats.sh
```

| Setting              | Default | What it does                                                                              |
|----------------------|---------|-------------------------------------------------------------------------------------------|
| `GAMES`              | 100     | Games played at once, with two connections each                                           |
| `THINK`              | 1s      | How long a bot waits before each move                                                     |
| `GAME_CPUS`          | 1       | CPUs for the game server                                                                  |
| `GAME_MEMORY`        | 1g      | Memory for the game server, which sets its soft limit to about 80% of this                |
| `EMPTY_ROOM_TIMEOUT` | 5m      | How long a room stays open with nobody connected, including after its game ends           |
| `MAX_ROOMS`          | 10,000  | Most rooms at once, counting finished ones until they close; above it, joins get an error |
| `IDLE`               | 0       | Instead of playing, hold this many idle connections, to measure what one costs            |

The bot prints finished games, failures, moves per second and how long the server took to answer a move, followed by the
reason for any failures:

```text
10:48:02 games=12 moves=2210 failures=0 | moves/s=98.6 p50=1.2ms p99=4.8ms
```

Reading the numbers:

- The bot's latency includes its own delays, so run it on a machine with CPU to spare, or give the game fewer CPUs than
  the machine has.
- A finished game's room stays open until it has been empty for `EMPTY_ROOM_TIMEOUT`, so a long run holds more rooms
  than
  are being played.
- Results depend on your machine, so they aren't a benchmark of playhold.

## Run it without Docker

From the repo root:

```bash
npm --prefix sdk/ts install
npm --prefix examples/cards/web install
make gen
npm --prefix examples/cards/web run build
go run ./examples/cards/server -web examples/cards/web/dist
```

Then open <http://localhost:8080>.

## Host the page elsewhere

Build the page with the server's address, and let the page's origin connect:

```bash
VITE_SERVER_URL=wss://game.example/ws npm --prefix examples/cards/web run build
go run ./examples/cards/server -origins "page.example"
```

The build in `examples/cards/web/dist` uses relative paths, so it can be zipped and uploaded.