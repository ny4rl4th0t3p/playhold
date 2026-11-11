# Keep games running

A match should outlive whatever happens to the server: a deploy, a crash, a handler with a bug, or a room nobody plays
anymore. This page covers saving and restoring rooms, the timeouts that close rooms, the watchdog, and stopping and
restarting the server.

## Saving and restoring

With a `Store`, such as the PostgreSQL one in `store/postgres`, the server saves each room after
every change, in the background, and brings every saved room back when it starts. Players reconnect to their rooms by
themselves. A room that can't be restored is logged and left in the store, so a fixed server can try again, and it
doesn't stop the others. Without a store, a restart forgets every room and guest.

`SnapshotInterval` saves a busy room less often: a change that comes sooner than the interval waits, and the changes
made meanwhile are saved together. A crash can then lose up to one interval of moves. A shutdown still saves every room.

`TimersAfterRestart` says what pending timers do when the server comes back:

- `TimersPause`, the default: the time the server was down doesn't count, so nobody loses a turn to an outage. It's
  measured from the room's last turn, so after a crash a player can get back a little more time than they had, but
  never less.
- `TimersKeepRunning`: each timer keeps its original time, and any that came due while the server was down fire at
  once, in order.
- `TimersCancel`: every pending timer is dropped, and a module that relies on one has to set it again.

## When a handler panics

A panic closes only its room. With a store, the room is replaced by a copy restored from its last save, with the same
ID, and the players connected to it follow. The message that caused the panic is skipped, and its sender gets an error.
A room that panics again after 3 restores within 10 minutes is closed, and its players are told why. Without a store,
the room just closes.

## Timeouts

Two timeouts close rooms, and a third tells the game a player left:

| Setting               | Default    | What it does                                                                                                                                                                                |
|-----------------------|------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `EmptyRoomTimeout`    | 5 minutes  | Closes a room once nobody has been connected to it for this long                                                                                                                            |
| `DisconnectGrace`     | 1 minute   | How long a seated player has to come back before the game gets a `PlayerDisconnected` event                                                                                                 |
| `InactiveRoomTimeout` | 30 minutes | Closes a room, and deletes its saved state, once no player has sent it anything for this long, connected or not. Timer turns don't count, so a room kept busy only by its timers closes too |

A `Timeout` left at zero keeps the default. `Off` turns
one off, and the server logs it at startup:

<<< ../../example_test.go#timeouts

A room closing doesn't wait for grace periods, so keep `EmptyRoomTimeout` longer than `DisconnectGrace` if the game
cares about those events. After a restart, the grace periods and the inactive time start over.

## Slow and stuck turns

Two settings of the `Runtime`'s `Config` watch how long a turn
takes:

- **`SlowTurn`**, 100 ms by default: a longer turn is logged as `slow turn`, with the message or timer that started it.
  The room carries on.
- **`StuckTimeout`**, 5 seconds by default: the watchdog gives up on a longer turn. The room is closed as stuck and,
  with a store, replaced by a copy from its last save, as after a panic. Go can't stop the stuck goroutine, so nothing
  it does
  afterward reaches players or the store.

## Restart when stuck

A stuck goroutine usually ends by itself, and one that's only blocked costs little memory. One that spins forever takes
CPU from every room, and only a new process gets rid of it. With `RestartWhenStuck`, the server restarts its process
when a stuck room's goroutine is still running 30 seconds after the watchdog gave up on it, while the server has been
overloaded for a minute without a break:

<<< ../../example_test.go#restart-when-stuck

The server then drains for 10 seconds, so a load balancer sends players elsewhere, saves every room, and
`ListenAndServe` returns `ErrRestartNeeded`. The game exits with an error, as `run` does in
[Run in production](./production.md), and the service manager
starts it again, with every game.

It needs [admission control](./limits.md#admission-control) on, to tell when the server is overloaded. Without
`RestartWhenStuck`, the server logs an error every minute while that lasts, for an operator to restart it. A server
that doesn't use `ListenAndServe` watches `Server.RestartNeeded` instead.

## Health checks, drain and shutdown

`ListenAndServe` answers two health checks:

| Path       | Answers                                                                                                                       |
|------------|-------------------------------------------------------------------------------------------------------------------------------|
| `/healthz` | Always 200: the process is up. Restarting the server doesn't help with anything `/readyz` reports                             |
| `/readyz`  | 503 while the server drains, or while the store can't be reached; then new players can't be identified and games aren't saved |

When its context ends, as on SIGTERM in `run`, it stops without losing a match:

1. It drains: no new rooms, seats or places in the matchmaking queue, and `/readyz` fails, so a load balancer stops sending players here. Players with
   a seat can still come back to it.
2. It closes every room, which saves it, and writes everything waiting to be saved.
3. Only then does it disconnect the clients, which reconnect and find their rooms on the next server.

`ShutdownTimeout`, 8 seconds by default, is how long saving may take. Keep it below the time the service manager waits
before killing the process. A server that doesn't use `ListenAndServe`
calls `Server.Drain` and `Server.Shutdown` itself.