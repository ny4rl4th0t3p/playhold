# Limit abuse and load

A server open to the internet gets more than players: floods of connections, scripts creating guests, and clients
sending as fast as they can. And a busy server can take on more games than it can run. playhold limits both by default.
Every limit can be changed, and turning one off is explicit and logged.

## Rate limits

Three layers apply to every server, each with a default far above what a player of a turn-based game needs:

| Setting       | Counts                         | Default                                | Over it                                                        |
|---------------|--------------------------------|----------------------------------------|----------------------------------------------------------------|
| `ConnectRate` | New connections per IP address | One every 200 ms, with bursts of 20    | The connection is refused with HTTP 429                        |
| `GuestRate`   | New guests per IP address      | One every 6 seconds, with bursts of 20 | The Hello gets a `RATE_LIMITED` error, and no guest is created |
| `MessageRate` | Frames per connection          | One every 50 ms, with bursts of 40     | The frame gets a `RATE_LIMITED` error and isn't handled        |

The bursts let a whole network behind one address, such as a school, reconnect at once after a restart. A player has
one connection at a time, so `MessageRate` is also the limit per player.

A `RateLimit` left at zero keeps the default; leaving it out never removes a limit. `Off` turns a layer off, for a
server behind a proxy that already limits it:

<<< ../../example_test.go#rate-limits

### A module's own limit

Some messages cost the room or other players something, such as a chat line. A module can limit one of its messages
further, on top of `MessageRate`:

<<< ../../example_test.go#module-limit

## Connections

| Setting            | Default | Over it                                       |
|--------------------|---------|-----------------------------------------------|
| `MaxConns`         | 4,096   | The connection is refused with HTTP 503       |
| `MaxConnsPerIP`    | 16      | The connection is refused with HTTP 429       |
| `MaxMessageBytes`  | 32 KiB  | The connection is closed                      |
| `HandshakeTimeout` | 10 s    | A connection that hasn't said Hello is closed |

## Rooms

`MaxRooms`, 10,000 by default, is the most rooms the server runs at once. Over it, joining a new code gets a
`SERVER_FULL` error, and existing rooms carry on.

## Admission control

A server can be under its caps and still overloaded. Admission control checks three signals every second:

| Signal     | Over it when                                                                                   | Default |
|------------|------------------------------------------------------------------------------------------------|---------|
| `Memory`   | The Go runtime holds this share of the memory limit. Not checked without a limit               | 0.9     |
| `SchedP99` | Goroutines wait this long to run, at the 99th percentile over 4 seconds: the CPU can't keep up | 20 ms   |
| `Queue`    | This share of all rooms' inbound queues is in use                                              | 0.5     |

While any signal is over, the server refuses new work:

- new guests get an `UNAVAILABLE` error;
- new rooms, new seats and new places in the matchmaking queue get a `SERVER_FULL` error.

It never refuses a player coming back to a seat they already have, and rooms already running carry on. It takes new
work again once every signal has stayed under 90% of its threshold for 10 checks in a row, so it doesn't flap on and
off. `/readyz` doesn't change: an overloaded server still serves the players it has.

<<< ../../example_test.go#admission

`Off` turns admission control off, and the server logs a
warning. [Restart when stuck](./keeping-games-running.md#restart-when-stuck)
needs it on.

The memory limit is `MemoryLimit`, about 80% of the machine's memory by default, or of the container's limit if that's
lower. The `GOMEMLIMIT` environment variable always wins.