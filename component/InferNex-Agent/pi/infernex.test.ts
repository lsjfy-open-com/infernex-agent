import assert from "node:assert/strict";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import infernexExtension, { registerAutonomousTask } from "./infernex.ts";

type RegisteredTool = {
	name: string;
	execute: (...args: any[]) => Promise<{ content: Array<{ type: string; text: string }> }>;
};

let mockLargeResponse = false;

function mockAPI() {
	const tools: RegisteredTool[] = [];
	const commands: string[] = [];
 const commandHandlers = new Map<string, any>();
 const sent: any[] = [];
	const events: string[] = [];
	const handlers = new Map<string, Array<(...args: any[]) => unknown>>();
	return {
		tools,
		api: {
 sendMessage: (...args: any[]) => sent.push(args),
			registerTool(tool: RegisteredTool) {
				tools.push(tool);
			},
			registerCommand(name: string, command: any) {
 commandHandlers.set(name, command);
				commands.push(name);
			},
			on(name: string, handler: (...args: any[]) => unknown) {
				events.push(name);
				const registered = handlers.get(name) || [];
				registered.push(handler);
				handlers.set(name, registered);
			},
		} as unknown as ExtensionAPI,
		commands, commandHandlers, sent,
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
		"infernex_host_exec", "infernex_network_probe", "infernex_sample_pfc", "infernex_run_hccl_test", "infernex_task_status",
	]);
	assert.deepEqual(mock.commands, ["mode_change", "infernex-tools"]);
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

test("filesystem builtins cannot bypass the mode-aware executor", async () => {
 installMockFetch(); const mock = mockAPI(); await infernexExtension(mock.api);
 const gate = mock.handlers.get("tool_call")?.[0]; assert.ok(gate);
 for (const toolName of ["read", "write", "bash", "grep", "find", "ls", "edit"]) {
  const result = await gate({toolName, input: {}}, {hasUI: true});
  assert.equal((result as any).block, true);
 }
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

test("tool rows stay one line until expanded, preserve failures and sanitize terminal controls", async () => {
 const { compactToolRenderer } = await import('./infernex.ts');
 const renderer = compactToolRenderer('本机命令');
 const state: any = {};
 const args = { command: 'echo ' + '很长的参数\n'.repeat(100) };
 const context: any = { state, args, executionStarted: true, expanded: false, isError: false };
 const call = renderer.renderCall!(args, {} as any, context);
 assert.equal(call.render(40).length, 1);
 assert.doesNotMatch(call.render(40)[0], /很长的参数/);
 const payload = { content: [{ type: 'text', text: '\u001b[2J' + 'long output\n'.repeat(500) }], details: { exitCode: 1 } } as any;
 const result = renderer.renderResult!(payload, { expanded: false, isPartial: false }, {} as any, context);
 assert.equal(result.render(40).length, 0);
 assert.match(call.render(40)[0], /失败/);
 assert.equal(payload.content[0].text.startsWith('\u001b[2J'), true, 'rendering must not change stored/model evidence');
 const expandedContext = { ...context, expanded: true };
 const expandedCall = renderer.renderCall!(args, {} as any, expandedContext);
 const expandedResult = renderer.renderResult!(payload, { expanded: true, isPartial: false }, {} as any, expandedContext);
 assert.ok(expandedCall.render(40).length > 1); assert.ok(expandedResult.render(40).length > 1);
 assert.doesNotMatch(expandedResult.render(40).join('\n'), /\u001b/);
 for (const width of [1, 2, 10, 40, 80]) {
  const rows = renderer.renderCall!(args, {} as any, context).render(width);
  assert.equal(rows.length, 1);
  assert.ok(Array.from(rows[0]).reduce((n, c) => n + (c.codePointAt(0)! > 127 ? 2 : 1), 0) <= width);
 }
 for (const [details, expected] of [[{ timedOut: true }, '超时'], [{ cancelled: true }, '已取消'], [{ exitCode: 0 }, '完成']] as const) {
  renderer.renderResult!({ content: [], details } as any, { expanded: false, isPartial: false }, {} as any, context);
  assert.match(call.render(40)[0], new RegExp(expected));
 }
});

test("all production MCP, host and evidence tools use compact rendering", async () => {
 installMockFetch(); const mock = mockAPI(); await infernexExtension(mock.api);
 for (const tool of mock.tools as any[]) {
  assert.equal(tool.renderShell, 'self', tool.name);
  assert.equal(typeof tool.renderCall, 'function', tool.name);
  assert.equal(typeof tool.renderResult, 'function', tool.name);
 }
});

test("Pi's real tool execution component renders a single content row and expands on demand", async () => {
 const { compactToolRenderer } = await import('./infernex.ts');
 const { ToolExecutionComponent } = await import('./node_modules/@earendil-works/pi-coding-agent/dist/modes/interactive/components/tool-execution.js');
 const { initTheme } = await import('./node_modules/@earendil-works/pi-coding-agent/dist/modes/interactive/theme/theme.js');
 initTheme('dark', false);
 const definition: any = { name: 'infernex_host_exec', ...compactToolRenderer('本机命令') };
 const component = new ToolExecutionComponent(definition.name, 'compact-render-test', { command: 'long command\n'.repeat(40) }, {}, definition, { requestRender() {} } as any, process.cwd());
 component.markExecutionStarted();
 const pending = component.render(60).filter(line => line.trim());
 assert.equal(pending.length, 1); assert.match(pending[0], /执行中/);
 component.updateResult({ content: [{ type: 'text', text: 'network-counter=123\n'.repeat(200) }], details: { exitCode: 1 }, isError: false });
 const collapsed = component.render(60).filter(line => line.trim());
 assert.equal(collapsed.length, 1); assert.match(collapsed[0], /失败/);
 component.setExpanded(true);
 const expanded = component.render(60).join('\n');
 assert.match(expanded, /long command/); assert.match(expanded, /network-counter=123/);
 component.setExpanded(false);
 assert.equal(component.render(60).filter(line => line.trim()).length, 1);
});

test("continuous tasks resume progress endings but respect completion, blockers, cancel and denial", async () => {
 const mock = mockAPI(); let access: 'manual'|'full' = 'full', denied = false;
 const task = registerAutonomousTask(mock.api,{access:()=>access,wasDenied:()=>denied,resetDenied:()=>{denied=false;}});
 const notices: string[] = [];
 const ctx = {hasUI:true,hasPendingMessages:()=>false,ui:{notify:(s:string)=>notices.push(s)}};
 const emit = async(name:string,event:any) => {for (const handler of mock.handlers.get(name)||[]) await handler(event,ctx);};
 const input = ()=>emit('input',{source:'interactive',text:'diagnose mooncake timeout'});
 const ended = (stopReason='stop')=>emit('agent_end',{messages:[{role:'assistant',stopReason,content:[{type:'text',text:'progress report'}]}]});
 await input(); await ended(); assert.equal(mock.sent.length,1); assert.deepEqual(mock.sent[0][1],{triggerTurn:true,deliverAs:'followUp'});
 await mock.tools[0].execute('done',{state:'complete',summary:'Verified and saved report'},undefined,undefined,ctx);
 await ended(); assert.equal(mock.sent.length,1);
 await input(); await ended('aborted'); await ended(); assert.equal(mock.sent.length,1);
 await input(); await ended('error'); await ended(); assert.equal(mock.sent.length,1);
 await input(); denied=true; await ended(); assert.equal(mock.sent.length,1);
 await input(); task.pause(); await ended(); assert.equal(mock.sent.length,1);
 await input(); await mock.tools[0].execute('blocked',{state:'blocked',summary:'Target cluster credentials are missing'},undefined,undefined,ctx); await ended(); assert.equal(mock.sent.length,1);
 await input(); await ended(); await ended(); await ended(); assert.equal(mock.sent.length,3); assert.match(notices.at(-1)!,/三次/);
 access='manual'; await input(); await ended(); assert.equal(mock.sent.length,3);
 access='full'; await input(); await emit('session_start',{}); await ended(); assert.equal(mock.sent.length,3);
});

test("full mode skips local report approval while cluster mutations still require the operator", async () => {
 installMockFetch(); const original = globalThis.fetch;
 globalThis.fetch = (async(input:any, init:any)=>{
  const response = await original(input,init); const payload = await response.json();
  if (JSON.parse(init.body).method==='tools/list') payload.result.tools.push({name:'infernex_create_markdown_report',inputSchema:{type:'object'},annotations:{readOnlyHint:false}});
  return Response.json(payload);
 }) as typeof fetch;
 const mock=mockAPI(); await infernexExtension(mock.api);
 let confirmations=0;
 const ctx={hasUI:true,isIdle:()=>true,ui:{notify:()=>{},setStatus:()=>{},confirm:async()=>{confirmations++;return false;}}};
 await mock.commandHandlers.get('mode_change').handler('full',ctx);
 await mock.tools.find(t=>t.name==='infernex_create_markdown_report')!.execute('1',{title:'Mooncake report',confirm:true},undefined,undefined,ctx);
 assert.equal(confirmations,0);
 await assert.rejects(mock.tools.find(t=>t.name==='deploy_service')!.execute('2',{name:'model',confirm:true,risk:'safe'},undefined,undefined,ctx),/denied/);
 assert.equal(confirmations,1);
 installMockFetch();
});
