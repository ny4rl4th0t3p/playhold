# Saving and timers

A room's state lives in memory while it runs. With a store, the server also saves it, so a game survives a restart, a
crash or a deploy, and timers are saved with it, so they survive too.

## Snapshots

A module says how its state is saved with `playhold.Snapshot`: a version, starting at 1, and two functions that turn the
state into a Protobuf message and back. The state itself stays a plain Go struct. The sample's `deck` module, in
[Modules, keepers and events](./modules.md#a-module), saves with `playhold.Snapshot(r, key, 1, save, load)`.

- With a store, the server saves each room after every change, in the background, so a room never waits for the
  database. `ServerConfig.SnapshotInterval` saves busy rooms less often.
- A server with a store refuses to start if any module's state has no snapshot, so a game can't quietly lose part of
  itself at a restart.
- When the server starts, it restores every saved room, through each module's `load`. Players reconnect and find their
  game as it was.

How the server saves, restores and stops: [Keep games running](../guides/keeping-games-running.md).

## Changing what's saved

A deploy that changes how a module saves its state raises its snapshot version, and declares a migration from each
earlier version to the next with `playhold.Migrate`. A room saved by the old server goes through each step in order when
the new one restores it. The sample's `turns` module has a version 2, which keeps the current player by ID instead of by
position, behind a setting so the change can be tried on a running game:

<<< ../../examples/cards/turns/turns.go#versions

<<< ../../examples/cards/turns/turns.go#migration

- Every version needs its step, from 1 up to the current one, so a server can read a game saved by any earlier release.
  `NewRoomType` fails if one is missing.
- If a step returns an error or panics, that room isn't restored, and its saved state stays as it was, for a fixed
  server to try again.
- A migration works on the encoded bytes, so it doesn't depend on Go types the new code no longer has.

These versions are about saved state. The versions of a module's messages and views, which decide whether an old page
can still play, are separate: `Registry.ClientSchema` declares those.

## Timers

`rc.After` emits an event after a while, in a turn of its own, to the modules subscribed to it. The sample's `turns`
module sets a turn timer, cancels it when someone wins, and closes the room a while after the game ends:

<<< ../../examples/cards/turns/turns.go#timers

- The event has to be a Protobuf message, because it's saved with the room.
- `rc.After` returns a `TimerID`, which a module keeps in its state to cancel the timer with `rc.CancelTimer`, or to
  show a countdown with `rc.TimerLeft`.
- A turn a timer started has no player: `rc.Player()` is empty.
- When the server comes back after a restart, `ServerConfig.TimersAfterRestart` decides what pending timers do: pause
  for the time it was down, which is the default, keep their original times, or be dropped. See
  [Saving and restoring](../guides/keeping-games-running.md#saving-and-restoring).
