# Changelog

## v0.3.0, unreleased

The first public release. This section grows as the work lands on `main`.

### Breaking changes

**Production mode by default.** Without `ServerConfig.Development`, a connection that arrives without TLS, and not
through a trusted proxy, is refused with HTTP 403, and `NewServer` refuses an origin pattern that matches every site
or a trusted proxy range that covers every address.

- On a developer's machine, set `Development: true`.
- On a public server, serve with `ListenAndServe` and TLS, or list the proxy that ends TLS in `TrustedProxies`. See
  [Run in production](./guides/production.md).

**Rate limits on by default.** New connections and new guests per IP address, and frames per connection, are limited.
The defaults are far above what a player needs, but a load test from one address goes over them.

- Raise a limit, or turn it off with `Off` behind a proxy that already limits it. See
  [Limit abuse and load](./guides/limits.md#rate-limits).

**`EmptyRoomTimeout` and `DisconnectGrace` are a `Timeout`.** They were a `time.Duration`, and now take
`Timeout{After: d}`, or `Timeout{Off: true}`.

- Replace `EmptyRoomTimeout: 10 * time.Minute` with `EmptyRoomTimeout: playhold.Timeout{After: 10 * time.Minute}`.

**Inactive rooms close.** A room no player has sent anything for 30 minutes closes, and its saved state is deleted.

- For a game whose rooms wait longer between moves, raise `InactiveRoomTimeout`, or turn it off. See
  [Timeouts](./guides/keeping-games-running.md#timeouts).

**`ListenAndServe` also serves metrics, on `127.0.0.1:9464`.** A second server on the same machine can't start until
it uses another address.

- Give each server its own `ListenConfig.Metrics.Addr`, or turn the metrics off with `Metrics.Off`. See
  [Watch the server](./guides/observability.md).

**The server makes rooms' codes.** A `Join` with a code no room has used to create the room; now it's refused with
`ROOM_NOT_FOUND`, and a `Join` with an empty code creates a room with a code the server makes.

- In a client, create rooms with the SDKs' `createRoom`, and share the code it reports.
- A game that picks its own codes sets `ServerConfig.AllowClientCodes`. See [Find a game](./guides/finding-a-game.md).

**Guests expire.** A guest who hasn't connected for 90 days is deleted, and coming back, starts again as a new guest.

- Change the period with `GuestExpiry`, or keep every guest with `GuestExpiry: playhold.Timeout{Off: true}`. See
  [What playhold keeps about players](./guides/privacy.md).

**`store.Store` has five new methods**: `SaveIncident`, `LoadIncidents` and `DeleteIncidents` for the breaker, and
`TouchGuest` and `DeleteGuestsUnseenSince` for guest expiry. `SaveGuest` also records that the guest was seen.

- A store implemented outside playhold must add them. The memory and PostgreSQL stores have them; the PostgreSQL store
  adds an `incidents` table and a `last_seen` column on `guests` when it starts.

### New responses for clients

New error codes `RATE_LIMITED`, `TURNED_OFF` and `ROOM_NOT_FOUND`, and new close reasons, `INACTIVE` and
`MATCH_INCOMPLETE`. The SDKs pass them to the game like any other; a client built before them sees an unknown code.
See [Errors and close reasons](./reference/errors.md).

### Added

- `SnapshotInterval`, to save busy rooms less often.
- `RoomConfig.OnJoinAttempt`, for the game to decide who takes a seat, and the SDKs' `createRoom`.
- Matchmaking, with `ServerConfig.Matchmaking`: a queue, the game's matcher, and a new room for each group, with its
  seats kept for the players for a while. New frames `Queue`, `LeaveQueue`, `Queued` and `Matched`, and the Go SDK's
  `Queue`, `Matched` and `LeaveQueue`. The TypeScript SDK's `queue` and `leaveQueue`, with a `queued` status, which
  join the matched room by themselves and queue again after a lost connection or `MATCH_INCOMPLETE`.
- TLS: automatic certificates from Let's Encrypt, certificate files, or a trusted proxy that ends TLS, with
  `ListenAndServe`.
- Layered rate limits, with a module's own limits on its messages.
- Admission control: new guests, rooms and seats are refused while the server is overloaded.
- Slow turns are logged, and nothing from a stuck room's goroutine reaches players or the store.
- `RestartWhenStuck`, for a server under a service manager.
- The crash journal and `playholdjournal`.
- The breaker: a message that keeps crashing the server is turned off for an hour, and a room that keeps crashing it
  isn't restored.
- Prometheus metrics, on a listener of their own at `127.0.0.1:9464` by default.
- OpenTelemetry traces of turns, store calls and the restore, with `ServerConfig.TracerProvider`.
- Logs name the player on a connection once it has said Hello, and the turn a stuck or panicking room was running.
- Guest expiry, and a page on what playhold keeps about players, where and for how long.
  `ServerConfig.HidePlayerIDs` leaves player IDs out of logs and traces.
- This documentation site.
- The sample: profiles of the server during a `t12.sh` run.

## v0.2.0, 6 November 2026

Games survive a restart, a crash and a deploy that changes how games are saved.

### Changed

- A room's inbound queue holds 32 inputs by default instead of 256, since each room allocates its whole queue. A room
  type sets its own with `RoomConfig.QueueSize`.

### New responses for clients

New error codes `UNAVAILABLE`, `SERVER_FULL` and `UPDATE_REQUIRED`.

### Added

- `Store`, with memory and PostgreSQL versions; guests are kept in it.
- Rooms saved in the background after every change, and restored after a restart or a crash, with the same ID.
- Module state migrations, for a deploy that changes how a module saves its state.
- Room timers; the sample adds a turn timer and closes finished rooms.
- `MaxRooms`, the most rooms a server runs at once.
- Drain, a shutdown that saves every room, and the `/healthz` and `/readyz` health checks.
- Client schema versions: a client too old for a module is told to update.
- `DisconnectGrace`: a seated player who drops has a while to come back before the game hears they left.
- The Go SDK, for bots and load tests.
- The sample: an idle load mode and a fixed-rate mode for its bot, and `done-test.sh`, which checks that a game survives
  a restart, a crash and a deploy.

## v0.1.0, 4 November 2026

The first tagged version: a sample game playable in a browser.

### Added

- The room runtime: each room handles its inputs one at a time, on its own goroutine.
- Modules, with typed state, message handlers, events and keepers.
- The wire format, in protobuf, and the WebSocket transport.
- Guests, joining a room by its code, and the server.
- Player views, so each player sees only what they're allowed to.
- `playholdvet`, the determinism checks for game code.
- The watchdog, which closes a room stuck in a turn.
- The TypeScript SDK, which reconnects by itself.
- The sample, Crazy Eights, with a browser page, a Docker Compose setup and a load bot.
