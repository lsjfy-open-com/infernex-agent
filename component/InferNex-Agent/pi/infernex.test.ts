import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import infernexExtension from "./infernex.ts";

type RegisteredTool = {
	name: string;
	execute: (...args: any[]) => Promise<{ content: Array<{ type: string; text: string }> }>;
};

function mockAPI() {
	const tools: RegisteredTool[] = [];
	const commands: string[] = [];
	const events: string[] = [];
	return {
		tools,
		api: {
			registerTool(tool: RegisteredTool) {
				tools.push(tool);
			},
			registerCommand(name: string) {
				commands.push(name);
			},
			on(name: string) {
				events.push(name);
			},
		} as unknown as ExtensionAPI,
		commands,
		events,
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
		return Response.json({
			jsonrpc: "2.0",
			id: request.id,
			result: { structuredContent: { ok: true, tool: request.params.name } },
		});
	}) as typeof fetch;
}

test("loads MCP tools and executes read-only calls without approval", async () => {
	installMockFetch();
	const mock = mockAPI();
	await infernexExtension(mock.api);
	assert.deepEqual(mock.tools.map((tool) => tool.name), ["cluster_overview", "deploy_service"]);
	assert.deepEqual(mock.commands, ["infernex-tools"]);
	assert.ok(mock.events.includes("session_start"));
	const result = await mock.tools[0].execute("call-1", {}, undefined, undefined, { hasUI: false });
	assert.match(result.content[0].text, /cluster_overview/);
});

test("denies write-capable tools without interactive approval", async () => {
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
