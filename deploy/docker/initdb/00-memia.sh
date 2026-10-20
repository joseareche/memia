#!/bin/bash
# Runs once on an empty data directory (postgres superuser).
# Creates roles, extensions, applies migrations as the owner role and seeds a dev token.
set -euo pipefail
: "${MEMIA_OWNER_PASSWORD:?}" "${MEMIA_APP_PASSWORD:?}" "${MEMIA_DEV_TOKEN:?}"
DB="${POSTGRES_DB:-memia}"
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$DB" <<SQL
CREATE ROLE devbrain_owner LOGIN BYPASSRLS PASSWORD '${MEMIA_OWNER_PASSWORD}';
CREATE ROLE devbrain_app LOGIN PASSWORD '${MEMIA_APP_PASSWORD}';
GRANT ALL ON SCHEMA public TO devbrain_owner;
ALTER DATABASE "$DB" OWNER TO devbrain_owner;
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto;
SQL
own() { psql -v ON_ERROR_STOP=1 -U devbrain_owner -d "$DB" "$@"; }
for f in /memia/migrations/*.sql; do
  echo "migration: $(basename "$f")"
  own -f "$f"
  if [ "$(basename "$f")" = 001_schema.sql ]; then
    # default tenant + owner user must exist before the seeds in 011-013
    own -c "INSERT INTO tenants (name) VALUES ('default') ON CONFLICT DO NOTHING;
            INSERT INTO users (tenant_id, email, role) SELECT id, 'admin@example.com', 'owner' FROM tenants WHERE name='default' ON CONFLICT DO NOTHING;"
  fi
done
# Dev tenant/user/token (token stored only as SHA-256).
own -v tok="$MEMIA_DEV_TOKEN" <<'SQL'
INSERT INTO api_tokens (tenant_id, user_id, name, token_hash, scopes)
  SELECT t.id, u.id, 'web-chat', digest(:'tok', 'sha256'), ARRAY['read','write','admin','commercial','distill','mcp','mcp:read','mcp:write']
  FROM tenants t JOIN users u ON u.tenant_id = t.id WHERE t.name='default' ON CONFLICT DO NOTHING;
SQL
