# Crazy Eights

A sample game for two players, built from three modules that don't import each other:

- `deck`: the draw and discard piles
- `turns`: whose turn it is, and starting and ending the game
- `hands`: each player's cards and moves, reaching the other two through keepers

`cards.go` wires them together, and `web/` is the browser page, which uses `sdk/ts`.

## Rules

Each player starts with 7 cards. Play a card matching the top card's rank or the suit in play. An eight can always be
played, and its player chooses the suit to continue with. If you don't want to play or can't, draw one card, then play
or pass. The first player with no cards wins.

## Run it

From the repo root:

```bash
npm --prefix sdk/ts install
npm --prefix examples/cards/web install
make gen
npm --prefix examples/cards/web run build
go run ./examples/cards/server -web examples/cards/web/dist
```

Open <http://localhost:8080> and start a new game, then open the room's link in a private window or another browser.
Two tabs in the same browser share one identity, so the newest tab replaces the older one.

## Host the page elsewhere

Build the page with the server's address, and let the page's origin connect:

```bash
VITE_SERVER_URL=wss://game.example/ws npm --prefix examples/cards/web run build
go run ./examples/cards/server -origins "page.example"
```

The build in `examples/cards/web/dist` uses relative paths, so it can be zipped and uploaded.