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
  - title: Matches that survive deploys
    details: Rooms are saved and restored, and players reconnect to them after a restart, a crash or a deploy.
  - title: Safe by default
    details: TLS, rate limits and overload protection are on unless you turn them off, and turning one off is logged.
---

::: warning Early development
playhold is a hobby project, and its API can still change. The first public release, v0.3.0, isn't out yet; these pages
describe the code on the `main` branch. A walkthrough and a tutorial come with that release.
:::

Until then, the [sample game](https://github.com/ny4rl4th0t3p/playhold/tree/main/examples/cards), Crazy Eights with a
browser page, shows a whole game built on playhold, and the guides cover running a server.