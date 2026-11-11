# TypeScript SDK

`sdk/ts` connects a web page to a playhold server: the guest's identity, joining a room or the matchmaking queue,
sending messages and receiving views. It reconnects by itself after a dropped connection, and joins the same room
again.

It isn't published yet. A page uses it from a clone of playhold, with a `file:` dependency on the clone's `sdk/ts` in
its `package.json`, as the sample's page does with `"playhold-sdk": "file:../../../sdk/ts"`. The package exports its
TypeScript source, so the page's build compiles it, such as with Vite.

## A client

The sample's page, `examples/cards/web/src/main.ts`, is a complete client. It makes one client for the page, and calls
`client.connect()` once its handlers are set:

<<< ../../examples/cards/web/src/main.ts#client

## Options

| Option                     | Default                   | What it does                                                                      |
|----------------------------|---------------------------|-----------------------------------------------------------------------------------|
| `url`                      | Required                  | The server's WebSocket address, such as `wss://game.example/ws`                   |
| `schemaVersions`           | None                      | Which version of each module's messages the page was built for, sent in its Hello |
| `storage`                  | `localStorage`            | Where the guest's identity is kept                                                |
| `storageKey`               | `playhold.identity`       | The key it's kept under                                                           |
| `minDelayMs`, `maxDelayMs` | 500 and 15,000            | The first reconnect's delay, doubling each time up to the most                    |
| `createSocket`             | The browser's `WebSocket` | Makes the connection; tests pass a fake                                           |
| `random`                   | `Math.random`             | Spreads reconnect delays, so players dropped together don't all come back at once |
| `onStatus`, `onState`, ... | None                      | The handlers, below                                                               |

Where `localStorage` is blocked, such as in a sandboxed iframe, the identity is kept in memory, so the guest still
survives a dropped connection, though not a reload.

## Methods

| Method                      | What it does                                                                                 |
|-----------------------------|----------------------------------------------------------------------------------------------|
| `connect()`                 | Connects, and keeps reconnecting until `close()`. After `replaced`, it takes the player back |
| `join(code)`                | Enters the room with this code, now or once connected. Throws if already in a room           |
| `createRoom()`              | Enters a new room with a code the server makes; the code arrives in `onJoined`               |
| `queue(properties)`         | Asks to be matched; once matched, joins the room by itself. Throws if already in a room      |
| `leaveQueue()`              | Stops waiting for a match                                                                    |
| `send(module, id, payload)` | Sends a message to a module; returns its frame number, or `undefined` outside a room         |
| `close()`                   | Disconnects and stops reconnecting. The identity is kept for next time                       |
| `status`, `playerId`        | The current status, and the player's ID once the server accepted them                        |

`send` only sends while the status is `joined`. A message sent while the connection is down isn't kept or sent later:
when the player is back, the full view they get shows where the game stands.

## Statuses

| Status           | Meaning                                                                            |
|------------------|------------------------------------------------------------------------------------|
| `disconnected`   | Not connected, and retrying, unless `connect` hasn't been called yet               |
| `connecting`     | Opening a connection and saying Hello                                              |
| `connected`      | The server knows this player, who isn't in a room                                  |
| `queued`         | Waiting to be matched                                                              |
| `joined`         | In a room; messages can be sent                                                    |
| `replaced`       | The player connected somewhere newer, such as another tab. Stopped until `connect` |
| `closed`         | Stopped by `close`                                                                 |
| `updateRequired` | The page is too old for the server, and has stopped. The player needs a newer one  |

## Handlers

| Handler                | Called with                                                                                    |
|------------------------|------------------------------------------------------------------------------------------------|
| `onStatus(status)`     | Each change of status                                                                          |
| `onState(views)`       | The player's whole view of the room: one entry per module, its bytes that module's own message |
| `onJoined(code)`       | Each time the client enters its room, with its code, including after a reconnect               |
| `onRoomClosed(reason)` | The room closed for good. The client stays connected, and can join or queue again              |
| `onError(error)`       | A problem the server reported: its code, message, and `seq`, the number `send` returned        |

## What it does by itself

- **After a dropped connection,** it reconnects, waiting longer each time, says Hello as the same guest, and joins the
  same room again, or queues again if it was waiting for a match.
- **If the server has forgotten the guest,** such as after it expired, it forgets the identity and says Hello as a new
  guest.
- **If a matched room closes because a player never came,** it queues again with the same properties.
- **If a room refuses it,** it forgets the code, so a reconnect doesn't keep trying that room.
- **It never reconnects after `replaced`,** or two open tabs would keep taking the game from each other.

What it does with each error: [Errors and close reasons](./errors.md).
