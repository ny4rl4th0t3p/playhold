# Configuration

Every setting, with its default. A field left at its zero value uses the default; turning something off always takes
an explicit `Off`, and the server logs it at startup. The guides explain when to change each one.

## ServerConfig

What `NewServer` takes.

### The game and saving

See [Keep games running](../guides/keeping-games-running.md).

| Field                | Default               | What it does                                                           |
|----------------------|-----------------------|------------------------------------------------------------------------|
| `RoomType`           | Required              | The kind of room the server creates                                    |
| `Store`              | Nil                   | What survives a restart. Nil keeps guests in memory and saves no rooms |
| `SnapshotInterval`   | 0, after every change | The least time between two saves of a room                             |
| `TimersAfterRestart` | `TimersPause`         | What pending timers do when the server comes back                      |

### Rooms

See [Timeouts](../guides/keeping-games-running.md#timeouts) and [Find a game](../guides/finding-a-game.md).

| Field                 | Default    | What it does                                                                        |
|-----------------------|------------|-------------------------------------------------------------------------------------|
| `AllowClientCodes`    | False      | Lets a code no room has create a room; otherwise only the server makes codes        |
| `Matchmaking`         | Nil, off   | Lets players queue to be matched; see Matchmaking below                             |
| `MaxRooms`            | 10,000     | The most rooms at once; over it, a new room gets `SERVER_FULL`                      |
| `EmptyRoomTimeout`    | 5 minutes  | Closes a room nobody has been connected to for this long                            |
| `InactiveRoomTimeout` | 30 minutes | Closes a room, and deletes its save, once no player has sent anything for this long |
| `DisconnectGrace`     | 1 minute   | How long a seated player has to come back before the game gets `PlayerDisconnected` |

### Security

See [Run in production](../guides/production.md).

| Field            | Default               | What it does                                                                    |
|------------------|-----------------------|---------------------------------------------------------------------------------|
| `Development`    | False                 | Turns the safety checks off, for a developer's machine                          |
| `OriginPatterns` | None                  | Other web origins allowed to connect; the server's own host always is           |
| `TrustedProxies` | None                  | Address ranges of the proxies that may send plain HTTP and the client's address |
| `ProxyClientIP`  | Reads X-Forwarded-For | Reads the client's address from a request that came through a trusted proxy     |

### Limits

See [Limit abuse and load](../guides/limits.md).

| Field              | Default                           | What it does                                                        |
|--------------------|-----------------------------------|---------------------------------------------------------------------|
| `ConnectRate`      | One every 200 ms, bursts of 20    | New connections per IP address                                      |
| `GuestRate`        | One every 6 s, bursts of 20       | New guests per IP address                                           |
| `MessageRate`      | One every 50 ms, bursts of 40     | Frames per connection                                               |
| `MaxConns`         | 4,096                             | Connections at once                                                 |
| `MaxConnsPerIP`    | 16                                | Connections at once from one IP address                             |
| `MaxMessageBytes`  | 32 KiB                            | The largest client message; a larger one closes the connection      |
| `HandshakeTimeout` | 10 s                              | How long a new connection has to say Hello                          |
| `Admission`        | On                                | When the server is too busy for new guests, rooms and seats         |
| `MemoryLimit`      | About 80% of the machine's memory | Go's soft memory limit; negative leaves it alone; `GOMEMLIMIT` wins |

### Crashes

See [Restart when stuck](../guides/keeping-games-running.md#restart-when-stuck) and
[When something crashes](../guides/crashes.md).

| Field              | Default | What it does                                                           |
|--------------------|---------|------------------------------------------------------------------------|
| `CrashJournal`     | Off     | The crash journal                                                      |
| `RestartWhenStuck` | False   | Restarts the process when a stuck room's goroutine keeps it overloaded |

### Logs and traces

See [Watch the server](../guides/observability.md).

| Field            | Default             | What it does                                 |
|------------------|---------------------|----------------------------------------------|
| `Logger`         | The runtime's       | Where server logs go                         |
| `HidePlayerIDs`  | False               | Leaves players' IDs out of logs and traces   |
| `TracerProvider` | Nil, traces nothing | The OpenTelemetry tracer provider for traces |

## ListenConfig

What `ListenAndServe` takes. The health checks are always at `/healthz` and `/readyz`. See
[Run in production](../guides/production.md#tls).

| Field                  | Default                          | What it does                                                                                |
|------------------------|----------------------------------|---------------------------------------------------------------------------------------------|
| `Addr`                 | `:443` with TLS, `:8080` without | The address to listen on                                                                    |
| `Path`                 | `/ws`                            | Where clients connect                                                                       |
| `Handler`              | Nil, which answers 404           | Serves every other path, such as the game's page                                            |
| `AutocertDomains`      | None                             | Turns on automatic certificates from Let's Encrypt for these domains                        |
| `AutocertCacheDir`     | Required with `AutocertDomains`  | The folder that keeps the certificates across restarts                                      |
| `AutocertEmail`        | None                             | Where Let's Encrypt writes about problems with the certificates                             |
| `AutocertDirectoryURL` | Let's Encrypt's                  | The ACME directory; set Let's Encrypt's staging one to test a setup                         |
| `CertFile`, `KeyFile`  | None                             | A certificate and its key, in PEM files, read at start                                      |
| `ShutdownTimeout`      | 8 s                              | How long saving the games may take once the context ends                                    |
| `Metrics`              | `127.0.0.1:9464`                 | Where the Prometheus metrics are served; see [Watch the server](../guides/observability.md) |

Set either the automatic certificate fields or the certificate files, not both. With neither, the server serves plain
HTTP, which it allows only behind a trusted proxy or in development mode.

## Config

What `NewRuntime` takes. See [Slow and stuck turns](../guides/keeping-games-running.md#slow-and-stuck-turns).

| Field          | Default          | What it does                                                               |
|----------------|------------------|----------------------------------------------------------------------------|
| `Logger`       | `slog.Default()` | Where runtime logs go                                                      |
| `StuckTimeout` | 5 s              | A longer turn closes its room as stuck                                     |
| `SlowTurn`     | 100 ms           | A longer turn is logged, measured in real time; `Off` logs none            |
| `NewSeed`      | A random seed    | The seed of a new room's random numbers; set it to reproduce a game        |
| `Now`          | `time.Now`       | The clock rooms read at the start of each turn; set it to reproduce a game |

## RoomConfig

What `NewRoomType` takes. The modules and the `Init` and `Close` hooks are the game itself, covered with building a
game.

| Field           | Default           | What it does                                                                                                         |
|-----------------|-------------------|----------------------------------------------------------------------------------------------------------------------|
| `Name`          | None              | Names the room type in logs and in saved rooms                                                                       |
| `QueueSize`     | 32                | Inputs a room's queue holds; over it, a message gets `QUEUE_FULL`                                                    |
| `MaxPlayers`    | No limit          | Seats per room; over it, joining gets `ROOM_FULL`                                                                    |
| `OnJoinAttempt` | Nil, seats anyone | Decides whether a new player takes a free seat; see [Who takes a seat](../guides/finding-a-game.md#who-takes-a-seat) |

A server only restores saved rooms of its own room type's `Name`, so renaming a room type leaves its saved rooms
unrestored. They stay in the store, and the server logs each one it skips.

## Value types

### RateLimit

`Every` and `Burst`: on average once every `Every`, with up to `Burst` at once. A field left at zero uses the layer's
default. `Off` turns the layer off. A module's own limit, set with `Registry.RateLimit`, needs both `Every` and `Burst`,
and can't be off.

### Timeout

`After`, or `Off`. Zero uses the default.

### Admission

| Field      | Default | Over it when                                                            |
|------------|---------|-------------------------------------------------------------------------|
| `Memory`   | 0.9     | The Go runtime holds this share of the memory limit                     |
| `SchedP99` | 20 ms   | Goroutines wait this long to run, at the 99th percentile over 5 seconds |
| `Queue`    | 0.5     | This share of all rooms' inbound queues is in use                       |
| `Off`      | False   | Turns admission control off                                             |

### Matchmaking

See [Matchmaking](../guides/finding-a-game.md#matchmaking).

| Field        | Default                                          | What it does                                                    |
|--------------|--------------------------------------------------|-----------------------------------------------------------------|
| `Matcher`    | Groups of `MaxPlayers`, in the order they queued | Forms groups from the players waiting                           |
| `Describe`   | Nil, `Info` stays nil                            | Returns what the game knows about a player, when they queue     |
| `Every`      | 1 s                                              | How often the matcher runs; nothing else starts it              |
| `ReserveFor` | 30 s                                             | How long a matched group's seats wait for its players           |

### JournalConfig

| Field       | Default | What it does                                                             |
|-------------|---------|--------------------------------------------------------------------------|
| `Path`      | Empty   | The journal's file. Empty turns the journal off                          |
| `Entries`   | 1,024   | How many turns it keeps, up to 16,777,216                                |
| `EntrySize` | 4 KiB   | The space for one turn, from 512 bytes to 1 MiB; a longer payload is cut |
