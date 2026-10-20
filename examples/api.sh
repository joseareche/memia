#!/usr/bin/env bash
# REST API examples (fake data). MEMIA_URL defaults to the docker-compose stack.
set -euo pipefail
URL="${MEMIA_URL:-http://localhost:8080}"; : "${MEMIA_TOKEN:?export MEMIA_TOKEN=...}"
auth=(-H "Authorization: Bearer $MEMIA_TOKEN" -H "Content-Type: application/json")
echo "# health / version (no auth)"
curl -fsS "$URL/healthz"; echo; curl -fsS "$URL/version"; echo
echo "# who am I (tenant + scopes)"
curl -fsS "${auth[@]}" "$URL/v1/whoami" | jq .
echo "# save a memory (redact_storage=true stores only the redacted text)"
curl -fsS "${auth[@]}" -X POST "$URL/v1/documents" -d '{
  "title": "Puerto de la API demo", "source": "note", "project": "demo-api",
  "text": "La API demo escucha en el puerto 8081 detrás de nginx. Contacto: ops@example.com",
  "redact_storage": true}' | jq .
echo "# search (full-text, or semantic when an embeddings index is active)"
curl -fsS "${auth[@]}" -X POST "$URL/v1/search" -d '{"query": "nginx 502", "project": "demo-api", "k": 3}' | jq '{mode, hits: [.hits[] | {title, score}]}'
echo "# answer from memory only (no AI call, cost 0)"
curl -fsS "${auth[@]}" -X POST "$URL/v1/answer" -d '{"query": "pgvector", "mode": "memory"}' | jq '{answered_by, answer, sources}'
echo "# list projects / open pending items"
curl -fsS "${auth[@]}" "$URL/v1/projects" | jq .
curl -fsS "${auth[@]}" "$URL/v1/pending" | jq .
