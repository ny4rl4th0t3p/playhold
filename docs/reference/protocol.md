# Wire protocol

What a client in any language needs to talk to a playhold server. The SDKs implement it; this page is for writing
another one.

## The connection

- **A WebSocket,** at `ListenConfig.Path`, `/ws` by default, with TLS on a public server. A browser's page is accepted
  from the server's own host and from `ServerConfig.OriginPatterns`.
- **Binary messages only,** each one Protobuf frame: a `ClientFrame` from the client, a `ServerFrame` from the server,
  defined in [`proto/playhold/v1/frames.proto`](https://github.com/ny4rl4th0t3p/playhold/blob/main/proto/playhold/v1/frames.proto).
  A text message closes the connection.
- **Limits:** a client message larger than `ServerConfig.MaxMessageBytes`, 32 KiB by default, closes the connection; a
  client that hasn't said a Hello the server accepted within `HandshakeTimeout`, 10 seconds by default, is
  disconnected; the server pings every 30 seconds, and disconnects a client that doesn't answer within 10. The rate
  limits are in [Limit abuse and load](../guides/limits.md).
- **Replaced:** when the same player connects again, the older connection is closed with status **4001**. A client
  mustn't reconnect after it, or two open tabs would keep replacing each other.

## The frames

<<< ../../proto/playhold/v1/frames.proto#frames

Every client frame has a `seq`, chosen by the client, which the server echoes in any `Error` about that frame, and in
`Joined` and `Queued`. The SDKs number their frames from 1.

## A connection, in order

1. **Hello → Welcome.** The first frame is a `Hello`, with `protocol_version` 1, and the guest's ID and secret, or both
   empty for a new guest. `Welcome` returns the ID, and the secret only to a new guest, once: keep both. The Hello also
   carries `schema_versions`, which version of each module's messages the client was built for. Another protocol
   version gets `UNSUPPORTED_PROTOCOL`, a client too old for a module gets `UPDATE_REQUIRED`, and an unknown or wrong
   identity gets `UNAUTHORIZED`.
2. **Join → Joined.** A `Join` with a room's code, or an empty code for a new room, is answered with `Joined`, carrying
   the room's code, or with an `Error`.
3. **Messages and states.** Once joined, the client sends `ModuleMessage`s, naming a module, an ID local to it and the
   payload, which is that module's own Protobuf message. A message the game handles gets no answer: the `RoomState`
   that follows shows what changed. One the game refuses gets an `Error`.
4. **RoomClosed.** When the room closes for good, `RoomClosed` says why, and is the last frame from that room. The
   connection stays open, and can join another room.

Or, instead of a Join, matchmaking:

- **Queue → Queued**, then **Matched** with a room's code, which the client joins with a `Join` soon, since its seat is
  kept only for a while. `LeaveQueue` stops waiting, and has no answer.

## What a client must expect

- **States are whole,** each one everything the player can see, one `ModuleView` per module, and a later one replaces
  the earlier. A client that reads slowly skips states: the server keeps only the newest one waiting.
- **`Joined` comes before the room's first state,** and `RoomClosed` after its last.
- **When the server stops,** the connection closes with no `RoomClosed`: the room comes back with the next server. The
  client reconnects, says Hello as the same guest, and joins the same code again.
- **A frame that breaks the order,** such as a Join before the Hello, a message before the Join, or a second Join while
  in a room, gets `MALFORMED_FRAME`. So does a frame that can't be decoded, with `seq` 0.
- **A client too slow to take frames that can't be skipped,** such as errors, is disconnected.

What each error code and close reason means, and what the SDKs do with each:
[Errors and close reasons](./errors.md).
