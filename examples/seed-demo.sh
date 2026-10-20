#!/usr/bin/env bash
# Loads FAKE demo data (examples/data/*.json) into a local Memia via the REST API and MCP.
# Usage: MEMIA_URL=http://localhost:8080 MEMIA_TOKEN=... ./examples/seed-demo.sh
set -euo pipefail
cd "$(dirname "$0")"
URL="${MEMIA_URL:-http://localhost:8080}"; : "${MEMIA_TOKEN:?set MEMIA_TOKEN}"
H=(-H "Authorization: Bearer $MEMIA_TOKEN" -H "Content-Type: application/json")
jq -c '.[]' data/memories.json | while read -r doc; do
  curl -fsS "${H[@]}" -X POST "$URL/v1/documents" -d "$doc" | jq -c '{document_id, chunks}'
done
# MCP: initialize -> session id -> tools/call save_verified_recipe
SID=$(curl -fsS -D - -o /dev/null "${H[@]}" -H "Accept: application/json, text/event-stream" -X POST "$URL/mcp" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"seed","version":"1"}}}' \
  | awk 'tolower($1)=="mcp-session-id:"{print $2}' | tr -d '\r')
i=10; jq -c '.[]' data/recipes.json | while read -r r; do
  i=$((i+1))
  curl -fsS "${H[@]}" -H "Mcp-Session-Id: $SID" -H "Accept: application/json, text/event-stream" -X POST "$URL/mcp" \
    -d "$(jq -nc --argjson a "$r" --argjson id $i '{jsonrpc:"2.0",id:$id,method:"tools/call",params:{name:"save_verified_recipe",arguments:$a}}')" | jq -c '.result.structuredContent // .result // .error'
done
