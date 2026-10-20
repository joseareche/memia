#!/bin/sh
# Dev entrypoint: builds the systemd-style credentials directory the chat expects,
# then waits for the TLS proxy's local CA (chat -> core is always HTTPS with a pinned CA).
set -eu
CREDENTIALS_DIRECTORY="$(mktemp -d)"; export CREDENTIALS_DIRECTORY
umask 077
printf '%s\n' "$CHAT_DEV_PASSWORD" | memia-chat hash > "$CREDENTIALS_DIRECTORY/admin_password_hash"
printf '%s' "$MEMIA_DEV_TOKEN" > "$CREDENTIALS_DIRECTORY/devbrain_token"
printf '%s' "$DEVBRAIN_ACTOR_HMAC_KEY" > "$CREDENTIALS_DIRECTORY/actor_hmac_key"
unset CHAT_DEV_PASSWORD MEMIA_DEV_TOKEN DEVBRAIN_ACTOR_HMAC_KEY
i=0; until [ -s "$DEVBRAIN_CA_FILE" ]; do i=$((i+1)); [ $i -gt 60 ] && { echo "CA not found: $DEVBRAIN_CA_FILE" >&2; exit 1; }; sleep 1; done
exec memia-chat "$@"
