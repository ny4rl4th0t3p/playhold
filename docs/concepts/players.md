# Players and connections

A player is a guest: an ID and a secret, made by the server the first time a client connects, and kept by the client.
A player's seat belongs to that ID, not to a connection, so a player whose connection drops is still in the game, and
gets their seat back when they reconnect.

## Saying hello

A client's first frame is a Hello. A new client sends it without an identity, and the server creates a guest and
answers with a Welcome holding the new ID and secret. A returning client sends its ID and secret, and the server checks
them against the hash it keeps. The TypeScript SDK keeps them in the browser's `localStorage` by default.

The Hello also says which version of each module's messages the client was built with. A client older than a module
accepts is told to update, before it joins anything; see `Registry.ClientSchema`.

A player has one connection at a time: a second connection with the same identity, such as another tab, takes over, and
the older one is told it was replaced.

## Seats

A player joins a room by its code and asks for a seat. A new seat can be refused: the room is full, the server is
draining or overloaded, or the game's `OnJoinAttempt` says no. See
[Who takes a seat](../guides/finding-a-game.md#who-takes-a-seat). A player who already has a seat is never refused,
so they can always come back to it.

- A new seat emits `PlayerJoined` to the room's modules. A seated player reconnecting doesn't.
- `rc.Players()` lists the seated players in the order they arrived.
- A seat stays when its player's connection drops. The game decides what being away means.

## Disconnecting and coming back

When a seated player's connection drops, the room stops sending them views, and their grace period starts:
`ServerConfig.DisconnectGrace`, a minute by default.

- If they're back within it, the game hears nothing: a dropped connection that recovers is invisible to it.
- If not, the room emits `PlayerDisconnected`. They keep their seat; the sample, for example, shows the other player a
  message, and lets the turn timer move the game on.
- When they come back after that, the room emits `PlayerReturned`.

After a restart, the grace period starts again for every seated player who wasn't already away, since the restart is
what cut everyone off.

The TypeScript SDK reconnects by itself, with the same identity, and joins the same room again. The Go SDK doesn't, by
design, so a load test sees every dropped connection: a bot connects again and joins the same code itself.

## Rooms nobody uses

Two timeouts close rooms whose players are gone: `EmptyRoomTimeout`, when nobody has been connected for 5 minutes, and
`InactiveRoomTimeout`, when no player has sent anything for 30 minutes, connected or not. See
[Timeouts](../guides/keeping-games-running.md#timeouts).

What playhold keeps about guests, and for how long: [What playhold keeps about players](../guides/privacy.md).
