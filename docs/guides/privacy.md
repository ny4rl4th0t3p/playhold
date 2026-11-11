# What playhold keeps about players

A game on playhold decides what its own modules store. This page covers what playhold itself keeps: where, for how
long, and what the game's operator can change. It's here to help write a game's privacy notice; it isn't legal advice,
and which rules apply depends on where the game runs and who plays it.

Players are guests: a random ID and a secret the client keeps. playhold asks for no name, email or password. The guest
ID is still personal data where it can be linked to someone, such as through an IP address.

## Where it's kept

| Where                | What about players                                                                                          | For how long                                                                 | Changed with                                      |
|----------------------|-------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------|---------------------------------------------------|
| The store: guests    | Each guest's ID, a hash of their secret, when they were created and when last seen                          | Until unseen for `GuestExpiry`, 90 days by default                           | `GuestExpiry`                                     |
| The store: rooms     | Each running room's saved state: its seated guests' IDs, and the game's own state                           | Until the room closes, when its save is deleted; a restart keeps it          | `EmptyRoomTimeout`, `InactiveRoomTimeout`         |
| The store: incidents | The ID of the player whose turn crashed the server or hung a room                                           | 7 days, or a new build; deleted when the server next starts                  |                                                   |
| Logs                 | Guest IDs on lines about a connection or a turn; IP addresses on TLS refusals and failed TLS handshakes     | As long as the game's logging keeps them                                     | `HidePlayerIDs`, `Logger`                         |
| Metrics              | Nothing about players                                                                                       | As long as the monitoring keeps them                                         |                                                   |
| Traces               | Guest IDs on turns' spans                                                                                   | As long as the tracing backend keeps them                                    | `TracerProvider`, off by default; `HidePlayerIDs` |
| The crash journal    | Each turn's guest ID and its message up to `EntrySize`, which can be anything a player typed                | Overwritten as new turns come; the file stays until deleted                  | `CrashJournal`, off by default                    |
| Recordings           | Every turn of a recorded room: the guest IDs in it and every message in full                                | `Retention` after its last turn, 30 days by default; sooner past `MaxSize`   | `Record`, `Recording`; off by default             |
| The server's memory  | IP addresses, for the rate limits per address                                                               | Forgotten once an address has been quiet for a while; never written anywhere | The rate limits                                   |
| The player's browser | Their guest ID and secret, in `localStorage` with the TypeScript SDK's defaults                             | Until the player clears the site's data                                      | The SDK's `storage` option                        |

A saved room the server couldn't bring back, such as one of another room type, stays in the store until a fixed server
restores it or someone deletes it.

## What the operator chooses

- **How long guests last.** `GuestExpiry` deletes a guest once they haven't connected for that long; the server checks
  every hour. A player who comes back later starts again as a new guest. Choose the shortest time that still suits the
  game: data protection rules such as the GDPR ask to keep personal data no longer than needed, without setting a
  period.
- **Logs.** playhold logs through `ServerConfig.Logger`, and rooms through the runtime's `Config.Logger`, and keeps
  nothing itself. Rotate and delete logs on a schedule,
  as for any server. With `HidePlayerIDs`, no log line or span carries a guest ID.
- **The crash journal.** It holds players' messages, cut at `EntrySize`, so turn it on only where it's needed, and keep
  its file, and any copy of it, as private as the database.
- **Recordings.** They hold players' messages in full, and `HidePlayerIDs` doesn't apply to them, since a replay needs
  each turn's player. Keep their folder, and any copy of a recording, as private as the database, and
  set `Retention` to as long as the game needs to look into a reported game.
- **Backups** of the database keep everything above for as long as the backups are kept.

## What a game adds

Anything a game's modules keep in their state is saved with the room, and goes when the room's save does. Anything a
game stores elsewhere, such as players' levels in its own database, follows the game's own rules, not these.
