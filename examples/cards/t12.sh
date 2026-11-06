#!/bin/sh
# Measures what saving games costs: the same fixed load with PostgreSQL off and on, on containers pinned to their own CPU
# cores. Each run starts from an empty database, in a Compose project of its own, so your other games are left alone.
# Stop anything else using port 8080 first.
#
#   sh t12.sh
#
# Writes to t12-results/, and ends by printing each run's summary and its longest snapshot delay. Settings: RUNS, GAMES,
# RATE, WARMUP and DURATION.

set -eu

runs="${RUNS:-3}"
results="t12-results"
compose="docker compose -p playhold-t12 -f compose.yaml -f compose.t12.yaml"

export GAMES="${GAMES:-500}"
export RATE="${RATE:-3000}"
export WARMUP="${WARMUP:-30s}"
export DURATION="${DURATION:-5m30s}"
export FINISHED_ROOM_TIME="${FINISHED_ROOM_TIME:-5s}" # so finished games don't pile up over a long run

mkdir -p "$results"

measure() {
    label="$1"
    $compose --profile load down -v --remove-orphans
    $compose up -d --build game
    $compose --profile load build bot

    sh record-stats.sh "$results/$label-game.csv" 5 playhold-t12-game-1 >/dev/null &
    game_stats=$!
    sh record-stats.sh "$results/$label-postgres.csv" 5 playhold-t12-postgres-1 >/dev/null &
    postgres_stats=$!

    $compose --profile load run --rm --no-deps --name playhold-t12-bot bot >"$results/$label-bot.log" 2>&1 &
    bot=$!
    sleep 5
    sh record-stats.sh "$results/$label-bot.csv" 5 playhold-t12-bot >/dev/null &
    bot_stats=$!

    status=0
    wait "$bot" || status=$?
    # The bot's recorder stops by itself when its container has gone.
    kill "$game_stats" "$postgres_stats" 2>/dev/null || true
    wait "$bot_stats" 2>/dev/null || true

    $compose logs --no-log-prefix game >"$results/$label-game.log" 2>&1
    $compose --profile load down -v --remove-orphans
    return "$status"
}

n=1
while [ "$n" -le "$runs" ]; do
    export DATABASE_URL="" # empty means nothing is saved
    measure "off-$n"
    unset DATABASE_URL # unset means the Compose default, the postgres service
    measure "on-$n"
    n=$((n + 1))
done

echo
echo "Summaries. A run only counts if the bot's CPU, in its CSV, stayed under about 80% of its two cores:"
grep -H '^summary' "$results"/*-bot.log
echo
echo "Longest snapshot delay in each report of each run with PostgreSQL on, in the order they were logged:"
for log in "$results"/on-*-game.log; do
    echo "$log: $(grep 'room snapshots written' "$log" | sed 's/.*max_delay=\([^ ]*\).*/\1/' | tr '\n' ' ')"
done