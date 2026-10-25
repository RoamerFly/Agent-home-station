#!/bin/sh
set -eu
umask 077
: "${WECHAT_API_TOKEN:?set the sending API token (at least 24 bytes)}"
: "${WECHAT_NATIVE_ACCOUNT:?set an account ID created with wx accounts create}"
native_pid=
api_pid=
bot_pid=
cleanup() {
    trap - EXIT
    [ -z "$bot_pid" ] || kill -TERM "$bot_pid" 2>/dev/null || true
    [ -z "$api_pid" ] || kill -TERM "$api_pid" 2>/dev/null || true
    [ -z "$native_pid" ] || kill -TERM "$native_pid" 2>/dev/null || true
    [ -z "$api_pid" ] || wait "$api_pid" 2>/dev/null || true
    [ -z "$native_pid" ] || wait "$native_pid" 2>/dev/null || true
    [ -z "$bot_pid" ] || wait "$bot_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
wx --data-dir /data --listen 127.0.0.1:6174 serve &
native_pid=$!
attempt=0
while [ ! -s /data/api-token ]; do
    kill -0 "$native_pid" 2>/dev/null || exit 1
    attempt=$((attempt + 1))
    [ "$attempt" -lt 60 ] || exit 1
    sleep 1
done
wechat-sendd &
api_pid=$!
if [ "${WECHAT_BOT_ENABLED:-0}" = 1 ]; then
    : "${DEEPSEEK_API_KEY_FILE:?set a readable local DeepSeek key file}"
    : "${WECHAT_BOT_STATE_DIR:?set the persistent bot state directory}"
    : "${WECHAT_BOT_ALLOWED_THREADS:?set the bot conversation allowlist}"
    [ -r "$DEEPSEEK_API_KEY_FILE" ] || { echo 'DeepSeek key file is unreadable' >&2; exit 1; }
    wechat-bot &
    bot_pid=$!
fi
# Exit if either child stops; Docker can then restart the entire service.
while kill -0 "$native_pid" 2>/dev/null && kill -0 "$api_pid" 2>/dev/null; do
    if [ -n "$bot_pid" ] && ! kill -0 "$bot_pid" 2>/dev/null; then
        exit 1
    fi
    sleep 1
done
exit 1
