# Go SDK

`sdk/go`, package `sdk`, connects a Go program to a playhold server, for bots and load tests. It's minimal on purpose:
unlike the TypeScript SDK, it doesn't reconnect by itself, because a load test has to see and count what goes wrong. To
come back after a dropped connection, a program connects again with the same identity and joins the same code.

It's part of the playhold module, `github.com/ny4rl4th0t3p/playhold/sdk/go`, used from a clone until it's published.
The sample's bot, `examples/cards/bot`, is a complete client, from load tests to the survival test.

## A client

The sample's bot connects each seat, keeps its identity from game to game, and joins its table's room. A server that
has forgotten the guest answers `ErrorUnauthorized`, and the bot starts again as a new one:

<<< ../../examples/cards/bot/main.go#connect

It then reads each view with `Next`, and sends its moves with `Send`.

## Functions and methods

| Name                         | What it does                                                                          |
|------------------------------|---------------------------------------------------------------------------------------|
| `Connect(ctx, opts)`         | Opens a connection and says Hello, as `opts.Identity`, or as a new guest if it's zero |
| `Identity()`                 | Who this client is: keep it to connect again as the same guest                        |
| `Join(ctx, code)`            | Enters the room with this code, and waits for the answer                              |
| `CreateRoom(ctx)`            | Enters a new room with a code the server makes, and returns the code                  |
| `Queue(ctx, properties)`     | Asks to be matched, and waits until the server accepts                                |
| `Matched(ctx)`               | Waits until the player is matched, and returns the room's code, to `Join` soon        |
| `LeaveQueue(ctx)`            | Stops waiting for a match. The server doesn't answer it                               |
| `Send(ctx, module, id, msg)` | Sends a message to a module of the room, and returns its frame number                 |
| `Next(ctx)`                  | Waits for the player's next whole view of the room, one `ModuleView` per module       |
| `Close()`                    | Ends the connection                                                                   |

A client's methods aren't safe to call at the same time, except that one goroutine can read with `Next` or `Matched`
while another sends. Canceling the context given to `Next` ends the connection.

## Errors

- **`*sdk.Error`**, with `Seq`, `Code` and `Message`, is a problem the server reported. `Connect`, `Join`, `CreateRoom`,
  `Queue`, `Matched` and `Next` return it. After `Next` returns one, the connection can still be used. The codes are
  constants, such as `sdk.ErrorRoomFull` and `sdk.ErrorUnavailable`.
- **`sdk.ErrRoomClosed`**, wrapped with the reason, means the player's room has closed for good. `Join`, `CreateRoom`
  and `Next` return it.
- Any other error is the connection's own, such as the server going away: connect again.

Asked to try again, with `ErrorUnavailable` or `ErrorRateLimited`, a program waits, connects again and repeats, which
is what the TypeScript SDK does by itself. What each code means: [Errors and close reasons](./errors.md).
