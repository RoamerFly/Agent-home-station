#!/bin/sh
set -eu
umask 077
native_pid=
web_pid=
cleanup() {
    trap - EXIT
    [ -z "$web_pid" ] || kill -TERM "$web_pid" 2>/dev/null || true
    [ -z "$web_pid" ] || wait "$web_pid" 2>/dev/null || true
    [ -z "$native_pid" ] || kill -TERM "$native_pid" 2>/dev/null || true
    [ -z "$native_pid" ] || wait "$native_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
wx --data-dir /data --listen "${WECHAT_NATIVE_LISTEN:-127.0.0.1:6174}" serve &
native_pid=$!
attempt=0
while [ ! -s /data/api-token ]; do
    kill -0 "$native_pid" 2>/dev/null || exit 1
    attempt=$((attempt + 1))
    [ "$attempt" -lt 60 ] || exit 1
    sleep 1
done
wechat-admin &
web_pid=$!
while kill -0 "$native_pid" 2>/dev/null && kill -0 "$web_pid" 2>/dev/null; do
    sleep 1
done
exit 1
