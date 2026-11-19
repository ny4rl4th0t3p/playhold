# Bonus: run it for players

The game is complete after [chapter 7](./a-second-module.md), and this page changes none of it. It runs it the way a
host would for players: over TLS, out of development mode, the page on a host of its own, games saved in PostgreSQL,
and the server started again when it dies. You can try it on your own machine, then use the same files on a server
with a domain.

The finished code is `bonus/` in
[playhold-tictactoe](https://github.com/ny4rl4th0t3p/playhold-tictactoe). Its Go paths start with
`github.com/ny4rl4th0t3p/playhold-tictactoe/bonus`; in your module, they're `example.com/tictactoe`. You need Docker with
Compose.

## What changes from development

- **TLS.** Out of development mode, the server refuses plain HTTP unless it comes through a proxy it trusts. Here that
  proxy is [Caddy](https://caddyserver.com), which ends TLS for both names and gets their certificates.
- **Two names.** The page is at one name and the game at another, such as `page.example` and `game.example`. The game's
  server never serves the page: Caddy does, as static files, so a new page is deployed without restarting the server.
  See [Why the page isn't served by the game server](./a-page.md#why-the-page-isn-t-served-by-the-game-server).
- **Settings from the environment.** The server's addresses, the page's address and the database's password are in a
  `.env` file, never in the code.

## The server

`server/main.go` turns development mode on only with `DEVELOPMENT=1`, so forgetting a setting never leaves a public
server unprotected. It reads the proxy's address from `TRUSTED_PROXIES`, and allows no page by default outside
development:

<<< ../.tictactoe/bonus/server/main.go#server

- `TrustedProxies` is the list of addresses allowed to send plain HTTP, and to say which address a player connects
  from, in `X-Forwarded-For`. That address counts for the [limits](../guides/limits.md) per address. See
  [Behind a proxy](../guides/production.md#behind-a-proxy).
- A wrong range stops the server at startup; a test in `server/main_test.go` covers the cases.
- The commands from the chapters now need `DEVELOPMENT=1`, such as
  `DEVELOPMENT=1 DATABASE_URL=… go run ./server`.

## The images

One `Dockerfile` builds two images: the game server, one binary on an empty image, and Caddy with the built page. The
page is built with the game's address, which it can't learn once it's only files:

<<< ../.tictactoe/bonus/Dockerfile{dockerfile}

`DIR` is where the game is in the build's folder. In your project, `go.mod` is next to the `Dockerfile`, and `DIR`
stays `.`; in the tutorial's repository, `go.mod` is one folder up, so its Compose file sets `DIR` to `bonus`.

A `.dockerignore` next to `go.mod` keeps installed packages, built files and `.env` out of the images:

<<< ../.tictactoe/.dockerignore{txt}

## Caddy

Two sites, one for each name. The page's serves the files; the game's passes everything to the game server, WebSocket
connections included, with the player's address in `X-Forwarded-For`:

<<< ../.tictactoe/bonus/Caddyfile{txt}

For a real domain, Caddy gets each name's certificate from Let's Encrypt. `LOCAL_CERTS` makes it use its own instead,
for a machine on your own network, which Let's Encrypt can't reach.

## Compose

<<< ../.tictactoe/bonus/compose.prod.yaml

- **Caddy** has the only published port, and a fixed address on the Compose network. The game server trusts that one
  address, and nothing else; the other containers get addresses from another range, so none can take it.
- **The game server** has no published port: only Caddy reaches it. `restart: unless-stopped` starts it again when it
  exits on its own, and it restores every game from PostgreSQL. `stop_grace_period` leaves it time to save every game
  when it's stopped: longer than its shutdown timeout, 8 seconds by default.
- **PostgreSQL** is only on the Compose network, with its password from `.env`.

`compose.yaml`, from chapter 5, stays as it was, for PostgreSQL alone while you write code.

## The settings

Copy `.env.example` to `.env`, next to `compose.prod.yaml`, and choose one of its three setups. `.env` holds a
password: keep it out of git.

<<< ../.tictactoe/bonus/.env.example{ini}

## Run it on your own machine

With the first setup, from the module's folder:

```bash
cp .env.example .env    # then set POSTGRES_PASSWORD
docker compose -f compose.prod.yaml up -d --build
```

1. Open `https://game.localhost:8443/healthz`, and accept the certificate warning. A page's own connection can't show
   the warning, so without this the page says "Connection lost. Reconnecting…" forever.
2. Open `https://page.localhost:8443`, accept its warning too, and play, in a normal window and a private one.
3. Restart the game server in the middle of a game: the game and the score come back.

```bash
docker compose -f compose.prod.yaml restart game
docker compose -f compose.prod.yaml logs game caddy
```

Browsers keep `.localhost` names to the machine they run on. To browse from another machine, such as with the game on a
virtual machine, use the second setup, with names ending in `.test` in that machine's hosts file.

## Run it on a server

With the third setup:

1. Point both names at the server in DNS, and open port 443.
2. Copy the module's folder to the server, write its `.env`, and start it with the same `docker compose` command.
3. Caddy gets the certificates from Let's Encrypt when it starts; `docker compose -f compose.prod.yaml logs caddy` says
   if it can't.

Back up the `postgres` volume, which holds every game in progress and every guest; the `caddy` volume holds the
certificates, which Caddy can get again. What each table holds: [Deploy](../guides/deploy.md#backups).

This page leaves out what a game with more players would want next: a crash journal, recordings, rate limits, metrics
and restart when stuck. [Run in production](../guides/production.md) and [Deploy](../guides/deploy.md) cover each.