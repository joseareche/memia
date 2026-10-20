# Ejemplos / Examples

Todos los datos son **ficticios** (`example.com`, proyectos `acme-webshop` y `demo-api`).

| Archivo | Qué hace |
|---|---|
| [`seed-demo.sh`](seed-demo.sh) | Carga memorias (`data/memories.json`) por REST y recetas (`data/recipes.json`) por MCP. |
| [`api.sh`](api.sh) | Llamadas `curl`: health, whoami, guardar memoria con redacción, búsqueda, respuesta solo-memoria, proyectos, pendientes. |
| [`mcp-client.mjs`](mcp-client.mjs) | Cliente MCP con el SDK oficial: lista herramientas y llama `find_solution`, `get_project_rules`, `search_history`. |
| [`data/`](data) | Memorias y recetas de ejemplo. |

```bash
export MEMIA_TOKEN=<MEMIA_DEV_TOKEN>          # del .env.docker
./examples/seed-demo.sh
./examples/api.sh
npm i @modelcontextprotocol/sdk && node examples/mcp-client.mjs
```

Requiere `curl` y `jq`. `MEMIA_URL` / `MEMIA_MCP_URL` cambian el destino (por defecto `http://localhost:8080`).
