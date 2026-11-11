#!/bin/sh
# Measures what saving games costs: the same fixed load with PostgreSQL off and on, on containers pinned to their own CPU
# cores. Each run starts from an empty database, in a Compose project of its own, so your other games are left alone.
# Stop anything else using port 8080 first.
#
#   sh t12.sh [name]
#
# Writes to t12-results/, or with a name to t12-results/NAME, which must not exist yet, so runs with other settings stay
# apart. Ends by printing each run's summary and its longest snapshot delay; t12-report.sh prints the rest. Settings:
# RUNS, GAMES, RATE, WARMUP and DURATION.
#
# With PROFILE=1, every run also collects the same profiles at the same point, PROFILE_AFTER seconds after its bot
# starts (default 90, past the warm-up): a 30 s CPU profile, then a 5 s execution trace, then allocations, blocking and
# lock contention, plus the CPU pinning Docker applied. If Go is installed, each profile also gets a text report. These
# runs are named prof-off-N and prof-on-N, because profiling slows the server down and their latencies don't count.

set -eu

runs="${RUNS:-3}"
results="t12-results${1:+/$1}"
if [ -n "${1:-}" ] && [ -e "$results" ]; then
    echo "$results already exists; pick another name" >&2
    exit 1
fi
compose="docker compose -p playhold-t12 -f compose.yaml -f compose.t12.yaml"
profile="${PROFILE:-}"
profile_after="${PROFILE_AFTER:-90}"
debug_url="http://127.0.0.1:6060/debug/pprof"
prefix=""
if [ -n "$profile" ]; then
    export DEBUG_ADDR=":6060"
    prefix="prof-"
fi

export GAMES="${GAMES:-500}"
export RATE="${RATE:-3000}"
export WARMUP="${WARMUP:-30s}"
export DURATION="${DURATION:-5m30s}"
export FINISHED_ROOM_TIME="${FINISHED_ROOM_TIME:-5s}" # so finished games don't pile up over a long run
# Off unless asked for, so runs compare with earlier ones: CRASH_JOURNAL=/var/lib/cards/journal measures its cost, and
# refusing new players under load would change what's measured.
export CRASH_JOURNAL="${CRASH_JOURNAL-}"
export ADMISSION="${ADMISSION-off}"
# Off unless asked for, like the journal: RECORDING=/var/lib/cards/recordings records every game, and each run then
# also writes the recordings' size and count to its -recordings.txt. It has to be a folder under /var/lib/cards, the
# volume the size is read from. RECORDING_MAX_MIB, such as 500, reaches the size limit within a run, to measure the
# cleanups; the game logs each one as "deleted recordings".
export RECORDING="${RECORDING-}"
export RECORDING_MAX_MIB="${RECORDING_MAX_MIB:-0}"

mkdir -p "$results"

# collect_profiles runs alongside a run's bot, always at the same point of the run, so runs can be compared.
collect_profiles() {
    label="$1"
    sleep "$profile_after"
    docker inspect -f '{{.HostConfig.CpusetCpus}}' playhold-t12-game-1 >"$results/$label-cpuset.txt" 2>&1 || true
    curl -fsS -o "$results/$label-cpu.pprof" "$debug_url/profile?seconds=30" || echo "cpu profile failed" >&2
    curl -fsS -o "$results/$label-trace.out" "$debug_url/trace?seconds=5" || echo "trace failed" >&2
    for name in allocs block mutex; do
        curl -fsS -o "$results/$label-$name.pprof" "$debug_url/$name" || echo "$name profile failed" >&2
    done
}

# report writes each profile of a run as text, if Go is installed.
report() {
    label="$1"
    command -v go >/dev/null 2>&1 || return 0
    base="$results/$label"
    go tool pprof -top -nodecount=40 "$base-cpu.pprof" >"$base-cpu.txt" 2>&1 || true
    go tool pprof -top -cum -nodecount=40 "$base-cpu.pprof" >"$base-cpu-cum.txt" 2>&1 || true
    go tool pprof -sample_index=alloc_space -top -nodecount=40 "$base-allocs.pprof" >"$base-allocs.txt" 2>&1 || true
    go tool pprof -top -nodecount=30 "$base-block.pprof" >"$base-block.txt" 2>&1 || true
    go tool pprof -top -nodecount=30 "$base-mutex.pprof" >"$base-mutex.txt" 2>&1 || true
    if go tool trace -pprof=sched "$base-trace.out" >"$base-sched.pprof" 2>"$base-sched.txt"; then
        go tool pprof -top -nodecount=40 "$base-sched.pprof" >"$base-sched.txt" 2>&1 || true
    fi
}

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
    profiler=""
    if [ -n "$profile" ]; then
        collect_profiles "$label" &
        profiler=$!
    fi

    status=0
    wait "$bot" || status=$?
    if [ -n "$profiler" ]; then
        wait "$profiler" || true
        report "$label"
    fi
    # The bot's recorder stops by itself when its container has gone.
    kill "$game_stats" "$postgres_stats" 2>/dev/null || true
    wait "$bot_stats" 2>/dev/null || true

    $compose logs --no-log-prefix game >"$results/$label-game.log" 2>&1
    if [ -n "$RECORDING" ]; then
        measure_recordings "$label"
    fi
    $compose --profile load down -v --remove-orphans
    return "$status"
}

# measure_recordings writes the size of the run's recordings, in KiB, and how many there are. The game stops first, so
# its last records are written. Its image has no shell, so a throwaway container reads the volume instead.
measure_recordings() {
    label="$1"
    $compose stop game
    docker run --rm -v playhold-t12_journal:/data:ro busybox \
        sh -c "echo \"kib=\$(du -sk '/data/${RECORDING#/var/lib/cards/}' | cut -f1) files=\$(find '/data/${RECORDING#/var/lib/cards/}' -name '*.rec' | wc -l)\"" \
        >"$results/$label-recordings.txt" 2>&1 || echo "measuring the recordings failed" >&2
}

n=1
while [ "$n" -le "$runs" ]; do
    export DATABASE_URL="" # empty means nothing is saved
    measure "${prefix}off-$n"
    unset DATABASE_URL # unset means the Compose default, the postgres service
    measure "${prefix}on-$n"
    n=$((n + 1))
done

echo
echo "Summaries. A run only counts if the bot's CPU, in its CSV, stayed under about 80% of its two cores; prof- runs never"
echo "count, because profiling slows the server down:"
grep -H '^summary' "$results"/*-bot.log
if [ -n "$profile" ]; then
    echo
    echo "CPU pinning Docker applied to the game server in each profiled run (should be 0):"
    grep -H . "$results"/prof-*-cpuset.txt || true
    echo "Profiles and their text reports are in $results/prof-*."
fi
if [ -n "$RECORDING" ]; then
    echo
    echo "Recordings left by each run, in KiB, written over the bot's whole run (DURATION, warm-up included):"
    grep -H . "$results"/*-recordings.txt || true
fi
echo
echo "Longest snapshot delay in each report of each run with PostgreSQL on, in the order they were logged:"
for log in "$results"/${prefix}on-*-game.log; do
    echo "$log: $(grep 'room snapshots written' "$log" | sed 's/.*max_delay=\([^ ]*\).*/\1/' | tr '\n' ' ')"
done