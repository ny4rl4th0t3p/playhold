# Errors and close reasons

The server answers a frame it didn't handle with an `Error`: the frame's `seq`, an `ErrorCode` and a message for logs.
When
a player's room closes, it sends a `RoomClosed` with a `CloseReason`. Both come from
[`frames.proto`](https://github.com/ny4rl4th0t3p/playhold/blob/main/proto/playhold/v1/frames.proto), shown here as it
is.

## Error codes

<<< ../../proto/playhold/v1/frames.proto#error-codes

### What the TypeScript SDK does

Every error reaches the game's `onError`. Some also change what the client does next:

| Code                                        | The client                                                               |
|---------------------------------------------|--------------------------------------------------------------------------|
| `UNAUTHORIZED`                              | Forgets the stored identity and says Hello again as a new guest          |
| `UNSUPPORTED_PROTOCOL`, `UPDATE_REQUIRED`   | Stops, with the status `updateRequired`: the player needs a newer page   |
| `UNAVAILABLE`                               | Disconnects and reconnects with a growing delay, keeping the room's code |
| `RATE_LIMITED`, before or on joining a room | The same as `UNAVAILABLE`                                                |
| Any other error answering a Join            | Forgets the room's code, so reconnecting doesn't try that room again     |

Any other error, such as `REJECTED`, `RATE_LIMITED` in a room, or `TURNED_OFF`, only reaches `onError`: the message
wasn't handled, and the game decides what to tell the player.

The Go SDK returns a server's error as an `*Error`, with its code, from `Join` or `Next`, and a closed room as
`ErrRoomClosed`. What to do next is left to the caller.

## Close reasons

<<< ../../proto/playhold/v1/frames.proto#close-reasons

The TypeScript client stays connected when its room closes, forgets the room's code and calls `onRoomClosed` with the
reason, so the game can offer another room. A room closing for a server shutdown sends nothing: the room comes back on
the next server, and the client reconnects to it.