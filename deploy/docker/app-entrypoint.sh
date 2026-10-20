#!/bin/sh
# Dev entrypoint: derives the admin password hash from MEMIA_ADMIN_PASSWORD (never logged).
set -eu
if [ -z "${DEVBRAIN_ADMIN_PASSWORD_HASH:-}" ] && [ -n "${MEMIA_ADMIN_PASSWORD:-}" ]; then
  DEVBRAIN_ADMIN_PASSWORD_HASH="$(printf '%s' "$MEMIA_ADMIN_PASSWORD" | memia hash-password)"
  export DEVBRAIN_ADMIN_PASSWORD_HASH
fi
unset MEMIA_ADMIN_PASSWORD
exec memia "$@"
