#!/bin/sh
# Plays one game while the server is restarted, killed and redeployed with a new snapshot format, and checks that it
# finishes with a winner and that nobody lost a seat or a card. It exits with 0 if the game survived.
#
#   sh done-test.sh [seconds between interruptions]
#
# It leaves the server running with the new format. To go back, start a new game after this one has finished, and run:
#
#   docker compose up -d game

set -eu

step="${1:-15}"
log="done-test.log"

docker compose up -d --build game
docker compose --profile load build bot

# The bot only draws and passes until the interruptions are over, so the game can't finish before them.
stall=$((step * 5))
docker compose --profile load run --rm --no-deps bot \
    -url ws://game:8080/ws -survive 3 -think 500ms -stall "${stall}s" -duration 10m >"$log" 2>&1 &
bot=$!

sleep "$step"
echo "restart, as a service manager would"
docker compose restart game

sleep "$step"
echo "kill -9, as a crash would"
docker compose kill -s SIGKILL game
docker compose up -d game

sleep "$step"
echo "deploy with a new snapshot format"
TURNS_SNAPSHOT_VERSION=2 docker compose up -d game

echo "waiting for the game to finish"
status=0
wait "$bot" || status=$?
cat "$log"
exit "$status"