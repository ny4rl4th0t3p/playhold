# Watch the server

A playhold server serves Prometheus metrics: what's running, how fast it answers, and everything it refuses or
recovers from. With an OpenTelemetry tracer provider, it also traces each turn and each call to the store.

## Where the metrics are

`ListenAndServe` serves them at `/metrics`, on a listener of their own, never the game's. By default it listens on
`127.0.0.1:9464`, so only the machine itself can read them. `ListenConfig.Metrics` changes that:

| Setting                        | Serves                                                                         |
|--------------------------------|--------------------------------------------------------------------------------|
| `MetricsListen{}`              | `127.0.0.1:9464`, the default                                                  |
| `MetricsListen{Addr: ":9464"}` | Every address, such as for monitoring that scrapes a container; logs a warning |
| `MetricsListen{Off: true}`     | Nothing                                                                        |

The warning for an address other machines can reach isn't logged in development mode. A server that doesn't use
`ListenAndServe` serves `Server.MetricsHandler()` itself, on an address only its monitoring can reach.

## The metrics

Every server has its own registry, with these metrics and the Go runtime's and process's own, such as `go_goroutines`
and `process_resident_memory_bytes`.

### What's running

| Metric                       | Type  | What it shows                                          |
|------------------------------|-------|--------------------------------------------------------|
| `playhold_rooms`             | Gauge | Rooms running, counting finished ones until they close |
| `playhold_connections`       | Gauge | Open connections                                       |
| `playhold_players_connected` | Gauge | Players with a connection                              |
| `playhold_queue_fill`        | Gauge | The share of all rooms' inbound queues in use          |

### Messages and turns

| Metric                             | Type      | Labels                        | What it shows                                                       |
|------------------------------------|-----------|-------------------------------|---------------------------------------------------------------------|
| `playhold_messages_total`          | Counter   | `module`, `message`, `result` | Players' messages, by how they ended                                |
| `playhold_message_latency_seconds` | Histogram |                               | From a message arriving to its answer, the room's queue included    |
| `playhold_turn_duration_seconds`   | Histogram | `kind`                        | How long a turn took: `message`, `timer` or `other`, such as a join |
| `playhold_slow_turns_total`        | Counter   |                               | Turns longer than the time budget, `Config.SlowTurn`                |
| `playhold_view_bytes_total`        | Counter   | `module`                      | Bytes of player views sent                                          |

`message` is the message's ID within its module. A message's `result` is one of `ok`, `rejected` (the handler
refused it), `unknown_message`, `bad_message`, `queue_full`, `room_closed`, `turned_off` or `failed` (it crashed the
room). A message no handler knows is counted with `module` and `message` set to `unknown`, so a client can't create new
label values.

### Saving and recovering

| Metric                                   | Type      | Labels   | What it shows                                                        |
|------------------------------------------|-----------|----------|----------------------------------------------------------------------|
| `playhold_snapshot_delay_seconds`        | Histogram |          | How long a room's change waited to be saved: what a crash could lose |
| `playhold_snapshot_write_failures_total` | Counter   |          | Batches of saves that failed and were retried                        |
| `playhold_room_restores_total`           | Counter   | `reason` | Rooms replaced by a copy from their last save: `panic` or `stuck`    |
| `playhold_rooms_closed_total`            | Counter   | `reason` | Rooms that closed, a restored one included                           |
| `playhold_incidents_total`               | Counter   | `kind`   | Turns that hurt the whole server: `crashed` or `stuck`               |
| `playhold_messages_turned_off`           | Gauge     |          | Messages the breaker refuses now                                     |
| `playhold_stuck_goroutines`              | Gauge     |          | Stuck rooms' goroutines still running long after the watchdog        |

A room's close `reason` is one of `requested`, `shutdown`, `panic`, `init_failed`, `stuck` or `inactive`. See
[Keep games running](./keeping-games-running.md) and [When something crashes](./crashes.md).

### Abuse and load

| Metric                             | Type    | Labels  | What it shows                                                             |
|------------------------------------|---------|---------|---------------------------------------------------------------------------|
| `playhold_rate_limited_total`      | Counter | `layer` | Requests refused by a rate limit: `connect`, `guest`, `frame` or `module` |
| `playhold_overloaded`              | Gauge   |         | 1 while admission control refuses new work                                |
| `playhold_admission_refused_total` | Counter | `what`  | What admission control refused: `guest`, `room` or `seat`                 |

See [Limit abuse and load](./limits.md).

## Traces

Set `ServerConfig.TracerProvider` to an OpenTelemetry tracer provider, and the server traces its work. playhold only
uses OpenTelemetry's API, so the game picks the SDK and where the traces go. Without a provider, nothing is traced,
and tracing costs nothing.

| Span                      | When                                                     | Attributes                                                                     |
|---------------------------|----------------------------------------------------------|--------------------------------------------------------------------------------|
| `playhold.turn`           | Each turn of each room                                   | Room and its type, player, turn kind; for a message, its module, ID and result |
| `playhold.store.<method>` | Each call to the store, such as `WriteRooms`; not `Ping` | For room saves and loads, how many rooms                                       |
| `playhold.restore`        | Restoring the saved rooms at startup                     | Its `LoadRooms` call is part of it                                             |

The attributes are named `playhold.room.id`, `playhold.room.type`, `playhold.player.id`, `playhold.turn.kind`,
`playhold.message.module`, `playhold.message.id`, `playhold.message.result`, `playhold.timer.event`, `playhold.rooms`
and `playhold.rooms.deleted`.

Clients don't send trace context, so each turn starts a trace of its own. A turn that panicked is marked as an error; a
message the game refused isn't, since that's the game working. A turn the watchdog gave up on never ends its span. A
store call that failed is an error, but not finding a record isn't. The server logs `tracing on` at startup when it has
a provider.

### What tracing costs

Every span the provider samples costs CPU. In the [rough measurements](../measurements.md) setup, tracing every turn
took about a tenth more of the game server's CPU, and with saving on, 99% of moves were answered within about 10 ms
instead of 3 to 8 ms. On a busy server, have the provider sample a share of the traces, such as with the OpenTelemetry
SDK's ratio-based sampler.
