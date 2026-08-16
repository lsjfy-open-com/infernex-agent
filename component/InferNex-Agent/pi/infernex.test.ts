import assert from "node:assert/strict";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import infernexExtension from "./infernex.ts";

type RegisteredTool = {
	name: string;
	execute: (...args: any[]) => Promise<{ content: Array<{ type: string; text: string }> }>;
};

let mockLargeResponse = false;

function mockAPI() {
	const tools: RegisteredTool[] = [];
	const commands: string[] = [];
	const events: string[] = [];
	const handlers = new Map<string, Array<(...args: any[]) => unknown>>();
	return {
		tools,
		api: {
			registerTool(tool: RegisteredTool) {
				tools.push(tool);
			},
			registerCommand(name: string) {
				commands.push(name);
			},
			on(name: string, handler: (...args: any[]) => unknown) {
				events.push(name);
				const registered = handlers.get(name) || [];
				registered.push(handler);
				handlers.set(name, registered);
			},
		} as unknown as ExtensionAPI,
		commands,
		events,
		handlers,
	};
}

function installMockFetch() {
	globalThis.fetch = (async (_input: string | URL | Request, init?: RequestInit) => {
		const request = JSON.parse(String(init?.body));
		if (request.method === "tools/list") {
			return Response.json({
				jsonrpc: "2.0",
				id: request.id,
				result: {
					tools: [
						{
							name: "cluster_overview",
							description: "Read cluster facts",
							inputSchema: { type: "object", properties: {} },
							annotations: { readOnlyHint: true },
						},
						{
							name: "deploy_service",
							description: "Deploy a service",
							inputSchema: { type: "object", properties: { name: { type: "string" } } },
							annotations: { readOnlyHint: false },
						},
					],
				},
			});
		}
		if (mockLargeResponse && request.params.name === "cluster_overview") {
			return Response.json({
				jsonrpc: "2.0",
				id: request.id,
				result: { content: [{ type: "text", text: `start\n${"log-line\n".repeat(3000)}end` }] },
			});
		}
		return Response.json({
			jsonrpc: "2.0",
			id: request.id,
			result: { structuredContent: { ok: true, tool: request.params.name } },
		});
	}) as typeof fetch;
}

test("loads MCP tools and executes read-only calls without approval", async () => {
	mockLargeResponse = false;
	installMockFetch();
	const mock = mockAPI();
	await infernexExtension(mock.api);
	assert.deepEqual(mock.tools.map((tool) => tool.name), [
		"cluster_overview",
		"deploy_service",
		"infernex_read_artifact",
	]);
	assert.deepEqual(mock.commands, ["infernex-tools"]);
	assert.ok(mock.events.includes("session_start"));
	const result = await mock.tools[0].execute("call-1", {}, undefined, undefined, { hasUI: false });
	assert.match(result.content[0].text, /cluster_overview/);
});

test("denies write-capable tools without interactive approval", async () => {
	mockLargeResponse = false;
	installMockFetch();
	const mock = mockAPI();
	await infernexExtension(mock.api);
	await assert.rejects(
		mock.tools[1].execute("call-2", { name: "demo" }, undefined, undefined, { hasUI: false }),
		/denied without an interactive terminal/,
	);
	await assert.rejects(
		mock.tools[1].execute("call-3", { name: "demo" }, undefined, undefined, {
			hasUI: true,
			ui: { confirm: async () => false },
		}),
		/User denied/,
	);
});

test("stores large tool results and reads them progressively by hash", async () => {
	installMockFetch();
	mockLargeResponse = true;
	process.env.INFERNEX_ARTIFACT_DIR = await mkdtemp(join(tmpdir(), "infernex-pi-artifacts-"));
	const mock = mockAPI();
	await infernexExtension(mock.api);
	const result = await mock.tools[0].execute("call-4", {}, undefined, undefined, { hasUI: false });
	const match = result.content[0].text.match(/artifact ([a-f0-9]{64})/);
	assert.ok(match, result.content[0].text);
	assert.match(result.content[0].text, /beginning preview/);
	assert.ok(result.content[0].text.length < 10000);

	const reader = mock.tools.find((tool) => tool.name === "infernex_read_artifact");
	assert.ok(reader);
	const page = await reader.execute("call-5", { id: match[1], offset: 0, limit: 512 }, undefined, undefined, {
		hasUI: false,
	});
	assert.match(page.content[0].text, /bytes 0-511/);
	assert.match(page.content[0].text, /next offset 512/);
});

test("surfaces provider errors and empty assistant responses in the TUI", async () => {
	mockLargeResponse = false;
	installMockFetch();
	const mock = mockAPI();
	await infernexExtension(mock.api);
	const notifications: Array<{ message: string; level: string }> = [];
	const statuses: string[] = [];
	const context = {
		ui: {
			notify(message: string, level: string) {
				notifications.push({ message, level });
			},
			setStatus(_key: string, value: string) {
				statuses.push(value);
			},
		},
	};
	const start = mock.handlers.get("agent_start")?.[0];
	const update = mock.handlers.get("message_update")?.[0];
	const end = mock.handlers.get("message_end")?.[0];
	assert.ok(start && update && end);

	start({ type: "agent_start" }, context);
	assert.match(statuses.at(-1) || "", /waiting for first response/);
	update({ type: "message_update" }, context);
	assert.match(statuses.at(-1) || "", /response streaming/);
	end(
		{
			type: "message_end",
			message: { role: "assistant", content: [], stopReason: "error", errorMessage: "invalid SSE payload" },
		},
		context,
	);
	assert.deepEqual(notifications.at(-1), {
		message: "Model response failed: invalid SSE payload",
		level: "error",
	});

	start({ type: "agent_start" }, context);
	end({ type: "message_end", message: { role: "assistant", content: [], stopReason: "stop" } }, context);
	assert.match(notifications.at(-1)?.message || "", /no displayable text or tool call/);
});
