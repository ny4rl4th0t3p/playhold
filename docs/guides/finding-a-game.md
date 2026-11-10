# Find a game

Players meet in rooms. A room has a code, and anyone with the code can ask to join it. This page covers how rooms get
their codes and how a game decides who takes a seat.

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
up the whole room. A game locks its room by refusing everyone new, such as once its game has started. A player who already has a seat is
never asked again, so they can always come back to it.
