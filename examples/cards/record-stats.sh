#!/bin/sh
# Records the game server's CPU and memory to a CSV file, until stopped with Ctrl+C.
#
#   sh record-stats.sh [file] [seconds between readings]
#
# Times are UTC, like the bot's reports, so the two can be lined up. Each reading itself takes a moment, so the real
# spacing is a little longer than asked for.

file="${1:-stats.csv}"
interval="${2:-5}"
container="playhold-cards-game-1"

echo "time,cpu,memory,memory_percent,network,processes" >"$file"
while :; do
    reading=$(docker stats --no-stream --format '{{.CPUPerc}},{{.MemUsage}},{{.MemPerc}},{{.NetIO}},{{.PIDs}}' "$container") || exit 1
    line="$(date -u +%H:%M:%S),$reading"
    echo "$line" >>"$file"
    echo "$line"
    sleep "$interval"
done