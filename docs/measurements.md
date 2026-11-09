# Measurements

::: warning Rough numbers
These are development references, not a benchmark. They come from one small machine and help spot a change that makes
the server slower. A proper measurement comes later.
:::

The sample game playing 500 games at once, 1,000 connections, at 3,000 moves a second in total, with saving off and
on. The game server runs in a Docker container limited to 1 CPU and 1 GB of memory, about a small VPS, and PostgreSQL
in another with the same limits. The load bot runs in a third, all on one 4-core virtual machine.

| Measure                           | Saving off   | Saving on    |
|-----------------------------------|--------------|--------------|
| Half of the moves answered within | About 0.6 ms | About 0.6 ms |
| 99% of the moves answered within  | 3 to 10 ms   | 3 to 10 ms   |
| Game server CPU, of one core      | About 25%    | 35 to 40%    |
| Game server memory                | About 100 MB | About 100 MB |
| PostgreSQL CPU, of one core       |              | About 20%    |

A move's time runs from when it was due to when the bot got the view it caused, so it includes the bot and the network
between the containers. The 99% figure varies from run to run, with or without saving.

To run the same measurement, see `t12.sh` in the
[sample game](https://github.com/ny4rl4th0t3p/playhold/tree/main/examples/cards#measure-what-saving-costs).
