#!/bin/sh
# The turn-based check: what a release gates on, at 1,000 players on the small server, over TLS. Runs one set at a time,
# on the same pinned containers as t12.sh, in a Compose project of its own. Stop anything else using ports 8080 and
# 8443 first.
#
#   sh check.sh SET [name]
#
# The sets:
#   idle       the server alone for 30 s, then 1,000 idle connections opened over 60 s and held 3 minutes
#   load       1,000 players, a move every 5 s each, with saving off and on, 3 runs each
#   reconnect  1,000 players that rejoin their game, with the server restarted 2 minutes in
#   overload   2,000 players with room for 600 games, so the rest are refused
#   soak       1,000 players for SOAK, 4h by default, recording every minute
#
# Writes to t12-results/NAME, check-SET by default, which must not exist yet; read it with t12-report.sh NAME. Makes a
# test certificate in check-certs/ the first time, with openssl in a throwaway container, so only Docker is needed.

set -eu

set="${1:-}"
name="${2:-check-$set}"
results="t12-results/$name"
compose="docker compose -p playhold-t12 -f compose.yaml -f compose.t12.yaml -f compose.check.yaml"

case "$set" in
idle | load | reconnect | overload | soak) ;;
*)
    echo "usage: sh check.sh idle|load|reconnect|overload|soak [name]" >&2
    exit 1
    ;;
esac
if [ -e "$results" ]; then
    echo "$results already exists; pick another name" >&2
    exit 1
fi

# The turn-based profile: each player moves every 5 seconds on average, so 1,000 players make 200 moves a second. The
# spread makes each wait random, from 2 to 8 seconds, so players don't move like clockwork.
export GAMES=500
export RATE=200
export SPREAD=0.6
export WARMUP=30s
export DURATION=5m30s
export FINISHED_ROOM_TIME=5s # so finished games don't pile up over a long run
export CRASH_JOURNAL=""
export RECORDING=""
# TLS, as on a public server: the game serves it on 8443, and the bot trusts the test certificate.
export ADDR=:8443
export TLS_CERT=/certs/server.pem
export TLS_KEY=/certs/server.key
export BOT_URL=wss://game:8443/ws
export BOT_CA=/certs/server.pem
stats_every=5
before_bot=0      # seconds the server runs alone before the bot starts
restart_after=""  # seconds after the bot starts to restart the server, if set

# make_certs makes a self-signed certificate for the name the bot reaches the game by, valid for 30 days. The files
# are readable by everyone, since the game runs as a user of its own; it's only ever a test certificate. Handing them
# to your user only works with rootful Docker; rootless Docker already writes them as you.
make_certs() {
    [ -f check-certs/server.pem ] && return 0
    mkdir -p check-certs
    docker run --rm -v "$PWD/check-certs:/out" alpine:3 sh -c "
        apk add --no-cache openssl >/dev/null &&
        openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 -subj /CN=game \
            -addext subjectAltName=DNS:game -keyout /out/server.key -out /out/server.pem 2>/dev/null &&
        chmod 644 /out/server.key /out/server.pem &&
        { chown $(id -u):$(id -g) /out/server.key /out/server.pem 2>/dev/null || true; }"
}

# run_one is one run, as t12.sh's: an empty database, the game, the bot, and the CPU and memory of all three, with
# before_bot and restart_after as set above. It also keeps how many times Docker restarted the game on its own, which
# is 0 unless it crashed.
run_one() {
    label="$1"
    $compose --profile load down -v --remove-orphans
    $compose up -d --build game
    $compose --profile load build bot

    sh record-stats.sh "$results/$label-game.csv" "$stats_every" playhold-t12-game-1 >/dev/null &
    game_stats=$!
    sh record-stats.sh "$results/$label-postgres.csv" "$stats_every" playhold-t12-postgres-1 >/dev/null &
    postgres_stats=$!
    sleep "$before_bot"

    $compose --profile load run --rm --no-deps --name playhold-t12-bot bot >"$results/$label-bot.log" 2>&1 &
    bot=$!
    sleep 5
    sh record-stats.sh "$results/$label-bot.csv" "$stats_every" playhold-t12-bot >/dev/null &
    bot_stats=$!
    if [ -n "$restart_after" ]; then
        sleep "$((restart_after - 5))"
        echo "$(date -u +%H:%M:%S) restarting the game" >>"$results/$label-restarts.log"
        $compose restart game
    fi

    status=0
    wait "$bot" || status=$?
    kill "$game_stats" "$postgres_stats" 2>/dev/null || true
    wait "$bot_stats" 2>/dev/null || true

    $compose logs --no-log-prefix game >"$results/$label-game.log" 2>&1
    docker inspect -f '{{.RestartCount}}' playhold-t12-game-1 >"$results/$label-crashes.txt" 2>&1 || true
    $compose --profile load down -v --remove-orphans
    return "$status"
}

make_certs
mkdir -p "$results"

case "$set" in
idle)
    export IDLE=1000 RAMP=60s DURATION=4m
    before_bot=30
    run_one on-1
    ;;
load)
    n=1
    while [ "$n" -le 3 ]; do
        export DATABASE_URL="" # empty means nothing is saved
        run_one "off-$n"
        unset DATABASE_URL # unset means the Compose default, the postgres service
        run_one "on-$n"
        n=$((n + 1))
    done
    ;;
reconnect)
    export REJOIN=true
    restart_after=120
    run_one on-1
    ;;
overload)
    export GAMES=1000 RATE=400 MAX_ROOMS=600 # RATE is for all the games, so it doubles with them
    run_one on-1
    ;;
soak)
    export DURATION="${SOAK:-4h}" WARMUP=1m
    stats_every=60
    run_one on-1
    ;;
esac

echo
echo "Done: sh t12-report.sh $name"