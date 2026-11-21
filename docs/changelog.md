# Changelog

## Unreleased

Three names in `frames.proto` change, for Godot: Godobuf, which generates GDScript from protobuf, refuses fields named
`message`, and a message named `Error` clashes with GDScript's global `Error`. The bytes on the wire don't change:
clients and servers built before and after the renames still talk to each other.

### Breaking changes

**Renamed in `frames.proto`,** and so in the code generated from it:

- `ClientFrame.message` is `module_message`. In Go, `wire.ClientFrame_Message` is `wire.ClientFrame_ModuleMessage`,
  with its field `ModuleMessage`, and `GetMessage()` is `GetModuleMessage()`. In TypeScript, the `body` case
  `"message"` is `"moduleMessage"`.
- The `Error` message is `ServerError`. In Go, `wire.Error` is `wire.ServerError`; `ServerFrame.GetError()` keeps its
  name. In TypeScript, `Error` and `ErrorSchema` are `ServerError` and `ServerErrorSchema`.
- `Error.message` is `ServerError.text`: `Message` and `GetMessage()` are `Text` and `GetText()` in Go, `message` is
  `text` in TypeScript.

Only code that uses the generated types directly changes. The Go SDK's `Error` and the TypeScript SDK's `onError` keep
their `message`.

## v0.4.2, 20 November 2026

The Go API's documentation, ready for pkg.go.dev: every exported doc comment checked against the code. Nothing in the
API changes.

### Changed

- A package overview, and one complete example, `NewServer`'s: a game's whole main function. The documentation site's
  snippets are no longer examples, since they call helpers an example's page doesn't show.
- Doc comments that said more or less than the code does, now matching it: among them `JournalUnknown`, which named
  the wrong turn, `ErrRestartNeeded`, `RoomConfig.Close`, `Registry.RateLimit` and `Timeout`. The same `unknown` row is
  fixed in [When something crashes](./guides/crashes.md#what-it-shows).
- The defaults of `RoomConfig.QueueSize`, `ServerConfig.MaxConns`, `MaxConnsPerIP`, `MaxMessageBytes` and
  `HandshakeTimeout` are stated, and exported fields that had no comment have one.
- Admission control's texts name matchmaking queues, which it refuses too: the `admission_refused_total` and
  `overloaded` metrics' help, and the `server overloaded` warning in the log.
- `playholdvet`'s and `playholdjournal`'s doc comments give commands that work from any folder.

## v0.4.1, 19 November 2026

The server's log now says three things it used to keep to itself. Nothing in the API changes.

### Changed

- A page refused for its web origin is logged as a warning, with its origin and the pattern that would allow it in
  `ServerConfig.OriginPatterns`. Before, the refusal was only logged at debug level, so a page refused in every browser
  left the log empty. See [Web origins](./guides/production.md#web-origins).
- A server with no `ServerConfig.Store` logs a warning at startup: games and guests live in memory only, and a restart
  loses them.
- Restoring rooms logs each migration it ran, with the module, both versions and how many rooms went through it. See
  [Changing what's saved](./concepts/saving.md#changing-what-s-saved).

## v0.4.0, 18 November 2026

The SDKs and the sample game move to repositories of their own, built on playhold's releases as any game's code would
be, and installable without a clone of playhold.

### Breaking changes

**The Go SDK is its own module.** `github.com/ny4rl4th0t3p/playhold/sdk/go` is gone from playhold.

- Require `github.com/ny4rl4th0t3p/playhold-sdk-go` at `v0.4.0`, and import it as
  `sdk "github.com/ny4rl4th0t3p/playhold-sdk-go"`. The package and its API are unchanged. See [Go SDK](./reference/sdk-go.md).

**The TypeScript SDK is its own repository.** `sdk/ts` is gone from playhold.

- Depend on `"playhold-sdk": "git+https://github.com/ny4rl4th0t3p/playhold-sdk-ts.git#v0.4.0"` instead of a `file:`
  path into a clone; installing it needs `git`. The package's name and API are unchanged. See
  [TypeScript SDK](./reference/sdk-ts.md).

**The sample game moved.** `examples/cards` is now
[playhold-crazy-eights](https://github.com/ny4rl4th0t3p/playhold-crazy-eights), with its bot, Compose setup and
measurement scripts. Its packages are no longer in playhold's module.

### Added

- `github.com/ny4rl4th0t3p/playhold/wire`, a module of its own with only protobuf as a dependency: the frames' Go
  types, shared by the server and the Go SDK, so one program can use both, such as a game server running its own bots.

### Changed

- `golang.org/x/net` v0.62.0, which fixes two advisories in its `html` package (GO-2026-6866, GO-2026-6860). playhold
  doesn't use that package.

## v0.3.0, 12 November 2026

The sample game runs over TLS with safe defaults, and the server gains protection against overload and crashes,
matchmaking, tools to test and replay games, and a documentation site.

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

- In a client, create rooms with the TypeScript SDK's `createRoom` or the Go SDK's `CreateRoom`, and share the code it
  reports.
- A game that picks its own codes sets `ServerConfig.AllowClientCodes`. See [Find a game](./guides/finding-a-game.md).

**Guests expire.** A guest the server hasn't seen for 90 days, counting each Hello it accepts, is deleted, and coming
back, starts again as a new guest.

- Change the period with `GuestExpiry`, or keep every guest with `GuestExpiry: playhold.Timeout{Off: true}`. See
  [What playhold keeps about players](./guides/privacy.md).

**`store.Store` has five new methods**: `SaveIncident`, `LoadIncidents` and `DeleteIncidents` for the breaker, and
`TouchGuest` and `DeleteGuestsUnseenSince` for guest expiry. `SaveGuest` also records that the guest was seen.

- A store implemented outside playhold must add them. The memory and PostgreSQL stores have them; the PostgreSQL store
  adds an `incidents` table and a `last_seen` column on `guests` when it starts.

**Go 1.27.4 or newer.** Earlier versions of Go's `net/http` can crash an HTTP server (GO-2026-6859), and every game on
playhold serves HTTP.

- Build with Go 1.27.4 or newer.

### New responses for clients

New error codes `RATE_LIMITED`, `TURNED_OFF` and `ROOM_NOT_FOUND`, and new close reasons, `INACTIVE` and
`MATCH_INCOMPLETE`. The SDKs pass them to the game like any other; a client built before them sees an unknown code.
See [Errors and close reasons](./reference/errors.md).

### Added

- `SnapshotInterval`, to save busy rooms less often.
- `RoomConfig.OnJoinAttempt`, for the game to decide who takes a seat, and the SDKs' `createRoom` and `CreateRoom`.
- Matchmaking, with `ServerConfig.Matchmaking`: a queue, the game's matcher, and a new room for each group, with its
  seats kept for the players for a while. New frames `Queue`, `LeaveQueue`, `Queued` and `Matched`, and the Go SDK's
  `Queue`, `Matched` and `LeaveQueue`. The TypeScript SDK's `queue` and `leaveQueue`, with a `queued` status, which
  join the matched room by themselves and queue again after a lost connection or `MATCH_INCOMPLETE`.
- `Matchmaking.MinPlayers`, for a matched game to go ahead without the players who never came, and the `MatchReady`
  event, when a matched room's game can start. `Matchmaking.FillFor`, for the default matcher to start a group smaller
  than `MaxPlayers` once its players have waited that long.
- `playholdtest`, to test a game's room type without a server: players join, send messages and read their views, time
  moves only when the test moves it, and a room can be restarted from its save or onto a new room type. See
  [Test a game](./guides/testing.md).
- Recording and replay: `RoomConfig.Record` and `ServerConfig.Recording` record every turn of a room type's rooms, with a
  hash of the room's state after each, and `playholdtest.Replay` plays a recording again, failing at the first turn that
  differs. A finished recording is renamed `.done.rec`, or `.problem.rec` after a panic, a stuck turn or a crash, and
  kept until `Retention` or `MaxSize`, 10 GiB by default, deletes it. See [Record and replay](./guides/recording.md).
- TLS: automatic certificates from Let's Encrypt, certificate files, or a trusted proxy that ends TLS, with
  `ListenAndServe`. `ProxyClientIP`, to read the client's address from a trusted proxy's request another way than
  `X-Forwarded-For`.
- Layered rate limits, with a module's own limits on its messages.
- Admission control: new guests, rooms and seats are refused while the server is overloaded.
- Slow turns are logged, and nothing from a stuck room's goroutine reaches players or the store.
- `RestartWhenStuck`, for a server under a service manager.
- The crash journal and `playholdjournal`.
- The breaker: a message that keeps crashing the server is turned off for an hour, and a room that keeps crashing it
  isn't restored.
- Prometheus metrics, on a listener of their own at `127.0.0.1:9464` by default, or from `MetricsHandler` for a server
  not using `ListenAndServe`.
- OpenTelemetry traces of turns, store calls and the restore, with `ServerConfig.TracerProvider`.
- Logs name the player on a connection once it has said Hello, and the turn a stuck or panicking room was running.
- Guest expiry, and a page on what playhold keeps about players, where and for how long.
  `ServerConfig.HidePlayerIDs` leaves player IDs out of logs and traces.
- This documentation site: a walkthrough, concept pages, guides, and references for the configuration, the errors,
  both SDKs and the wire protocol, with a page on what a server needs from whatever runs it, and what to back up.
- The sample: profiles of the server during a `t12.sh` run, and `check.sh`, the check at 1,000 players over TLS, with
  the bot's `-ca`, `-rejoin` and `-spread`.

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
