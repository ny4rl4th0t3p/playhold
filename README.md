# playhold

A self-hosted game backend in Go, built from authoritative rooms and composable modules.

> **Status: early development.** v0.2.0 runs a sample game in a browser, and a game in progress survives a restart, a
> crash and a deploy that changes how games are saved. It isn't ready for a public game yet, and the API can change.

playhold is a hobby project. The first goal is a turn-based web game, public on itch.io, running on playhold. After
that, each release adds support for another kind of game.

**Documentation:** <https://ny4rl4th0t3p.github.io/playhold/>, including rough
[measurements](https://ny4rl4th0t3p.github.io/playhold/measurements.html) from development.

## The idea

- **Authoritative rooms:** each room owns its state and handles messages one at a time, so game code needs no locks.
- **Composable modules:** game logic lives in modules that never import each other. They call each other through small
  interfaces and notify each other with events, so they can be reused across games.
- **Hidden information:** the server builds each player's own view, so a client never receives what its player isn't
  allowed to see.
- **Matches that survive deploys:** rooms are saved and restored, a new version can migrate their saved state, players
  reconnect by themselves, and a client too old for the server is told to update.
- **Reproducible games:** a room's random numbers and clock can be set, so the same moves give the same game, and
  `playholdvet` flags game code that would break that, such as reading the real clock.
- **A server that protects itself:** safe settings by default, rate limits, no new games while it's overloaded, rooms
  stuck in a turn cut off, and a message that keeps crashing it turned off for a while, with a crash journal to find
  out why.
- **Your own server:** designed to run on a small VPS with PostgreSQL.

## Intended features

**First public release**

- [x] Rooms and modules
- [x] TypeScript SDK for web games
- [x] Go SDK for bots and load tests
- [x] Persistence and safe deploys
- [x] Security and abuse protection (bans come with accounts)
- [ ] Matchmaking, private rooms and bots
- [ ] Match replay and test tools

**More kinds of games**

- [ ] Async turn-based
- [ ] Casual real-time
- [ ] Real-time with hidden information
- [ ] Persistent and social
- [ ] Large worlds

**Also planned**

- [ ] Accounts, with bans and admin tools
- [ ] Code generation and scaffolding
- [ ] Unity and Godot SDKs
- [ ] Modules in other languages

## Layout

- Root: the Go server library, imported as `github.com/ny4rl4th0t3p/playhold`
- `sdk/`: libraries a game's client uses to talk to the server, one folder per language
- `store/`: what a server keeps across restarts, with memory and PostgreSQL versions
- `proto/`: the wire format they share
- `examples/cards/`: a sample game, Crazy Eights, with a browser page

## Not planned

Competitive netcode, seamless open-world MMOs and server-side physics.

## License

[Apache-2.0](LICENSE).
