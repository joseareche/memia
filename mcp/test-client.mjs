// MCP client test using the official TypeScript SDK (Streamable HTTP). Usage:
//   set NODE_EXTRA_CA_CERTS=devbrain-root-ca.crt & set DEVBRAIN_MCP_TOKEN=... & node test-client.mjs
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
const url = new URL(process.env.DEVBRAIN_MCP_URL || "https://memia.internal.example/mcp");
const tr = new StreamableHTTPClientTransport(url, { requestInit: { headers: { Authorization: "Bearer " + process.env.DEVBRAIN_MCP_TOKEN } } });
const c = new Client({ name: "devbrain-test", version: "1.0.0" });
const show = (r) => (r.content || []).map((x) => x.text).join("\n");
await c.connect(tr);
console.log("server:", JSON.stringify(c.getServerVersion()), "session:", tr.sessionId ? tr.sessionId.slice(0, 8) + "…" : null, "proto:", tr.protocolVersion);
const tools = await c.listTools();
console.log("tools:", tools.tools.map((t) => t.name).join(", "));
const call = async (name, args, n = 1500) => { const t0 = Date.now(); const r = await c.callTool({ name, arguments: args }); console.log(`\n=== ${name} ${JSON.stringify(args)} (${Date.now() - t0} ms, isError=${!!r.isError})\n` + show(r).slice(0, n)); return r; };
const fs = await call("find_solution", { problem: process.env.Q_SOLUTION || "nginx 502 upstream" }, 2500);
const txt = show(fs);
console.log("\nfind_solution has code block:", txt.includes("```"), "| has refs:", /doc=|recipe /.test(txt));
await call("search_history", { query: "nginx 502", k: 3 }, 1200);
await call("search_history", { query: "zzqx frobnicate quuxwidget 98765", k: 3, min_score: 0.6 }, 600);
await call("get_project_rules", { project: "my-project" }, 1200);
const conv = (fs.structuredContent && fs.structuredContent.hits || []).find((h) => h.conversation_id);
if (conv) await call("get_conversation", { conversation_id: conv.conversation_id, max_chars: 1500 }, 800);
await call("save_verified_recipe", { title: "[TEST MCP] receta de prueba sin evidencia", problem: "prueba de guardado vía MCP", solution: "no aplicar; solo prueba", project: "devbrain-test" }, 400);
await call("save_verified_recipe", { title: "[TEST MCP] receta de prueba sin evidencia", problem: "prueba de guardado vía MCP", solution: "no aplicar; solo prueba", project: "devbrain-test" }, 400);
await tr.terminateSession(); console.log("\nsession terminated (DELETE)");
await c.close();
