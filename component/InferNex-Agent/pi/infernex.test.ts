import assert from "node:assert/strict";
import { mkdtemp } from "node:fs/promises";
import { findPackageJSON } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import infernexExtension, { registerAutonomousTask, registerTurnBoundaryCompaction, trustedStdioConfiguration } from "./infernex.ts";

type RegisteredTool = {
	name: string;
	execute: (...args: any[]) => Promise<{ content: Array<{ type: string; text: string }> }>;
};

let mockLargeResponse = false;

test("trusted private stdio configuration pins the installed agent and exact mode", () => {
	const exact = JSON.stringify(["serve", "--transport", "stdio", "--private-state-directory", "/var/lib/infernex-private", "--private-inventory-only"]);
	assert.deepEqual(trustedStdioConfiguration({
		INFERNEX_MCP_STDIO_COMMAND: "/opt/infernex-agent/bin/infernex-agent",
		INFERNEX_MCP_STDIO_ARGS: exact,
	}), { command: "/opt/infernex-agent/bin/infernex-agent", args: JSON.parse(exact) });
	assert.throws(() => trustedStdioConfiguration({
		INFERNEX_MCP_STDIO_COMMAND: "/tmp/infernex-agent",
		INFERNEX_MCP_STDIO_ARGS: exact,
	}), /must be \/opt\/infernex-agent\/bin\/infernex-agent/);
	assert.throws(() => trustedStdioConfiguration({
		INFERNEX_MCP_STDIO_COMMAND: "/opt/infernex-agent/bin/infernex-agent",
		INFERNEX_MCP_STDIO_ARGS: JSON.stringify(["serve", "--transport", "stdio", "--private-state-directory", "/tmp/state"]),
	}), /private-inventory-only/);
});

function mockAPI() {
	const tools: RegisteredTool[] = [];
	const commands: string[] = [];
 const commandHandlers = new Map<string, any>();
 const sent: any[] = [];
	const users: any[] = [];
	const events: string[] = [];
	const handlers = new Map<string, Array<(...args: any[]) => unknown>>();
	return {
		tools,
		api: {
 sendMessage: (...args: any[]) => sent.push(args),
			sendUserMessage: (...args: any[]) => users.push(args),
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
		commands, commandHandlers, sent, users,
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
							name: "k8s_plan_deployment",
							description: "Estimate deployment resource fit without reserving capacity",
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
		if (mockLargeResponse && request.params.name === "k8s_plan_deployment") {
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
		"k8s_plan_deployment",
		"deploy_service",
		"infernex_read_artifact",
		"infernex_classify_command", "infernex_host_exec", "infernex_network_probe", "infernex_sample_pfc", "infernex_run_hccl_test", "infernex_task_status",
	]);
	assert.deepEqual(mock.commands, ["mode_change", "infernex-tools"]);
	assert.ok(mock.events.includes("session_start"));
	const result = await mock.tools[0].execute("call-1", {}, undefined, undefined, { hasUI: false });
	assert.match(result.content[0].text, /k8s_plan_deployment/);
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

test("tool-loop boundary compaction is single-flight and resumes only after success", async () => {
 const mock = mockAPI();
 const state = registerTurnBoundaryCompaction(mock.api, 80);
 let callbacks: any; let compactCalls = 0; let percent: number | null = 96;
 const statuses: Array<string | undefined> = [];
 const ctx: any = {
  signal: new AbortController().signal, isIdle: () => true, hasPendingMessages: () => false,
  getContextUsage: () => ({tokens: percent === null ? null : 960, contextWindow: 1000, percent}),
  compact: (options: any) => { compactCalls++; callbacks = options; },
  ui: {setStatus: (_key: string, value: string | undefined) => statuses.push(value), notify() {}},
 };
 const emit = async(name: string, event: any) => {
  const results = [];
  for (const handler of mock.handlers.get(name) || []) results.push(await handler(event, ctx));
  return results;
 };
 const boundary = {message: {role: "assistant", stopReason: "toolUse"}, toolResults: [{role: "toolResult"}]};
 await emit("turn_end", boundary); await emit("turn_end", boundary);
 assert.equal(compactCalls, 1); assert.equal(state.isActive(), true);
 assert.deepEqual((await emit("session_before_compact", {reason: "threshold"}))[0], {cancel: true});
 assert.equal((await emit("session_before_compact", {reason: "manual"}))[0], undefined);
 percent = null; callbacks.onComplete({summary: "probe finished"}); callbacks.onComplete({summary: "duplicate"});
 await new Promise(resolve => setTimeout(resolve, 10));
 assert.equal(state.isActive(), false); assert.equal(mock.sent.length, 1);
 assert.deepEqual(mock.sent[0][1], {triggerTurn: true, deliverAs: "followUp"});
 assert.match(mock.sent[0][0].content, /compacted context/);
 assert.equal(statuses.at(-1), undefined);
});

test("input racing with compaction is replayed, while newer user intent takes priority", async () => {
 const mock = mockAPI(); let callbacks: any; let pending = false; const notices: string[] = [];
 registerTurnBoundaryCompaction(mock.api, 80);
 const ctx: any = {
  signal: new AbortController().signal, isIdle: () => true, hasPendingMessages: () => pending,
  getContextUsage: () => ({tokens: 960, contextWindow: 1000, percent: 96}),
  compact: (options: any) => { callbacks = options; }, ui: {setStatus() {}, notify: (message: string) => notices.push(message)},
 };
 const emit = async(name: string, event: any) => {
  const results = [];
  for (const handler of mock.handlers.get(name) || []) results.push(await handler(event, ctx));
  return results;
 };
 const ordinary = await emit("input", {source: "interactive", text: "ordinary steering", streamingBehavior: "steer"});
 assert.equal(ordinary[0], undefined);
 await emit("turn_end", {message: {role: "assistant", stopReason: "stop"}, toolResults: []});
 assert.equal(callbacks, undefined, "input must not be captured when no boundary compaction will own it");

 await emit("turn_end", {message: {role: "assistant", stopReason: "toolUse"}, toolResults: [{}]});
 assert.ok(callbacks);
 const firstImage = {type: "image", data: "first", mimeType: "image/png"};
 const secondImage = {type: "image", data: "second", mimeType: "image/png"};
 const racing = await emit("input", {source: "interactive", text: "new target while compacting", images: [firstImage], streamingBehavior: "steer"});
 assert.deepEqual(racing[0], {action: "handled"});
 const second = await emit("input", {source: "interactive", text: "second target", images: [secondImage], streamingBehavior: "followUp"});
 assert.deepEqual(second[0], {action: "handled"});
 callbacks.onComplete({summary: "done"}); await new Promise(resolve => setTimeout(resolve, 10));
 assert.equal(mock.sent.length, 0); assert.equal(mock.users.length, 1);
 assert.deepEqual(mock.users[0], [[
  {type: "text", text: "new target while compacting"}, firstImage,
  {type: "text", text: "second target"}, secondImage,
 ], {deliverAs: "steer"}]);

 callbacks = undefined; await emit("turn_end", {message: {role: "assistant", stopReason: "toolUse"}, toolResults: [{}]});
 await emit("input", {source: "interactive", text: "preserve this target", streamingBehavior: "followUp"});
 pending = true; callbacks.onComplete({summary: "done again"}); await new Promise(resolve => setTimeout(resolve, 10));
 assert.equal(mock.users.length, 2); assert.equal(mock.sent.length, 0);
 assert.deepEqual(mock.users[1], ["preserve this target", {deliverAs: "followUp"}]);

 pending = false; callbacks = undefined;
 await emit("turn_end", {message: {role: "assistant", stopReason: "toolUse"}, toolResults: [{}]});
 await emit("input", {source: "interactive", text: "older queued target", streamingBehavior: "followUp"});
 callbacks.onComplete({summary: "done before stop"});
 await emit("input", {source: "interactive", text: "stop"});
 await new Promise(resolve => setTimeout(resolve, 10));
 assert.equal(mock.users.length, 2, "a newer user action must block replay of older held input");
 assert.equal(mock.sent.length, 1);
 assert.match(mock.sent[0][0].content, /Not executed.*older queued target/s);
 assert.match(notices.at(-1) || "", /preserved but not executed/);
});

test("boundary compaction failure, cancellation, user input and pending input never force a continuation", async (t) => {
 for (const scenario of ["failure", "cancel", "new-input", "pending-input", "aborted-signal"] as const) {
  await t.test(scenario, async () => {
   const mock = mockAPI(); let callbacks: any; let pending = false; const notices: string[] = [];
   registerTurnBoundaryCompaction(mock.api, 80);
   const controller = new AbortController(); if (scenario === "aborted-signal") controller.abort();
   const ctx: any = {
    signal: controller.signal, isIdle: () => true, hasPendingMessages: () => pending,
    getContextUsage: () => ({tokens: 960, contextWindow: 1000, percent: 96}),
    compact: (options: any) => { callbacks = options; },
    ui: {setStatus() {}, notify: (message: string) => notices.push(message)},
   };
   const emit = async(name: string, event: any) => {
    for (const handler of mock.handlers.get(name) || []) await handler(event, ctx);
   };
   await emit("turn_end", {message: {role: "assistant", stopReason: "toolUse"}, toolResults: [{}]});
   if (scenario === "aborted-signal") { assert.equal(callbacks, undefined); return; }
   if (scenario === "failure" || scenario === "cancel") {
    await emit("input", {source: "interactive", text: `${scenario} held input`, streamingBehavior: "steer"});
    callbacks.onError(new Error(scenario === "cancel" ? "Compaction cancelled" : "summary endpoint unavailable"));
   }
   else {
    if (scenario === "new-input") await emit("input", {source: "interactive", text: "stop"});
    if (scenario === "pending-input") pending = true;
    callbacks.onComplete({summary: "done"});
   }
   await new Promise(resolve => setTimeout(resolve, 10));
   assert.equal(mock.sent.length, scenario === "failure" || scenario === "cancel" ? 1 : 0);
   if (mock.sent.length) assert.match(mock.sent[0][0].content, /Not executed.*held input/s);
   assert.equal(mock.users.length, 0);
   if (scenario === "failure") assert.match(notices.at(-1) || "", /failed/);
   if (scenario === "cancel") assert.match(notices.at(-1) || "", /cancelled/);
  });
 }
});

test("pinned Pi AgentSession compacts the real tool loop without duplicate continuation or deadlock", async (t) => {
 const codingAgentEntry = import.meta.resolve("@earendil-works/pi-coding-agent");
 const piAiPackage = findPackageJSON("@earendil-works/pi-ai", codingAgentEntry);
 assert.ok(piAiPackage, "resolve Pi's own pi-ai dependency");
 const piAiEntry = pathToFileURL(join(dirname(piAiPackage), "dist", "index.js")).href;
 const [coding, ai] = await Promise.all([import(codingAgentEntry), import(piAiEntry)]);

 for (const summaryFails of [false, true]) {
  await t.test(summaryFails ? "summary failure stops and preserves held input" : "queued steering resumes from one compaction", async () => {
   const timeline: string[] = []; const notifications: string[] = []; const compactionStarts: string[] = [];
   const provider = `boundary-session-${summaryFails ? "failure" : "success"}-${Date.now()}-${Math.random()}`;
   const faux = ai.fauxProvider({provider, models: [{id: "boundary", contextWindow: 1000, maxTokens: 200}]});
   faux.setResponses([
    () => { timeline.push("model:tool"); return ai.fauxAssistantMessage(ai.fauxToolCall("probe", {}, {id: "probe-1"}), {stopReason: "toolUse"}); },
    (context: any) => {
     assert.match(JSON.stringify(context.messages), /queued correction/); timeline.push("model:steered");
     return ai.fauxAssistantMessage(ai.fauxToolCall("checkpoint", {}, {id: "checkpoint-1"}), {stopReason: "toolUse"});
    },
    () => {
     timeline.push("model:summary");
     return summaryFails
      ? ai.fauxAssistantMessage([], {stopReason: "error", errorMessage: "summary endpoint unavailable"})
      : ai.fauxAssistantMessage("Summary: probe completed and queued correction remains the next instruction.");
    },
    () => { timeline.push("model:turn-prefix-summary"); return ai.fauxAssistantMessage("Turn prefix: queued correction requested the checkpoint."); },
    (context: any) => {
     const serialized = JSON.stringify(context.messages);
     assert.match(serialized, /Summary: probe completed/); assert.match(serialized, /queued correction/);
     timeline.push("model:continued"); return ai.fauxAssistantMessage("task complete");
    },
   ]);
   const modelRuntime = await coding.ModelRuntime.create({refreshOnCreate: false, modelsPath: null});
   modelRuntime.registerNativeProvider(faux.provider); await modelRuntime.setRuntimeApiKey(provider, "test");
   const settingsManager = coding.SettingsManager.inMemory({
    retry: {enabled: false}, compaction: {enabled: true, reserveTokens: 200, keepRecentTokens: 0},
   });
   let releaseTool!: () => void; let finishTool!: () => void;
   const toolStarted = new Promise<void>(resolve => { releaseTool = resolve; });
   const toolMayFinish = new Promise<void>(resolve => { finishTool = resolve; });
   let toolRuns = 0;
   const probe = {
    name: "probe", label: "probe", description: "bounded diagnostic probe", parameters: {type: "object", properties: {}},
    execute: async () => { toolRuns++; timeline.push("tool:probe"); releaseTool(); await toolMayFinish; return {content: [{type: "text", text: "probe completed"}], details: {}}; },
   };
   const checkpoint = {
    name: "checkpoint", label: "checkpoint", description: "record the next diagnostic step", parameters: {type: "object", properties: {}},
    execute: async () => { toolRuns++; timeline.push("tool:checkpoint"); return {content: [{type: "text", text: "checkpoint recorded"}], details: {}}; },
   };
   const resourceLoader = new coding.DefaultResourceLoader({
    cwd: process.cwd(), agentDir: process.cwd(), settingsManager,
    extensionFactories: [(pi: ExtensionAPI) => registerTurnBoundaryCompaction(pi, 80)],
    skillsOverride: () => ({skills: [], diagnostics: []}), agentsFilesOverride: () => ({agentsFiles: []}),
    promptsOverride: () => ({prompts: [], diagnostics: []}),
   });
   await resourceLoader.reload();
   const {session} = await coding.createAgentSession({
    cwd: process.cwd(), agentDir: process.cwd(), model: faux.getModel(), modelRuntime, resourceLoader, settingsManager,
    sessionManager: coding.SessionManager.inMemory(), customTools: [probe, checkpoint], tools: ["probe", "checkpoint"],
   });
   await session.bindExtensions({mode: "tui", uiContext: {
    setStatus() {}, notify(message: string) { notifications.push(message); },
   } as any});
   let agentEnds = 0; let manualCompactionEnded = false; let finalRunEnded = summaryFails;
   let settle!: () => void;
   const settled = new Promise<void>(resolve => { settle = resolve; });
   session.subscribe((event: any) => {
    if (event.type === "agent_end") { agentEnds++; if (!summaryFails && agentEnds === 2) finalRunEnded = true; }
    if (event.type === "compaction_start") compactionStarts.push(event.reason);
    if (event.type === "compaction_end" && event.reason === "manual") {
     manualCompactionEnded = true;
     if (event.result) settingsManager.setCompactionEnabled(false);
    }
    if (manualCompactionEnded && finalRunEnded && !session.isStreaming && !session.isCompacting) settle();
   });
   try {
    const initialRun = session.prompt("x".repeat(4000));
    await toolStarted;
    await session.prompt("queued correction", {streamingBehavior: "steer"});
    finishTool(); await initialRun;
    await Promise.race([settled, new Promise((_, reject) => setTimeout(() => reject(new Error(`AgentSession compaction timed out: ${JSON.stringify({timeline, compactionStarts, agentEnds, streaming: session.isStreaming, compacting: session.isCompacting, calls: faux.state.callCount})}`)), 3000))]);

    assert.equal(toolRuns, 2); assert.equal(session.isStreaming, false); assert.equal(session.isCompacting, false);
    assert.ok(compactionStarts.includes("manual"), JSON.stringify({compactionStarts, timeline}));
    assert.equal(session.sessionManager.getBranch().filter((entry: any) => entry.type === "compaction").length, summaryFails ? 0 : 1);
    if (summaryFails) {
     assert.equal(faux.state.callCount, 3); assert.match(notifications.at(-1) || "", /failed/);
     assert.match(JSON.stringify(session.sessionManager.getBranch()), /queued correction/);
    } else {
     assert.equal(faux.state.callCount, 5, "two tool turns, two split-summary calls, and one resumed turn only");
     assert.deepEqual(timeline, ["model:tool", "tool:probe", "model:steered", "tool:checkpoint", "model:summary", "model:turn-prefix-summary", "model:continued"]);
    }
   } finally { session.dispose(); }
  });
 }
});

test("continuous tasks resume progress endings but respect completion, blockers, cancel and denial", async () => {
 const mock = mockAPI(); let access: 'manual'|'full'|'risk' = 'full', denied = false;
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
 access='risk'; await input(); await ended(); assert.equal(mock.sent.length,4);
 assert.match(mock.sent.at(-1)![0].content,/root risk access/);
 assert.match(mock.sent.at(-1)![0].content,/without additional approval dialogs/);
 assert.doesNotMatch(mock.sent.at(-1)![0].content,/Keep operator approval/);
 access='full'; await input(); await emit('session_start',{}); await ended(); assert.equal(mock.sent.length,4);
});

test("continuous tasks count only successful tool results as new evidence", async () => {
 const mock = mockAPI();
 registerAutonomousTask(mock.api,{access:()=>"full",wasDenied:()=>false,resetDenied:()=>{}});
 const notices: string[] = [];
 const ctx = {hasUI:true,hasPendingMessages:()=>false,ui:{notify:(s:string)=>notices.push(s)}};
 const emit = async(name:string,event:any) => {for (const handler of mock.handlers.get(name)||[]) await handler(event,ctx);};
 const ended = ()=>emit('agent_end',{messages:[{role:'assistant',stopReason:'stop',content:[{type:'text',text:'progress'}]}]});
 const result = (details:any, text='{}') => ({toolName:'infernex_host_exec',isError:false,result:{details,content:[{type:'text',text}]}});
 await emit('input',{source:'interactive',text:'diagnose timeout'});
 await emit('tool_execution_end',{toolName:'infernex_classify_command',isError:false,result:{details:{executed:false},content:[{type:'text',text:'{"assessment":{"level":"read-only"}}'}]}});
 await emit('tool_execution_end',result({exitCode:1})); await ended();
 await emit('tool_execution_end',result({timedOut:true})); await ended();
 await emit('tool_execution_end',result({cancelled:true})); await ended();
 assert.equal(mock.sent.length,2); assert.match(notices.at(-1)!,/三次/);

 await emit('input',{source:'interactive',text:'retry with evidence'});
 await emit('tool_execution_end',result({exitCode:1})); await ended();
 await emit('tool_execution_end',result({exitCode:0},'{"sample":1}')); await ended();
 await emit('tool_execution_end',result({status:'failed'})); await ended();
 await emit('tool_execution_end',result({},'{"status":"failed"}')); await ended();
 assert.equal(mock.sent.length,6);
 await emit('tool_execution_end',result({},'{"exitCode":2}')); await ended();
 assert.equal(mock.sent.length,6); assert.match(notices.at(-1)!,/三次/);
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

test("root risk sends MCP writes unchanged without dialogs and preserves backend errors", async () => {
 const calls: any[] = [];
 globalThis.fetch = (async (_input:any, init:any) => {
  const request = JSON.parse(String(init.body));
  if (request.method === 'tools/list') return Response.json({jsonrpc:'2.0',id:request.id,result:{tools:[{name:'write_cluster',description:'Write cluster state',inputSchema:{type:'object',properties:{name:{type:'string'},confirm:{type:'boolean'}},required:['name','confirm'],additionalProperties:false},annotations:{readOnlyHint:false}}]}});
  calls.push(request.params);
  if (request.params.arguments.name === 'rejected') return Response.json({jsonrpc:'2.0',id:request.id,result:{isError:true,content:[{type:'text',text:'backend validation rejected fake target'}]}});
  return Response.json({jsonrpc:'2.0',id:request.id,result:{structuredContent:{ok:true}}});
 }) as typeof fetch;
 const execute = async (mode:any, command:string) => ({mode,uid:0,cwd:'/tmp',command,exitCode:0,stdout:'0\n',stderr:'',truncated:false,timedOut:false,cancelled:false});
 const mock=mockAPI(); await infernexExtension(mock.api,execute);
 let confirmations=0;
 const ctx={hasUI:true,isIdle:()=>true,ui:{notify:()=>{},setStatus:()=>{},confirm:async()=>{confirmations++;return false;}}};
 await mock.commandHandlers.get('mode_change').handler('root risk',ctx);
 const tool=mock.tools.find(t=>t.name==='write_cluster')!;
 const params={name:'accepted',confirm:true};
 const success:any=await tool.execute('risk-ok',params,undefined,undefined,ctx);
 assert.equal(success.details.access,'risk'); assert.equal(success.details.approval,'risk-preauthorized');
 assert.deepEqual(calls[0],{name:'write_cluster',arguments:params});
 await assert.rejects(tool.execute('risk-error',{name:'rejected',confirm:true},undefined,undefined,ctx),/backend validation rejected fake target/);
 assert.deepEqual(calls[1],{name:'write_cluster',arguments:{name:'rejected',confirm:true}});
 assert.equal(confirmations,0);

 const before=mock.handlers.get('before_agent_start')?.[0]; assert.ok(before);
 const prompt=await before({systemPrompt:'base'},ctx) as {systemPrompt:string};
 assert.match(prompt.systemPrompt,/ROOT RISK ACCESS/);
 assert.match(prompt.systemPrompt,/supply confirm:true/);
 assert.match(prompt.systemPrompt,/does not bypass backend validation, snapshots, ownership, scope, timeouts, cancellation, errors, remote identity, or Kubernetes RBAC/);
 assert.doesNotMatch(prompt.systemPrompt,/Cluster mutations, HCCL\/iperf load tests and unclassified shell commands still require fresh operator approval/);
 installMockFetch();
});

test("full-access prompt directs exact-command preflight and fresh impact approval", async () => {
 installMockFetch(); const mock=mockAPI(); await infernexExtension(mock.api);
 const ctx={hasUI:true,isIdle:()=>true,ui:{notify:()=>{},setStatus:()=>{},confirm:async()=>true}};
 await mock.commandHandlers.get('mode_change').handler('full',ctx);
 const before=mock.handlers.get('before_agent_start')?.[0]; assert.ok(before);
 const result=await before({systemPrompt:'base'},ctx) as {systemPrompt:string};
 assert.match(result.systemPrompt,/exact command and its effects/);
 assert.match(result.systemPrompt,/infernex_classify_command/);
 assert.match(result.systemPrompt,/Invoke rule-automatic commands directly without asking for prose confirmation/);
 assert.match(result.systemPrompt,/approval is not cached/);
 assert.match(result.systemPrompt,/reversible operation can still restart services/);
});
