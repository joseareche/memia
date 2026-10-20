// Minimal MCP client against Memia (Streamable HTTP) using the official SDK.
//   npm i @modelcontextprotocol/sdk
//   MEMIA_MCP_URL=http://localhost:8080/mcp MEMIA_TOKEN=... node examples/mcp-client.mjs
// For an HTTPS endpoint with a private CA:  NODE_EXTRA_CA_CERTS=/path/to/root-ca.crt
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const url = new URL(process.env.MEMIA_MCP_URL || "http://localhost:8080/mcp");
const transport = new StreamableHTTPClientTransport(url, {
  requestInit: { headers: { Authorization: `Bearer ${process.env.MEMIA_TOKEN}` } },
});
const client = new Client({ name: "memia-example", version: "1.0.0" });
await client.connect(transport);

const { tools } = await client.listTools();
console.log("tools:", tools.map((t) => t.name).join(", "));

const text = (r) => (r.content || []).map((c) => c.text).join("\n");
for (const [name, args] of [
  ["find_solution", { problem: "nginx 502" }],
  ["get_project_rules", { project: "acme-webshop" }],
  ["search_history", { query: "pgvector", k: 3 }],
]) {
  const r = await client.callTool({ name, arguments: args });
  console.log(`\n=== ${name} ${JSON.stringify(args)}\n${text(r).slice(0, 1200)}`);
}
await transport.terminateSession();
await client.close();
