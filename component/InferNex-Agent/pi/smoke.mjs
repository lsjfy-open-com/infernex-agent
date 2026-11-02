import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const piBinary = process.argv[2];
assert.ok(piBinary, "usage: node smoke.mjs /path/to/pi");
const extension = resolve(dirname(fileURLToPath(import.meta.url)), "infernex.ts");
const agentDir = await mkdtemp(join(tmpdir(), "infernex-pi-smoke-"));
await writeFile(
	join(agentDir, "models.json"),
	JSON.stringify({
		providers: {
			infernex: {
				baseUrl: "http://127.0.0.1:1/v1",
				api: "openai-completions",
				apiKey: "$INFERNEX_PI_API_KEY",
				models: [{ id: "smoke", contextWindow: 32768, maxTokens: 2048 }],
			},
		},
	}),
	{ mode: 0o600 },
);

let toolsListed = false;
const server = createServer(async (request, response) => {
	let body = "";
	for await (const chunk of request) body += chunk;
	const rpc = JSON.parse(body);
	if (rpc.method === "tools/list") toolsListed = true;
	response.writeHead(200, { "Content-Type": "application/json" });
	response.end(
		JSON.stringify({
			jsonrpc: "2.0",
			id: rpc.id,
			result: rpc.method === "tools/list" ? { tools: [] } : {},
		}),
	);
});
await new Promise((resolveListen) => server.listen(0, "127.0.0.1", resolveListen));
const address = server.address();
assert.ok(address && typeof address === "object");

const child = spawn(
	piBinary,
	[
		"--provider",
		"infernex",
		"--model",
		"infernex/smoke",
		"--no-builtin-tools",
		"--no-extensions",
		"--extension",
		extension,
		"--no-skills",
		"--no-prompt-templates",
		"--no-context-files",
		"--no-approve",
		"--offline",
		"--list-models",
		"infernex",
	],
	{
		shell: process.platform === "win32",
		env: {
			...process.env,
			PI_CODING_AGENT_DIR: agentDir,
			INFERNEX_PI_API_KEY: "smoke-key",
			INFERNEX_MCP_URL: `http://127.0.0.1:${address.port}/mcp`,
			INFERNEX_ARTIFACT_DIR: join(agentDir, "artifacts"),
		},
		stdio: ["ignore", "pipe", "pipe"],
	},
);
let stdout = "";
let stderr = "";
child.stdout.on("data", (chunk) => (stdout += chunk));
child.stderr.on("data", (chunk) => (stderr += chunk));
const exitCode = await new Promise((resolveExit) => child.on("close", resolveExit));
server.close();

assert.equal(exitCode, 0, `Pi smoke failed\nstdout:\n${stdout}\nstderr:\n${stderr}`);
assert.equal(toolsListed, true, "InferNex extension did not call MCP tools/list");
assert.match(stdout, /smoke/i, `configured model was not listed\n${stdout}`);
console.log("Pi InferNex extension smoke passed");
