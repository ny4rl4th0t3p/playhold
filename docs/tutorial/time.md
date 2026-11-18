# 6. Time, and a new version

A player who walks away leaves the other waiting forever. This chapter gives each move 30 seconds, and running out
loses the game. Losing on time is a third way for a game to end, so the board saves how it ended differently, and games
saved by chapter 5's server move to the new shape when chapter 6's server restores them: a deploy that changes what's
saved.

The finished code is `chapter6/` in
[playhold-tictactoe](https://github.com/ny4rl4th0t3p/playhold-tictactoe). Its Go paths start with
`github.com/ny4rl4th0t3p/playhold-tictactoe/chapter6`; in your module, they're `example.com/tictactoe`. In a clone of
that repository, `diff -r chapter5 chapter6` shows the whole change.

## Before you change anything

Leave a game saved by this code, for the new code to restore at the end of the chapter. With PostgreSQL running and the
server started as in [chapter 5](./saving.md#play-it-and-stop-it), play a game to a win in two browsers, then stop the
server with Ctrl+C. Leave PostgreSQL running, and the browsers open.

## What changes in the messages

The view gets the time left, and whether the game ended on time. What's saved gets a `Result`, which replaces the
winner and the draw, and the turn's timer. The timer's own event is a message too, because it's saved with the room:

<<< ../.tictactoe/chapter6/proto/tictactoe/v1/tictactoe.proto#view

<<< ../.tictactoe/chapter6/proto/tictactoe/v1/tictactoe.proto#saved

The old fields stay in `BoardState`: a game saved by chapter 5's server has them, and the migration below reads them.

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.75.0 generate
```

## The state

<<< ../.tictactoe/chapter6/tictactoe.go#state

## The turn's timer

`rc.After` emits an event after a while, in a turn of its own, to the modules subscribed to it with `playhold.On`. It
returns a `TimerID`, which the board keeps in its state, to cancel the timer when a move comes in, and to show the time
left:

<<< ../.tictactoe/chapter6/tictactoe.go#timer

- The timer is saved with the room, so it survives a restart. By default, the time the server was down doesn't count,
  so nobody loses a turn to an outage; `ServerConfig.TimersAfterRestart` changes that.
- A turn a timer started has no player: `rc.Player()` is empty in `turnExpired`.
- `PlayerJoined` comes only with a new seat, never with a player coming back, so a reconnect doesn't start the clock
  again.

A move cancels the timer, and starts the next player's, unless it ended the game:

<<< ../.tictactoe/chapter6/tictactoe.go#play

The view shows the time left with `rc.TimerLeft`, counted from the start of the turn the view is for:

<<< ../.tictactoe/chapter6/tictactoe.go#view

`playholdtest` runs the room in fake time, which only moves when the test moves it, with `Room.Advance`:

<<< ../.tictactoe/chapter6/tictactoe_test.go#timer

The chapter's other tests check that a move restarts the time, that waiting for a second player takes nobody's time, and
that the timer comes back after `Room.Restart`.

## A new version of what's saved

What the board saves changed shape, so its snapshot is now version 2, with a migration from version 1:

<<< ../.tictactoe/chapter6/tictactoe.go#register

<<< ../.tictactoe/chapter6/tictactoe.go#snapshot

<<< ../.tictactoe/chapter6/tictactoe.go#migration

- When the server restores a room saved at version 1, it runs the step from 1 to 2 on the saved bytes, then `load`.
  Each later version adds a step from the one before, and a room goes through each in order.
- The migration works on bytes, not on Go types, so it doesn't depend on code that has changed since.
- Every version needs its step, from 1 up to the current one: `NewRoomType` fails if one is missing.
- If a step returns an error, that room isn't restored, and its saved state stays as it was, for a fixed server to try
  again.

A test migrates boards as chapter 5's `save` wrote them, then loads them:

<<< ../.tictactoe/chapter6/snapshot_test.go#migration-test

It's an internal test, in `package tictactoe`, because the migration and `load` aren't exported. It builds version 1's
bytes itself rather than importing chapter 5's code: both chapters register the same Protobuf names, and Go's Protobuf
registry refuses two packages with the same names in one program.

```bash
go test ./...
```

## The countdown

The view says how long was left when it was sent. The page counts on from there, until the next view comes:

<<< ../.tictactoe/chapter6/web/src/main.ts#clock

The server's timer decides: the countdown is only what the player sees, and a page someone changed can't buy time. A
game lost on time says so:

<<< ../.tictactoe/chapter6/web/src/main.ts#text

`index.html` gets a line for it, `<p id="clock">`, under the status.

## Deploy it

The migration is for this: a server with the new code, on a database the old one saved games in. Start the server
with the same `DATABASE_URL` as before, and the page, then reload both browsers soon: a room nobody connects to closes
after 5 minutes.

The game you finished [before changing anything](#before-you-change-anything) is back: the winner sees "You won!", the
other "You lost.", and no cell can be played. The new code reads only `result`, so the game comes back finished only
because the migration turned the old winner into a result. A game saved unfinished comes back with no countdown, since
version 1 saved no timer, and its next move starts one.

Then play a new game, and let a turn run out.

## Next

The board is one module. [Chapter 7](./a-second-module.md) adds a second one, which keeps a score across games.