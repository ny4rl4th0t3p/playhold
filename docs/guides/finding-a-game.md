# Find a game

Players meet in rooms. A room has a code, and anyone with the code can ask to join it. This page covers how rooms get
their codes, how a game decides who takes a seat, and how players who don't know each other are matched.

## Room codes

By default, the server makes every room's code. A client asks for a new room by joining with an empty code, and the
server answers with the code it made: 26 random letters and digits, so nobody can guess another room's code or claim a
name. The player shares it, such as in a link, and a friend joins with it.

| A client joins with            | Default                            | With `AllowClientCodes`    |
|--------------------------------|------------------------------------|----------------------------|
| An empty code                  | A new room, with the server's code | The same                   |
| The code of a room that exists | That room                          | The same                   |
| A code no room has             | Refused with `ROOM_NOT_FOUND`      | A new room, with that code |

`ServerConfig.AllowClientCodes` is for a game that picks its own codes, such as a load test. A new code never takes one
in use, by a running room or by a saved room the server couldn't bring back.

In the SDKs, `createRoom` asks for a new room. The TypeScript client reports the code in `onJoined`, and the Go client
returns it. After a lost connection, the client joins the same code again.

## Who takes a seat

A player who joins a room asks for a seat. The server refuses a new seat while it drains or is overloaded, and once the
room has `RoomConfig.MaxPlayers` players. Then the game decides, with `RoomConfig.OnJoinAttempt`, which runs on the
room's goroutine, so it can read the game's state:

- returning nil seats the player;
- returning `ErrRoomFull` refuses them with `ROOM_FULL`;
- returning any other error refuses them with `REJECTED`.

It receives a `JoinAttempt`, which holds the player's ID today, and will gain what playhold learns about players
later, such as their account. It must decide from what's in memory, since waiting on a database or a service would hold
up the whole room. A game locks its room by refusing everyone new, such as once its game has started. A player who
already has a seat is never asked again, so they can always come back to it.

## Matchmaking

With `ServerConfig.Matchmaking` set, players can queue to be matched with others. playhold keeps the queue, runs the
game's matcher, and gives each group it forms a new room. Who plays whom is the game's choice.

1. A client queues, with properties for the player's choices, such as a mode. The server answers `Queued`.
2. Every `Every`, 1 second by default, the server calls the `Matcher` with everyone waiting, in the order they queued.
   It returns groups of players.
3. Each group gets a new room, with a code the server makes, and its free seats kept for the group. Each player's
   client gets `Matched` with the code, and joins it.
4. Once every player has joined, the room's modules get a `MatchReady` event, and the room is like any other from then
   on. If a player hasn't joined after `ReserveFor`, 30 seconds by default, the room closes as `MATCH_INCOMPLETE`, so
   those who joined can queue again, unless `MinPlayers` have joined.

Without its own `Matcher`, the server makes groups of `RoomConfig.MaxPlayers` in the order players queued. Players
left over wait for more, however long that takes, unless `FillFor` is set: see
[Starting without everyone](#starting-without-everyone).

### What the matcher sees

The matcher gets a `Candidate` for each player waiting:

| Field        | What it holds                                                                        |
|--------------|--------------------------------------------------------------------------------------|
| `Player`     | The player's ID                                                                      |
| `Properties` | What the client sent with its Queue, such as a mode                                  |
| `Info`       | What `Matchmaking.Describe` returned for the player, such as their level or region   |
| `Waiting`    | How long the player has waited, so a matcher can widen its rules as time goes on     |

A client can send any properties, so use them only for the player's choices, never for what has to be trusted, such
as a level. That comes from `Describe`: the game's own function, which reads the game's own data. It runs once, when
the player queues, on their connection's goroutine, outside any room, so it may wait on a database. If it fails, the
Queue is refused with `UNAVAILABLE`.

The queue holds at most one place per open connection, so never more than `ServerConfig.MaxConns` players. The matcher
runs over all of them, on a goroutine of its own, one run at a time. It may take longer than `Every`: runs never
overlap, the next one starts when it's done, and rooms carry on meanwhile, so a slow matcher only makes players wait
longer. A game whose matcher is expensive sets `Every` to how often it's worth running.

A matcher's groups must hold players who are waiting, each in one group only, at least `MinPlayers` and no more than
`MaxPlayers` in a group. An empty group is skipped. Otherwise the whole result is dropped and logged as a bug in the matcher, and nobody is matched until its next
run. A panic in the matcher is logged the same way.

### Starting without everyone

`Matchmaking.MinPlayers` lets a match go ahead when some players never come. When `ReserveFor` ends with at least
`MinPlayers` of the group in the room, the room carries on, the missing players' seats are freed, and `MatchReady` goes
out with the players who joined and how many are missing. Zero, the default, needs the whole group. It must be no more
than `MaxPlayers`; `NewServer` refuses a config where it is.

`MatchReady` goes out once per matched room. A room made by code never gets it, and neither does one restored after a
restart before it went out, so a game that waits for it also needs a rule of its own, such as a timer.

`Matchmaking.FillFor` makes the default matcher start smaller groups. After the full groups, the players left over
become a group of their own once there are at least `MinPlayers` of them and the first of them to queue has waited
`FillFor`. Zero, the default, makes full groups only. It needs `MinPlayers`, and a game with its own `Matcher` decides
with `Candidate.Waiting` instead, so `NewServer` refuses `FillFor` without `MinPlayers` or with a `Matcher`.

A game for up to 4 players that starts with 2:

- `RoomConfig.MaxPlayers` is 4, `Matchmaking.MinPlayers` is 2, and `Matchmaking.FillFor` is how long 2 or 3 players
  wait for a fourth, such as 20 seconds;
- it starts its game on `MatchReady`;
- its `OnJoinAttempt` returns `ErrRoomFull` once the game has started, so the freed seats stay empty.

### Saving what a game learns

A game that matches by level has to save each player's level somewhere `Describe` can read it. playhold doesn't save
player data yet, so the game saves it itself, such as from its `Close` hook. That write leaves the room: if the server
crashed just after it, the room would come back from its last save, before the game ended, while the level stays
saved. playhold will cover this later, with writes saved together with the room.

### Leaving the queue

A player leaves the queue by joining a room, by sending `LeaveQueue`, or by losing their connection. The queue lives in
the server's memory, so a restart empties it, and a client has to queue again when it reconnects.

### In the SDKs

The TypeScript client's `queue(properties)` asks for a match, and its status becomes `queued` once the server
accepts. When it's matched, it joins the room by itself, and `onJoined` reports the code. It queues again by itself,
with the same properties, after a lost connection and when its room closes as `MATCH_INCOMPLETE`. `leaveQueue` stops
looking.

The Go client's `Queue` asks for a match, `Matched` waits for one and returns the code, and the caller joins it with
`Join`. It does nothing by itself, so after a lost connection or `MATCH_INCOMPLETE`, the caller queues again.
