# Modules, keepers and events

A game is a list of modules. Each owns one part of the game, its state and the messages that change it, and none
imports another, so a module written for one game can be used in the next. They work together in two ways: a module
calls another through a **keeper**, and tells the others what happened with an **event**.

## A module

A module has a name, unique in its room type, and a `Register` method that declares everything it does. The sample's
`deck` module, which owns the piles of cards:

<<< ../../examples/cards/deck/deck.go#module

- **`playhold.NewKey`** makes the key to the module's state, once, in a package-level variable. The key stays
  unexported, so only this module can reach its state.
- **`playhold.State`** gives each room its own copy of the state, from the function given, when the room is created.
- **`key.Get(rc)`** returns this room's state, from any handler, event, keeper or view.
- **`playhold.Snapshot`** says how the state is saved. See [Saving and timers](./saving.md).
- **`playhold.On`** subscribes to an event, and **`playhold.View`** declares what each player sees. See
  [Views](./views.md).

## Messages

`playhold.Handle` declares a handler for one of the module's messages, by an ID local to the module, with the message
type it decodes:

<<< ../../examples/cards/hands/hands.go#register

A message from a client names its module and that ID, and its payload is a Protobuf message. The handler gets it
decoded, and `rc.Player()` is who sent it:

<<< ../../examples/cards/hands/hands.go#play

An error the handler returns reaches only the sender, as a `REJECTED` error with its text, so it mustn't say anything
that player may not see. Nothing in the room changes because of it unless the handler changed it first: check
everything, then change the state. A message that works gets no answer of its own; the sender sees what changed in the
view that follows. A message no module handles is refused with `UNKNOWN_MESSAGE`, and one whose payload doesn't decode
with `BAD_MESSAGE`.

## Keepers

A module that needs another declares what it needs as an interface, in its own package:

<<< ../../examples/cards/hands/hands.go#keepers

The providing module offers a type with those methods, here `deck.Keeper`, without knowing who uses it:

<<< ../../examples/cards/deck/deck.go#keeper

The game wires them together where it lists its modules, the only place that sees all of them: `hands.Module{Deck:
deck.Keeper{}, Turns: turns.Keeper{...}}` in the sample's room type, shown in
[Rooms and turns](./rooms.md#a-room-type). With `r.Needs("deck", m.Deck)`, `NewRoomType` fails if the keeper wasn't
wired in, or if no module named `deck` is in the room type. A keeper the module can do without isn't declared: the
module checks it for nil before calling it.

A keeper runs in the caller's turn, on the room's goroutine, and reaches the provider's state through its own key.

## Events

An event is any Go value that isn't an interface. A module emits it with `rc.Emit`, and every module subscribed to
its type with `playhold.On` gets it, without either one knowing about the other. The sample's `turns` module starts
the game once the room is full:

<<< ../../examples/cards/turns/turns.go#game-started

`deck` and `hands` both subscribe to `GameStarted`, which is defined in a package all three import, `rules`:

<<< ../../examples/cards/deck/deck.go#start

Events are delivered after the handler that emitted them returns, in the same turn, to the modules in the room type's
order. That's why the sample lists `deck` before `hands`: the deck is shuffled before the cards are dealt. Events
emitted while handling events queue behind them. A chain more than 16 events deep panics, so a loop of events fails
loudly instead of running forever.

playhold emits its own events too:

| Event                | When                                                                              |
|----------------------|-----------------------------------------------------------------------------------|
| `PlayerJoined`       | A new player takes a seat; not when a seated player reconnects                    |
| `PlayerDisconnected` | A seated player has been gone for the whole grace period. They keep their seat    |
| `PlayerReturned`     | That player comes back                                                            |
| `MatchReady`         | A matched room's players have joined, or enough of them when the reservation ends |

See [Players and connections](./players.md) and [Find a game](../guides/finding-a-game.md#matchmaking).
