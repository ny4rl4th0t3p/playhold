# Rooms and turns

A room is one game: its players, its state, and the code that changes it. Each room runs on a goroutine of its own and
handles its work one piece at a time, so game code never needs a lock, and two rooms never wait for each other.

## A room type

A game describes its rooms once, as a room type built from modules. The sample game's:

<<< ../../examples/cards/cards.go#room-type

`NewRoomType` registers every module and checks how they fit together: a duplicate module name, state or message, a
keeper nobody wired in, or a module needed but missing. It reports every problem at once, so a wiring mistake stops the
server at startup instead of failing in the middle of a game. A server offers one room type, and every room it creates
is of that type.

`RoomConfig` also takes `Init`, which runs once before a new room starts, after its modules' state is created, and
`Close`, which runs after a room's last turn with the reason it closed, but not after a panic that a restore follows, nor
after a stuck turn. An error or a panic in `Init` means the room is never created.

## Turns

Everything that happens to a room arrives as an input in its queue: a player's message, a player taking a seat or
dropping their connection, a timer coming due. The room takes them one at a time, and each is a **turn**:

1. The input runs: for a message, its module's handler.
2. The events emitted along the way are delivered, in the order of the room type's modules, including any those
   handlers emit in turn. See [Modules, keepers and events](./modules.md).
3. The sender gets the handler's error, if any, which is its answer.
4. The room is saved, if the server has a store, and every connected player gets their view. See
   [Views](./views.md). A turn that can't have changed the game, such as a player's connection dropping, skips this
   step.

A room's queue holds 32 inputs by default (`RoomConfig.QueueSize`). A message that arrives while it's full is refused
with `QUEUE_FULL`, and the room carries on.

## What game code can read

Handlers, events and views get a `RoomContext`, valid only during the call it was passed to:

- `rc.Player()` is the player whose input this is, or the player a view is for. It's empty in a turn a timer started.
- `rc.Players()` is the room's seated players, in the order they arrived.
- `rc.Now()` is the room's clock, which stays the same for the whole turn.
- `rc.Rand()` is the room's random numbers, from a seed the room was created with.
- `rc.CloseRoom()` closes the room after this turn.

Game code reads the time and random numbers only through these, so the same moves from the same seed always give the
same game: [Record and replay](../guides/recording.md) and [Test a game](../guides/testing.md) depend on it, and
`playholdvet` flags code that reads the real clock, another source of randomness, or a map in its random order.

## When a turn goes wrong

- **A handler returns an error:** only that player hears it, and the room carries on. Refusing a move is the game
  working, not a failure.
- **A handler panics:** only its room closes. With a store, a copy is restored from its last save, and its players
  carry on in it. See [When a handler panics](../guides/keeping-games-running.md#when-a-handler-panics).
- **A turn runs too long:** the watchdog closes the room. See
  [Slow and stuck turns](../guides/keeping-games-running.md#slow-and-stuck-turns).
