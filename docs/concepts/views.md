# Views

A player never receives a room's state. They receive a view: what each module chooses to show that player, built by the
server for them alone. A card another player holds, the order of the draw pile, anything a module doesn't put in a
view, never leaves the server, so a modified client has nothing to find.

## Declaring a view

A module declares its view with `playhold.View`: a function that returns a Protobuf message, with `rc.Player()` as the
player it's for. The sample's `hands` module copies across only that player's own cards, and a count for everyone
else's:

<<< ../../examples/cards/hands/hands.go#view

The `deck` module shows the top card and how many cards are left, and keeps the pile's order to itself:

<<< ../../examples/cards/deck/deck.go#view

- Copy across only what that player may see, and never send the module's state itself: a field added to the state
  later stays private until someone adds it to the view.
- Returning nil shows that player nothing from this module.
- A view only reads. It can't emit events or set timers, which panic, and it must not change the state.
- A module has at most one view.

## When views go out

At the end of every turn, the server builds each module's view for each player who has a seat and a connection, and
sends each of them all their views in one frame. The views are whole each time, not changes since the last one, so a
client only ever shows the latest. A player who joins or comes back gets theirs at the end of that turn.

A turn that can't have changed the game, such as a player's connection dropping, sends no views.

The client decodes each module's view with that module's message type. The TypeScript SDK passes them to the page's
`onState`, and in a Go test, [`playholdtest`](../guides/testing.md) decodes them with `View`.

## Testing what a player sees

A view that leaks something is a bug a player can exploit, so it's worth a test: `playholdtest`'s `Received` returns
every state a player was sent, as encoded bytes, and the sample's tests search each for the names of cards that player
must not see. See [Hidden information](../guides/testing.md#hidden-information).
