# Crazy Eights

A sample game for two players, built from three modules that don't import each other:

- `deck`: the draw and discard piles
- `turns`: whose turn it is, and starting and ending the game
- `hands`: each player's cards and moves, reaching the other two through keepers

`cards.go` wires them together, and `web/` is the browser page, which uses `sdk/ts`.

## Rules

Each player starts with 7 cards. Play a card matching the top card's rank or the suit in play. An eight can always be
played, and its player chooses the suit to continue with. If you don't want to play or can't, draw one card, then play
or pass. You have a minute for each turn, and a player who runs out of time draws and passes. The first player with no
cards wins, and the room closes a minute later. Both times can be changed, see below. If a player stays disconnected for
a minute, the other player sees a message saying so until they're back.

## Run, play and measure it

With Docker Compose, from this folder:

```bash
docker compose up --build
```

Open <http://localhost:8080> and start a new game, then open the room's link in a private window or another browser.
Two tabs in the same browser share one identity, so the newest tab replaces the older one.

The game runs limited to 1 CPU and 1 GB of memory, the size of a small VPS. To play from another machine on your
network, start it with `BIND=0.0.0.0`. It runs in development mode (`-dev`), over plain HTTP with the safety checks off,
and with the rate limits per address off for the load bot, so don't expose it to the internet.

Games are saved in PostgreSQL, so they survive the server stopping. Try it partway through a game, and each player gets
their seat and cards back when the page reconnects:

```bash
docker compose restart game
docker compose kill game && docker compose up -d game
```

`/healthz` says whether the process is up, and `/readyz` whether it should be sent players, which is no while it's
stopping or can't reach PostgreSQL.

To put it under load, add a bot that plays games over real connections, and watch the server:

```bash
GAMES=500 docker compose --profile load up --build
```

In another terminal, record the server's CPU and memory to `stats.csv` every 5 seconds. A single reading can mislead, so
compare the trend with the bot's reports, which use the same UTC times:

```bash
sh record-stats.sh
```

| Setting                  | In Compose, if unset   | What it does                                                                                             |
|--------------------------|------------------------|----------------------------------------------------------------------------------------------------------|
| `GAMES`                  | 100                    | Games played at once, with two connections each                                                          |
| `THINK`                  | 1s                     | How long a bot waits before each move                                                                    |
| `GAME_CPUS`              | 1                      | CPUs for the game server                                                                                 |
| `GAME_MEMORY`            | 1g                     | Memory for the game server, which sets its soft limit to about 80% of this                               |
| `EMPTY_ROOM_TIMEOUT`     | 5m                     | How long a room stays open with nobody connected, including after its game ends, or `off`                |
| `MAX_ROOMS`              | 10,000                 | Most rooms at once, counting finished ones until they close; above it, joins get an error                |
| `TURN_TIME`              | 1m                     | How long a player has for each turn                                                                      |
| `FINISHED_ROOM_TIME`     | 1m                     | How long a room stays open after its game ends                                                           |
| `TIMERS_AFTER_RESTART`   | pause                  | What a server coming back does with turn timers: `pause`, `keep` or `cancel`                             |
| `DISCONNECT_GRACE`       | 1m                     | How long a player can be disconnected before the other player is told, or `off`                          |
| `SHUTDOWN_TIMEOUT`       | 8s                     | How long to wait for games to be saved before exiting                                                    |
| `STOP_GRACE_PERIOD`      | 10s                    | How long Docker waits before killing the server; keep it above `SHUTDOWN_TIMEOUT`                        |
| `TURNS_SNAPSHOT_VERSION` | 1                      | The format saved games use, 1 or 2, to try a deploy that changes it                                      |
| `RATE`                   | 0                      | Moves per second across all games, on a fixed schedule, instead of `THINK`                               |
| `WARMUP`                 | 0s                     | How long to leave out of the bot's final summary                                                         |
| `DURATION`               | 0s                     | How long the bot runs; 0s runs until stopped                                                             |
| `IDLE`                   | 0                      | Instead of playing, hold this many idle connections, to measure what one costs                           |
| `DATABASE_URL`           | The `postgres` service | Where games are saved. Set it empty to save nothing, for comparison                                      |
| `SNAPSHOT_INTERVAL`      | 0s                     | Least time between two saves of a game; 0s saves after every move. A crash can lose up to this much      |
| `POSTGRES_CPUS`          | 1                      | CPUs for PostgreSQL                                                                                      |
| `POSTGRES_MEMORY`        | 1g                     | Memory for PostgreSQL                                                                                    |
| `CONNECT_RATE`           | off, for the bot       | New connections per address, as `EVERY/BURST`, or `off`. The server's own default is `200ms/20`          |
| `GUEST_RATE`             | off, for the bot       | New guests per address, as `EVERY/BURST`, or `off`. The server's own default is `6s/20`                  |
| `MESSAGE_RATE`           | 50ms/40                | Frames per connection, as `EVERY/BURST`, or `off`. The same as the server's own default                  |
| `DEBUG_ADDR`             | empty, off             | Address for Go's profiling endpoints, such as `:6060`. With `t12.sh`, published on `127.0.0.1:6060` only |
| `GAME_GODEBUG`           | empty                  | With `t12.sh`, the game server's `GODEBUG`, such as `gctrace=1` to log every garbage collection          |
| `GAME_GOGC`              | empty, Go's default    | With `t12.sh`, the game server's `GOGC`, such as `400` for rarer garbage collections and a larger heap   |

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

With `RATE` set, moves go out on a fixed schedule whether or not the server keeps up, and each one's latency counts from
when it was due, so a server falling behind shows up in the numbers. The server logs `room snapshots written` every 10
seconds or so while saving, with the longest a change waited to be saved, which is how much a crash could lose.

## See a game survive

`done-test.sh` checks that a game in progress survives what a real server goes through. Run it from this folder:

```bash
sh done-test.sh [seconds between interruptions, default 15]
```

What it does:

1. Starts the game server and PostgreSQL with Docker Compose, using the same project and database as the demo above.
2. Starts the bot with `-survive 3`. The bot plays **both seats of one game**, as two separate connections. There are no
   other players. For the first 5 intervals (75 s by default) both seats only draw and pass, so the game can't end
   before the interruptions; after that they play normally until someone wins.
3. Interrupts the server three times, one interval apart:
    - `docker compose restart game`, a clean stop and start, as a service manager would do;
    - `docker compose kill -s SIGKILL game`, then starts it again, as a crash would;
    - `TURNS_SNAPSHOT_VERSION=2 docker compose up -d game`, a deploy of a server that saves games in a new format, which
      has to migrate the saved game first.
4. Waits for the game to finish, prints the bot's log, which is also kept in `done-test.log`, and exits with the bot's
   status.

After each interruption, each seat reconnects every 200 ms until it's back in the game, and logs how long that took.
The test fails if:

- a seat can't get back in: its identity, seat or room is gone;
- a seat's cards change by more than one card across a reconnect. One is allowed, because a move's answer can be cut
  off by the drop, and a crash can lose the very last move;
- either seat reconnected fewer than 3 times, so the interruptions didn't all happen during the game;
- the game doesn't end with a winner that both seats agree on, holding no cards;
- the game isn't over within 10 minutes.

It ends with a line per seat: reconnects, moves lost or unseen, moves the server refused around a reconnect, and the
slowest return. The server is left saving at version 2; `docker compose up -d game` puts it back to version 1 once that
game has finished.

## Measure what saving costs

`t12.sh` runs the same load with PostgreSQL off and on, to see what saving games costs the server. Stop anything else
using port 8080 first, including the demo, which would also take CPU from the measurement:

```bash
sh t12.sh
```

What it does, for each run:

1. Starts a separate Compose project, `playhold-t12`, with an empty database, so the demo's games are left alone.
   `compose.t12.yaml` pins each container to its own CPU core: the game server to core 0, PostgreSQL to core 1 and the
   bot to cores 2 and 3. Change them with `GAME_CPUSET`, `POSTGRES_CPUSET` and `BOT_CPUSET`.
2. Plays 500 games at 3,000 moves per second in total, on a fixed schedule, for 5 min 30 s. The first 30 s are warm-up
   and left out of the results. Finished rooms close after 5 s, so they don't pile up.
3. Records CPU and memory of the game server, PostgreSQL and the bot every 5 seconds.
4. Removes the project and its database.

It does one run with saving off and one with it on, 3 times, alternating, so it takes about 35 minutes. Set `RUNS`,
`GAMES`, `RATE`, `WARMUP` and `DURATION` to change that.

Everything goes to `t12-results/`, named by setting and run, such as `on-2`:

| File                | What it holds                                                               |
|---------------------|-----------------------------------------------------------------------------|
| `on-2-bot.log`      | The bot's reports every 5 seconds, and its final `summary` line             |
| `on-2-game.log`     | The server's log, including `room snapshots written` about every 10 seconds |
| `on-2-game.csv`     | The game server's CPU and memory                                            |
| `on-2-postgres.csv` | PostgreSQL's CPU and memory                                                 |
| `on-2-bot.csv`      | The bot's CPU and memory                                                    |

At the end it prints each run's `summary` line and each report's longest snapshot delay. The summary has:

- `p50`, `p99` and `p99.9`: half, 99% and 99.9% of moves were answered at least this fast; `max` is the slowest. A
  move's time runs from when it was due on the schedule to when the bot received the view it caused, so it includes
  the network and the bot, not just the server;
- `moves/s`, which should match the rate asked for. If it's lower, the server or the bot couldn't keep up;
- `failures`, which should be 0.

`max_delay` in the server's log is the longest a change waited before it was saved, which is how much a crash could
lose at that moment. With `SNAPSHOT_INTERVAL` set, it counts only the wait for PostgreSQL, so a crash can lose up to
the interval more.

A run only counts if the machine was otherwise idle, and if the CSVs show the bot stayed under about 80% of its two
cores and the game server under 70% of its one. Docker counts 100% per core, so for the bot that's about 160%.

To see where the time goes, profile the same runs:

```bash
PROFILE=1 RUNS=1 sh t12.sh
```

Each run then collects the same profiles at the same point, 90 seconds after its bot starts (`PROFILE_AFTER`): a 30 s
CPU profile, a 5 s execution trace, allocations, blocking and lock contention, and the CPU pinning Docker applied. If Go
is installed, each profile also gets a text report next to it, such as `prof-on-1-cpu.txt`. These runs are named
`prof-off-1`, `prof-on-1` and so on, and their latencies don't count, because profiling slows the server down.

## Run it without Docker

From the repo root:

```bash
npm --prefix sdk/ts install
npm --prefix examples/cards/web install
make gen
npm --prefix examples/cards/web run build
go run ./examples/cards/server -dev -web examples/cards/web/dist
```

Then open <http://localhost:8080>. `-dev` allows plain HTTP; without it, the server refuses to start with neither TLS
nor a trusted proxy.

## Run it on a public server

Outside `-dev`, the server needs TLS, in one of three ways:

```bash
# Automatic certificates from Let's Encrypt; game.example must point at this server, with port 443 open
go run ./examples/cards/server -tls-domains game.example -tls-cache /var/lib/cards/certs

# A certificate you already have
go run ./examples/cards/server -tls-cert cert.pem -tls-key key.pem

# Behind a proxy that ends TLS, such as a load balancer at 10.0.0.2
go run ./examples/cards/server -trusted-proxies 10.0.0.2/32
```

How automatic certificates work:

- The server asks Let's Encrypt for a certificate when the first player connects, not when it starts. That first
  connection waits a few seconds.
- It renews the certificate by itself while it runs.
- It keeps the certificate in the `-tls-cache` folder, so a restart doesn't ask for a new one.
- If Let's Encrypt can't reach the server, for example because the domain points elsewhere or port 443 is closed, the
  connection fails and the server's log says why.

Let's Encrypt blocks a domain for a while after a few failed requests. To try a new setup without that risk, use their
test service first. It works the same way, but browsers don't trust its certificates.

```bash
# 1. Test run: Let's Encrypt's test service, with a cache folder only for test certificates
go run ./examples/cards/server -tls-domains game.example -tls-cache /var/lib/cards/certs-test -tls-acme-directory https://acme-staging-v02.api.letsencrypt.org/directory

# From another terminal: an issuer with (STAGING) in its name means the setup works
curl -kv https://game.example/healthz 2>&1 | grep issuer
```

```bash
# 2. Real run: the real Let's Encrypt, the default, with the real cache folder
go run ./examples/cards/server -tls-domains game.example -tls-cache /var/lib/cards/certs
```

The two runs need different cache folders: certificates are kept by domain name, so a shared folder would keep serving
the test certificate.

Rate limits are on by default: new connections and new guests per address, and frames per connection. Change them with
`-connect-rate`, `-guest-rate` and `-message-rate`, as `EVERY/BURST` such as `200ms/20`. `off` turns one off, for a
server behind a proxy that already limits it; the server then logs a warning at startup.

Without `-addr`, it listens on `:443` with TLS and `:8080` without. Behind a proxy, the client's address comes from
the proxy's `X-Forwarded-For` header, and only from the listed proxies. Outside `-dev`, the server also refuses an
`-origins` pattern that matches any site, such as `*`, and a `-debug-addr` that isn't on localhost.

## Host the page elsewhere

Build the page with the server's address, and let the page's origin connect:

```bash
VITE_SERVER_URL=wss://game.example/ws npm --prefix examples/cards/web run build
go run ./examples/cards/server -origins "page.example" -tls-domains game.example -tls-cache /var/lib/cards/certs
```

The build in `examples/cards/web/dist` uses relative paths, so it can be zipped and uploaded.