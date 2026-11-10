# Test a game

`playholdtest` runs a room of the game's own room type in a Go test: its modules, events, views and timers run as on a
server, with no server, no network and no real time. This page covers what a test can do with it.

The examples test `counter`, a small module from `playholdtest`'s own tests: players add to a count with message
`msgAdd`, a handler refuses more than 10 with `errTooBig`, its view shows the count, or nothing while it's zero, and
message `msgTimer` sets a timer that adds one a minute later.

## A first test

<<< ../../playholdtest/playholdtest_test.go#views

A test runs inside `playholdtest.Run`, which is `testing/synctest`'s bubble, and makes its rooms there with `NewRoom`.
Each room has a runtime of its own, closed when the test ends.

- `Join` seats a player and connects them, so they get views from then on. Joining again reconnects them, as a client
  coming back does. `TryJoin` returns a refusal instead of failing the test: `ErrRoomFull`, or the error from the game's
  `OnJoinAttempt`.
- `Send` sends a module's message as the player.
- `View` decodes the module's view in the latest state the player received, or returns nil if the module showed them
  nothing.
- `Disconnect` drops the player's connection, which starts their grace period.

Every call returns once the room has nothing left to do: the turn's events have run and its views have been delivered.
A test never waits or counts messages.

## Errors

`Send` returns the handler's own error, so a test checks it with `errors.Is`:

<<< ../../playholdtest/playholdtest_test.go#errors

A message no module handles gets `ErrUnknownMessage`. A turn that panics closes the room, as on a server without a store,
and `Send` returns `ErrRoomClosed`; `Closed` then says `ClosePanic`.

## Time

Time only moves when the test moves it. `Advance` moves it forward, and timers, grace periods and the close for
inactivity fire as they come due, each in its own turn:

<<< ../../playholdtest/playholdtest_test.go#advance

The room has the server's timeouts, with the same defaults, set in `Options`:

| Field                 | Default       | What it does                                                   |
|-----------------------|---------------|----------------------------------------------------------------|
| `DisconnectGrace`     | 1 minute      | Time to come back before the game gets `PlayerDisconnected`    |
| `InactiveRoomTimeout` | 30 minutes    | Closes the room once no player has sent anything for this long |
| `TimersAfterRestart`  | `TimersPause` | What pending timers do in a `Restart`                          |

So `Advance(time.Hour)` closes a room nobody uses, as a server would, unless the test turns that off with
`InactiveRoomTimeout: playhold.Timeout{Off: true}`.

## Setting up a position

`Do` runs a function as a turn of the room, with its `RoomContext`, and views go out after it. A module's own tests, in
its package, reach its state through its key, so they can set up a position instead of playing up to it:

<<< ../../playholdtest/playholdtest_test.go#do

A test outside the module's package can't reach its state, since the key is unexported. It plays the game instead, or
looks for a seed that deals what it needs.

## Restarts and deploys

`Restart` saves the room as a server shutting down would, and brings it back from that save, as the next server
would. Everyone is disconnected and joins again. `RestartAs` brings it back as another room type, as a deploy would,
so a module whose snapshot version changed runs its migrations:

<<< ../../playholdtest/playholdtest_test.go#restart

Both fail the test if a module's state has no snapshot. A room type with snapshots is saved after every change, as with
a store.

## Seeds

A room's random numbers come from `Options.Seed`. Left at zero, the seed is random, as on a server, and a test that fails
logs it, so it can be set in `Options` to play the same game again. Two rooms with the same seed, given the same moves,
play the same game.

## Hidden information

`Received` returns every state a player was sent, each encoded as it goes to a client. A test that looks for something the
player must never see, such as another player's cards, searches these bytes: the sample game's tests check every state
for card names that player can't see, in `examples/cards/cards_test.go`.

## What it doesn't cover

`playholdtest` runs the room, not the server: no handshake, rate limits, admission control or matchmaking. A room never
gets `MatchReady`, which only comes from matchmaking; a test of a game that starts on it emits it with `Do`. A test of
those runs a server, as the sample's `examples/cards/server_test.go` does.