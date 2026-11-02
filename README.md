# playhold

A self-hosted game backend in Go, built from authoritative rooms and composable modules.

> **Status: planning.** There is no code yet. Once releases start, the API can change between them.

playhold is a hobby project. The first goal is a turn-based web game, public on itch.io, running on playhold. After that, each release adds support for another kind of game.

## The idea

- **Authoritative rooms:** each room owns its state and handles messages one at a time, so game code needs no locks.
- **Composable modules:** game logic lives in modules that can be reused across games.
- **Hidden information:** each player receives only what they're allowed to see.
- **Matches that survive deploys:** rooms are saved and restored, and players reconnect automatically.
- **Testable game logic:** matches can be replayed exactly, in tests and when debugging.
- **Your own server:** designed to run on a small VPS with Postgres.

## Intended features

**First public release**

- [ ] Rooms and modules
- [ ] Web client
- [ ] Persistence and safe deploys
- [ ] Security and abuse protection
- [ ] Matchmaking, private rooms and bots
- [ ] Admin tools
- [ ] Match replay and test tools

**More kinds of games**

- [ ] Async turn-based
- [ ] Casual real-time
- [ ] Real-time with hidden information
- [ ] Persistent and social
- [ ] Large worlds

**Also planned**

- [ ] Code generation and scaffolding
- [ ] Unity and Godot clients
- [ ] Modules in other languages

## Not planned

Competitive netcode, seamless open-world MMOs and server-side physics.

## License

[Apache-2.0](LICENSE).
