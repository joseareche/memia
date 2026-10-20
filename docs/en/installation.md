# Memia — installation (English summary)

Full manual in Spanish: [../es/manual-instalacion.md](../es/manual-instalacion.md).

**Components:** `app/` core (REST API, MCP server, embeddings, redaction, admin panel, CLI), `chat/` authenticated web chat (proxy to the core), PostgreSQL 17 + pgvector with per-tenant row-level security.

**Requirements:** Go 1.24+, PostgreSQL 17 + pgvector, optional AI provider key via env (`OPENAI_API_KEY`, `GEMINI_API_KEY`) or local Ollama (`DEVBRAIN_OLLAMA_URL`). Without a provider Memia answers from memory (full-text) at zero cost.

**Local dev (Docker Compose):**
```bash
cp .env.docker.example .env.docker   # change every value
sed -i "s/^DEVBRAIN_ACTOR_HMAC_KEY=.*/DEVBRAIN_ACTOR_HMAC_KEY=$(openssl rand -hex 32)/" .env.docker
docker compose --env-file .env.docker up -d --build
export MEMIA_TOKEN=<MEMIA_DEV_TOKEN>; ./examples/seed-demo.sh
```
Chat: http://localhost:8093/chat (user `default`) · Admin: http://localhost:8080/admin · MCP: http://localhost:8080/mcp

**Manual install:** create roles `devbrain_owner` (BYPASSRLS) and `devbrain_app`, extensions `vector` + `pgcrypto`, run `app/migrations/0*.sql` in order as `devbrain_owner`, build with `go build`, create a tenant/user/token (token stored as SHA-256), run behind a TLS proxy on a private network with systemd credentials.

**MCP (Cursor):**
```json
{ "mcpServers": { "memia": { "url": "https://memia.internal.example/mcp",
  "headers": { "Authorization": "Bearer ${env:MEMIA_TOKEN}" } } } }
```
**Claude Code:** `claude mcp add --transport http memia https://memia.internal.example/mcp --header "Authorization: Bearer $MEMIA_TOKEN"`

**Security:** forced RLS per tenant, hashed scoped tokens, redaction of secrets/PII before any external provider, chat is the only exposed service (strict cookies, CSP, rate limiting, pinned-CA TLS to the core). **Backups:** `pg_dump -Fc`, encrypt, keep off-site, test restores.
