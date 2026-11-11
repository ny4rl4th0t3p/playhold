# Record and replay

A recorded room writes every turn to a file: what started it, the room's clock, how it ended, and a hash of the room's
state after it. With that file and the game's code, a test plays the game again turn by turn, and says at which turn,
if any, it stopped matching. This page covers turning it on, what a recording holds, and replaying one.

The crash journal answers "what was running when the server died", for the last turns of every room. A recording
answers "how did this game get here", for every turn of one room.

## Turn it on

The game turns it on for a room type, with `RoomConfig.Record`, and the server says where the files go, with
`ServerConfig.Recording`:

<<< ../../example_test.go#recording

- `NewRoomType` refuses `Record` if a module's state has no snapshot, since a turn's hash covers every module's state,
  or if `Name` isn't a folder name: letters, digits, `-` and `_`.
- `NewServer` refuses a recorded room type without `Recording.Dir`, so a game that asks for recordings never quietly
  gets none.

Each room type's recordings go in a folder of their own, named after it, with one file per room, named by its ID:
`/var/lib/game/recordings/match/<room ID>.rec`. The files are readable only by the server's user.

`Retention` deletes a recording 30 days after its last turn by default, checked every hour, so a long game keeps its
whole history while it runs. `Off` keeps every recording, and the server logs a warning.

## What it costs

A recorded room encodes its state after every turn, to hash it, unless the turn's save already did; and every turn's
input goes to disk. The disk use is about the size of the inputs: a card game's moves are tens of bytes. The server
writes in the background every 100 ms, so a room never waits on the disk. If the disk falls more than 8 MiB behind, new
records are dropped and logged, and the recording marks the gap: a replay stops there rather than report a difference
that isn't one.

## What a recording holds

Every input a room's turns start from, in order, each with the room's clock:

| Input                              | Recorded with                                                |
|------------------------------------|--------------------------------------------------------------|
| A player's message                 | The player, the module, the message ID and the whole payload |
| A seat, or a player coming back    | The player; a refusal while draining or overloaded           |
| A player's connection dropping     | The player, whose grace period starts                        |
| A timer firing                     | The timer                                                    |
| A grace period ending              | The player                                                   |
| Matchmaking's reservation, its end | The matched players and `MinPlayers`                         |

After each turn come its error, if it had one, whether it panicked, and the start of a SHA-256 of the room's state.
Messages the server refused before they reached the room, such as over a rate limit, aren't turns, and aren't recorded.

A recording is in segments, one for each time the room started running. A new room's segment starts from its seed and
the clock its `Init` ran at; a room restored after a restart, a deploy or a crash starts from its state as restored.
Each
segment names the build that ran it: a hash of the executable, and the version control revision Go stamped into it,
empty for a build that has none.

A turn that got stuck isn't recorded, since it never ended: the crash journal has it. A record cut off at the end of
the file, by a machine failure or a copy taken while the room ran, is ignored, with everything before it still read.

A recording holds every player's message in full, like the crash journal, and `HidePlayerIDs` doesn't apply to it,
since a replay needs each turn's player. See [What playhold keeps about players](./privacy.md).

## Replay one

Copy the recording from the server, check out the code of the revision it names, and replay it in a test with
`playholdtest.Replay`, against the game's room type. The sample game's tests record a game on a real server, then:

<<< ../../examples/cards/server_test.go#replay

The room plays every turn again, at its recorded clock, with timers firing as recorded: a timer the recording fired
has to be pending, or that's a difference. `Replay` fails the test at the first turn that doesn't end as recorded, with:

- which segment and turn, and that turn's input;
- what differs: the error, a panic, a timer that isn't pending, or the room's state, with both hashes.

From there, a test of its own can play up to that turn with `playholdtest` and look at the state.

`TryReplay` returns the same report without failing the test. Its `SchemaMatches` says whether the room type's
modules save at the snapshot versions the recording's did; replaying against other code is a best effort, and the
report shows where it parts. A recording with a gap fails `Replay`, since part of the game wasn't checked.

`playhold.ReadRecording` reads a recording's segments and turns, for a tool of the game's own.
