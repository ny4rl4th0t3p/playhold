---
layout: home

hero:
  name: playhold
  text: A self-hosted game backend in Go
  tagline: Authoritative rooms and composable modules, on your own server.
  actions:
    - theme: brand
      text: Run in production
      link: /guides/production
    - theme: alt
      text: GitHub
      link: https://github.com/ny4rl4th0t3p/playhold

features:
  - title: Authoritative rooms
    details: Each room owns its state and handles messages one at a time, so game code needs no locks.
  - title: Composable modules
    details: Modules never import each other. They call each other through small interfaces and notify each other with events, so they can be reused across games.
  - title: Hidden information
    details: The server builds each player's own view, so a client never receives what its player isn't allowed to see.
  - title: Matches that survive deploys
    details: Rooms are saved and restored, a new version can migrate their saved state, players reconnect by themselves, and a client too old for the server is told to update.
  - title: Reproducible games
    details: A room's random numbers and clock can be set, so the same moves give the same game, and playholdvet flags game code that would break that.
  - title: A server that protects itself
    details: Safe settings by default, rate limits, no new games while overloaded, stuck rooms cut off, and a message that keeps crashing the server turned off for a while.
---

::: warning Early development
playhold is a hobby project, and its API can still change. The first public release, v0.3.0, isn't out yet; these pages
describe the code on the `main` branch. A walkthrough and a tutorial come with that release.
:::

## What works today

- **A game survives what a real server goes through.** The sample's survival test plays a game while the server is
  restarted, killed and redeployed with a new save format, and passes only if nobody loses a seat or a card.
- **It fits a small server.** On a container with 1 CPU and 1 GB of memory, the sample plays 500 games at 3,000 moves a
  second, answering half of them within about 0.6 ms. See the rough [measurements](./measurements.md).
- **Its defaults are the safe ones.** TLS, rate limits and overload protection are on unless turned off, and turning
  one off is logged. See [Run in production](./guides/production.md).

The [sample game](https://github.com/ny4rl4th0t3p/playhold/tree/main/examples/cards), Crazy Eights with a browser page,
shows a whole game built on playhold, and the guides cover running a server.
