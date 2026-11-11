#!/bin/sh
# Prints one line per t12.sh run, from the files it left: the bot's summary, the game server's and PostgreSQL's average
# CPU, the game server's most memory and the bot's most CPU, over the window the summary covers (after the warm-up), plus
# the longest snapshot delay the server logged and, if games were recorded, the recordings' size and count.
#
#   sh t12-report.sh [name]
#
# Reads t12-results/, or with a name t12-results/NAME, the same name given to t12.sh.
#
# Times are in ms, CPU in % of one core, memory in MiB. A dash means the run has no such file or value.

set -eu

# Numbers in the files use a dot; a locale with a decimal comma would make awk cut them off.
export LC_ALL=C

dir="t12-results${1:+/$1}"
found=""
for log in "$dir"/*-bot.log; do
    [ -f "$log" ] && found=1
done
if [ -z "$found" ]; then
    echo "no runs in $dir" >&2
    exit 1
fi

# sec turns a UTC time into seconds of the day, ms a Go duration into milliseconds, and mib a Docker memory reading into
# MiB.
helpers='
function sec(t, p) { split(t, p, ":"); return p[1] * 3600 + p[2] * 60 + p[3] }
function ms(d, p) {
    if (d ~ /ns$/) return (d + 0) / 1000000
    if (d ~ /(µs|us)$/) return (d + 0) / 1000
    if (d ~ /ms$/) return d + 0
    if (d ~ /m/) { split(d, p, "m"); return p[1] * 60000 + (p[2] + 0) * 1000 }
    return (d + 0) * 1000
}
function mib(m) {
    if (m ~ /GiB$/) return (m + 0) * 1024
    if (m ~ /MiB$/) return m + 0
    if (m ~ /KiB$/) return (m + 0) / 1024
    return (m + 0) / 1048576
}
'

# stat prints one value of a CSV from record-stats.sh, over the readings in the window that ends at second END of the
# day and lasts LEN seconds: avg for the average CPU, top for the most CPU, mem for the most memory. Readings are
# compared modulo a day, so a run across midnight UTC still works.
stat() {
    file="$1" end="$2" len="$3" what="$4"
    if [ ! -f "$file" ]; then
        echo -
        return
    fi
    awk -F, -v end="$end" -v len="$len" -v what="$what" "$helpers"'
        NR > 1 && (end - sec($1) + 86400) % 86400 <= len {
            n++
            cpu += $2
            if ($2 + 0 > top) top = $2 + 0
            split($3, m, " ")
            if (mib(m[1]) > mem) mem = mib(m[1])
        }
        END {
            if (!n) print "-"
            else if (what == "avg") printf "%.1f\n", cpu / n
            else if (what == "top") printf "%.1f\n", top
            else printf "%.0f\n", mem
        }' "$file"
}

format='%-16s %8s %7s %7s %7s %7s %8s %8s %8s %7s %7s %9s %8s %7s\n'
# shellcheck disable=SC2059 # the format is ours
printf "$format" run moves/s p50 p99 p99.9 max failures game_cpu game_mib pg_cpu bot_top snap_max rec_kib rec_n

for log in "$dir"/*-bot.log; do
    [ -f "$log" ] || continue
    run="${log##*/}"
    run="${run%-bot.log}"
    base="$dir/$run"

    # The window the summary covers ends at the bot's last report and lasts its seconds.
    # shellcheck disable=SC2046 # split into words on purpose
    set -- $(awk "$helpers"'
        /^[0-9][0-9]:[0-9][0-9]:[0-9][0-9] / { end = sec($1) }
        /^summary / { for (i = 2; i <= NF; i++) { split($i, kv, "="); v[kv[1]] = kv[2] } }
        END {
            if (!("seconds" in v)) { print "-"; exit }
            printf "%d %d %s %.2f %.2f %.2f %.2f %s\n", end, v["seconds"], v["moves/s"], ms(v["p50"]), ms(v["p99"]),
                ms(v["p99.9"]), ms(v["max"]), v["failures"]
        }' "$log")
    if [ "$1" = - ]; then
        echo "$run: no summary in $log"
        continue
    fi
    end="$1" len="$2"

    snap=-
    if [ -f "$base-game.log" ]; then
        snap=$(awk "$helpers"'
            /room snapshots written/ && match($0, /max_delay=[^ ]+/) {
                d = ms(substr($0, RSTART + 10, RLENGTH - 10))
                if (!n++ || d > top) top = d
            }
            END { if (n) printf "%.1f\n", top; else print "-" }' "$base-game.log")
    fi

    rec_kib=- rec_n=-
    if [ -f "$base-recordings.txt" ]; then
        rec_kib=$(awk -F'[= ]' '/^kib=/ { print $2 }' "$base-recordings.txt")
        rec_n=$(awk -F'[= ]' '/^kib=/ { print $4 }' "$base-recordings.txt")
        # The measurement failed; the file holds its error.
        rec_kib="${rec_kib:-?}" rec_n="${rec_n:-?}"
    fi

    # shellcheck disable=SC2059 # the format is ours
    printf "$format" "$run" "$3" "$4" "$5" "$6" "$7" "$8" \
        "$(stat "$base-game.csv" "$end" "$len" avg)" "$(stat "$base-game.csv" "$end" "$len" mem)" \
        "$(stat "$base-postgres.csv" "$end" "$len" avg)" "$(stat "$base-bot.csv" "$end" "$len" top)" \
        "$snap" "$rec_kib" "$rec_n"
done