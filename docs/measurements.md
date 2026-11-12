# Measurements

::: warning Rough numbers
These are development references, not a benchmark. They come from one small machine and help spot a change that makes
the server slower. A proper measurement comes later.
:::

The sample game playing 500 games at once, 1,000 connections, at 3,000 moves a second in total, with saving off and
on. The game server runs in a Docker container limited to 1 CPU and 1 GB of memory, about a small VPS, and PostgreSQL
in another with the same limits. The load bot runs in a third, all on one 4-core virtual machine.

| Measure                           | Saving off   | Saving on     |
|-----------------------------------|--------------|---------------|
| Half of the moves answered within | About 0.6 ms | About 0.6 ms  |
| 99% of the moves answered within  | About 3.5 ms | About 9.5 ms  |
| Game server CPU, of one core      | About 27%    | About 39%     |
| Game server memory                | About 100 MB | 105 to 120 MB |
| PostgreSQL CPU, of one core       |              | About 20%     |

A move's time runs from when it was due to when the bot got the view it caused, so it includes the bot and the network
between the containers. The 99% figure varies from run to run, with or without saving.

## Recording every game

The same load, with every game recorded:

| Measure                          | Saving off                          | Saving on                               |
|----------------------------------|-------------------------------------|-----------------------------------------|
| Game server CPU, of one core     | About 34%, from about 27%           | About 42%, from about 39%               |
| Game server memory               | Rising to about 140 MB, from 100 MB | Rising to about 140 MB, from 105–120 MB |
| 99% of the moves answered within | About 7 ms, from about 3.5 ms       | About 10 ms, from about 9.5 ms          |
| Recordings written               | About 16 MB a minute                | About 16 MB a minute                    |

The game server's memory rose through each recording run, and was still rising at its end; why isn't known yet. The
recordings come to about 4.5 KB for each game, and take about 1.4 times that on disk, since each file takes whole
blocks. At this load, the default retention of 30 days would keep about 0.7 TB of recordings; the default `MaxSize`
keeps them to 10 GiB, about the last 11 hours.

With `MaxSize` lowered to 64 MiB, so that a cleanup ran about every 25 seconds over about 15,000 files, each cleanup
took about half a second, and the moves' latency stayed as it was with no cleanup at all.

## Tracing every turn

The same load, with every turn and store call traced and the spans dropped, measured when tracing was added, against
runs of the same code without it:

| Measure                          | Saving off                    | Saving on                      |
|----------------------------------|-------------------------------|--------------------------------|
| Game server CPU, of one core     | About 29%, from about 26%     | About 41%, from about 38%      |
| 99% of the moves answered within | About 5 ms, from about 3.3 ms | About 10 ms, from about 7.4 ms |

## 1,000 players over TLS

The check a release passes for turn-based games: 500 games of the sample, 1,000 players, each moving every 2 to 8
seconds, 5 on average, so about 200 moves a second in total. TLS is on, with a test certificate the bot trusts, and
each change is saved straight away (`SnapshotInterval` 0). Same containers and limits as above; 3 runs with saving off
and 3 on.

| Measure                            | Saving off   | Saving on    |
|------------------------------------|--------------|--------------|
| Half of the moves answered within  | About 0.9 ms | About 0.9 ms |
| 99% of the moves answered within   | About 1.8 ms | About 1.9 ms |
| 99.9% of the moves answered within | About 3 ms   | About 3.2 ms |
| Game server CPU, of one core       | About 6.5%   | About 10%    |
| Game server memory                 | About 118 MB | About 119 MB |
| PostgreSQL CPU, of one core        |              | About 8.5%   |
| Longest snapshot delay             |              | 39–50 ms     |

The game server sends each player about 170 bytes a second while playing, and about 7 while idle, measured on the
container's network, so with TLS, WebSocket and TCP overheads.

- **Idle:** the server alone uses about 4 MB. 1,000 idle connections, opened over 60 seconds, were held for 3 minutes
  with none failing.
- **12 hours** at the same load, saving on: no failures, 99% of the moves within 1.7 ms, and the game server's memory
  between 139.5 and 140.4 MB from the second minute to the last, with CPU at about 10%. The longest snapshot delay was
  143 ms.
- **A restart under load,** 2 minutes in: all 1,000 players back in their games, the slowest after 1.4 seconds, and
  no game lost. For 13 seconds after starting, the server counted itself overloaded and refused 341 joins with
  `SERVER_FULL`.
- **Over capacity:** 2,000 players with room for 600 games. Every refused game got `SERVER_FULL` and nothing crashed;
  for the players who got in, 99% of the moves were answered within 4.2 ms. The game server's CPU rose to about 54%,
  with the refused players trying again every second, and one snapshot was 0.9 s late.

To run the same measurements, see `t12.sh` in the
[sample game](https://github.com/ny4rl4th0t3p/playhold/tree/main/examples/cards#measure-what-saving-costs), with
`RECORDING=/var/lib/cards/recordings` for the recording runs and `TRACES=measure` for the tracing ones, and `check.sh`
for [the 1,000-player check](https://github.com/ny4rl4th0t3p/playhold/tree/main/examples/cards#run-the-turn-based-check).
