#!/bin/sh
set -eu

image_bin=/usr/local/bin/cfsm-monitor
update_dir="${DATA_DIR:-/data}/.cfsm-update"
if [ "$#" -ne 0 ] || ! mkdir -p "$update_dir" || [ ! -w "$update_dir" ]; then
    exec "$image_bin" "$@"
fi

export CFSM_UPDATE_DIR="$update_dir"
unset CFSM_UPDATE_READY
current="$update_dir/current"
pending="$update_dir/pending"
trial="$update_dir/trial"
ready="$update_dir/ready"
rm -f "$trial" "$ready" "$update_dir"/download-*

child=
stop() {
    if [ -n "$child" ]; then
        kill -TERM "$child" 2>/dev/null || true
        wait "$child" || true
    fi
    exit 0
}
trap stop TERM INT

version() {
    output="$("$1" --version 2>/dev/null)" || return 1
    case "$output" in
        'cfsm-monitor-go v'*) printf '%s\n' "${output#cfsm-monitor-go v}" ;;
        *) return 1 ;;
    esac
}

newer() {
    candidate_compat="$("$1" --update-compat 2>/dev/null)" || return 1
    [ "$candidate_compat" = "$image_compat" ] || return 1
    candidate="$(version "$1")" && installed="$(version "$2")" || return 1
    [ "$candidate" != "$installed" ] && [ "$(printf '%s\n' "$candidate" "$installed" | sort -V | tail -n 1)" = "$candidate" ]
}

image_compat="$("$image_bin" --update-compat)"
while true; do
    active="$image_bin"
    if [ -x "$current" ] && newer "$current" "$image_bin"; then
        active="$current"
    fi

    if [ -x "$pending" ] && newer "$pending" "$active" && mv -f "$pending" "$trial"; then
        CFSM_UPDATE_READY="$ready" "$trial" &
        child=$!
        remaining=20
        while [ ! -f "$ready" ] && kill -0 "$child" 2>/dev/null && [ "$remaining" -gt 0 ]; do
            sleep 1
            remaining=$((remaining - 1))
        done
        if [ -f "$ready" ]; then
            sleep 3
        fi
        if [ -f "$ready" ] && kill -0 "$child" 2>/dev/null && mv -f "$trial" "$current"; then
            echo "CFSM Monitor upgrade confirmed"
            rm -f "$ready"
        else
            kill -KILL "$child" 2>/dev/null || true
            wait "$child" || true
            child=
            rm -f "$trial" "$ready"
            echo "CFSM Monitor upgrade failed; restarting previous version" >&2
            continue
        fi
    else
        rm -f "$pending"
        "$active" &
        child=$!
    fi
    rc=0
    wait "$child" || rc=$?
    child=
    if [ "$rc" -ne 42 ] || [ ! -x "$pending" ]; then
        exit "$rc"
    fi
done
