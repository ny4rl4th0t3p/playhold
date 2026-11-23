# Godot SDK

[playhold-sdk-godot](https://github.com/ny4rl4th0t3p/playhold-sdk-godot) connects a Godot game to a playhold server, in
GDScript, so it runs in every export: the guest's identity, joining a room or the matchmaking queue, sending messages
and receiving views. It connects again by itself after a lost connection, and goes back to its room.

It isn't in the Asset Library yet. Download the release's zip from GitHub,
[`v0.5.0`](https://github.com/ny4rl4th0t3p/playhold-sdk-godot/archive/refs/tags/v0.5.0.zip), and copy its
`addons/playhold/` into the game's own `addons/`. With git, a clone at the tag does the same:

```bash
git clone --branch v0.5.0 https://github.com/ny4rl4th0t3p/playhold-sdk-godot.git
```

Then it adds `addons/playhold/client.gd` as an autoload, in Project Settings, Globals, Autoload, so the connection
outlives a change of scene. The client's class is `PlayholdClient`, and every method and signal below is also in the
editor's help, under that name.

An SDK `v0.X.y` works with any playhold `v0.X.*`, and is tested with Godot 4.7.2.

## The game's messages

The game's own messages are protobuf, as the frames are. [Godobuf](https://github.com/oniksan/godobuf) generates
GDScript from the game's `.proto` files. With its `addons/godobuf/`, at the commit the SDK's `Makefile` uses, copied
into the project, it runs headless:

```bash
godot --headless -s addons/godobuf/godobuf_cmdln.gd --input=proto/tictactoe/v1/tictactoe.proto --output=tictactoe.gd
```

Enum values lose their prefix: `MARK_X` in the `.proto` is `Mark.X` in GDScript, as in the sample's code below.

## A client

The SDK's sample, `sample/main.gd` in [its repository](https://github.com/ny4rl4th0t3p/playhold-sdk-godot), is the
tutorial's tic-tac-toe, a complete client. It connects its handlers to the autoload, named `Playhold`, then connects:

<<< ../.godot-sdk/sample/main.gd#client{gdscript}

[A Godot client](../tutorial/a-godot-client.md), in the tutorial, goes through the whole of it.

## Settings

Exported, so they can be set in the inspector or in code, before `connect_to_url`:

- **`schema_versions`**, `{}` by default: which version of each module's messages the game was built for, sent in its
  Hello.
- **`identity_path`**, `"user://playhold_identity.cfg"` by default: where the guest's identity is saved, as a
  `ConfigFile`. An empty path keeps it in memory only.
- **`min_retry_delay`**, `0.5` by default: the first retry's delay after a lost connection, in seconds, doubling each
  time.
- **`max_retry_delay`**, `15.0` by default: the longest delay between retries, in seconds.

Each delay is a random 50-100% of that, so players dropped together don't all come back at once.

## Methods

- **`connect_to_url(url) -> Error`:** connects, and connects again after a lost connection until `close()`. From
  `REPLACED`, `CLOSED` or `UPDATE_REQUIRED`, it starts again. `ERR_ALREADY_IN_USE` while connecting, connected or
  waiting to retry.
- **`join(code) -> Error`:** enters the room with this code, now or once connected. `ERR_ALREADY_IN_USE` in another room
  or joining one.
- **`create_room() -> Error`:** enters a new room, with a code the server makes, which arrives in `room_joined`.
- **`queue(properties := {}) -> Error`:** asks to be matched, with the player's choices for the game's matcher, sent as
  Strings; once matched, joins the room by itself. `ERR_ALREADY_IN_USE` in a room or joining one.
- **`leave_queue()`:** stops waiting for a match.
- **`send(module, id, payload) -> int`:** sends a message to a module, its `payload` that module's own message in bytes.
  Returns its frame number, which `server_error` carries if the server refuses it, or 0 outside a room, when nothing
  was sent.
- **`close()`:** disconnects, stops connecting again, and forgets the room and the queue. The identity is kept for next
  time.
- **`status`** and **`player_id`:** the current status, and the player's ID once the server accepted them.

A message sent while the connection is down isn't kept or sent later: when the player is back, the full view they get
shows where the game stands.

## Statuses

`PlayholdClient.Status`:

- **`DISCONNECTED`:** not connected; retrying, unless `connect_to_url` hasn't been called yet.
- **`CONNECTING`:** opening a connection and saying Hello.
- **`CONNECTED`:** the server knows this player, who isn't in a room.
- **`QUEUED`:** waiting to be matched.
- **`JOINED`:** in a room; messages can be sent.
- **`REPLACED`:** the player connected somewhere newer. Stopped until `connect_to_url`.
- **`CLOSED`:** stopped by `close`.
- **`UPDATE_REQUIRED`:** the game is too old for the server, and has stopped. The player needs a newer one.

## Signals

- **`status_changed(status)`:** each change of status.
- **`room_joined(code)`:** each time the client enters its room, with its code, including after a reconnect.
- **`views_received(views)`:** the player's whole view of the room, a Dictionary from each module's name to its view,
  as that module's own message in bytes.
- **`room_closed(reason)`:** the room closed for good, with one of `Frames.CloseReason`. The client stays connected,
  and can join or queue again.
- **`server_error(seq, code, message)`:** a problem the server reported: `seq`, the number `send` returned, one of
  `Frames.ErrorCode`, and a message for logs and developers.

`Frames` is `addons/playhold/frames.gd`, the frames generated from playhold's proto, also `PlayholdClient.Frames`.

## What it does by itself

- **After a lost connection,** it connects again, waiting longer each time, says Hello as the same guest, and joins the
  same room again, or queues again if it was waiting for a match.
- **If the server has forgotten the guest,** such as after it expired, it forgets the identity and says Hello as a new
  guest.
- **If a matched room closes because a player never came,** it queues again with the same properties, unless the game
  joined or queued for something else in its `room_closed` handler.
- **If a room refuses it,** it forgets the code, so connecting again doesn't keep trying that room.
- **It never connects again after `REPLACED`,** or two copies of the game would keep taking it from each other.
- **It keeps running while the game is paused:** its `process_mode` is `PROCESS_MODE_ALWAYS`.

What it does with each error: [Errors and close reasons](./errors.md).

## A web export

A web export connects from a page, so the page's address goes in the server's
[web origins](../guides/production.md#web-origins), and a public server uses TLS, as in
[Run in production](../guides/production.md).

What Godot's web export itself needs is on Godot's
[web export page](https://docs.godotengine.org/en/stable/tutorials/export/exporting_for_web.html): it only supports the
Compatibility renderer, and exports on a single thread by default since Godot 4.3; with thread support on, the page's
server must also send cross-origin isolation headers.
