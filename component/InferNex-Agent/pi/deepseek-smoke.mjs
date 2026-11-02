import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const apiKey = process.env.DEEPSEEK_API_KEY;
assert.ok(apiKey, "DEEPSEEK_API_KEY is required");

const here = dirname(fileURLToPath(import.meta.url));
const piCLI = resolve(here, "node_modules/@earendil-works/pi-coding-agent/dist/cli.js");
const standalonePi = process.env.PI_BINARY;
const modelID = process.env.DEEPSEEK_MODEL || "deepseek-v4-flash";

async function runProfile(name, provider) {
	const agentDir = await mkdtemp(join(tmpdir(), `infernex-pi-${name}-`));
	try {
		await writeFile(
			join(agentDir, "models.json"),
			JSON.stringify({ providers: { probe: provider } }, null, 2),
			{ mode: 0o600 },
		);
		await writeFile(
			join(agentDir, "settings.json"),
			JSON.stringify({ retry: { enabled: false }, httpIdleTimeoutMs: 30_000 }, null, 2),
			{ mode: 0o600 },
		);

		const piArgs = [
				"--provider", "probe",
				"--model", modelID,
				"--no-tools",
				"--no-extensions",
				"--no-skills",
				"--no-prompt-templates",
				"--no-context-files",
				"--no-approve",
				"--offline",
				"--no-session",
				"--thinking", "off",
				"--mode", "json",
				"--print",
				"Reply with exactly: OK",
			];
		const child = spawn(
			standalonePi || process.execPath,
			standalonePi ? piArgs : [piCLI, ...piArgs],
			{
				env: {
					...process.env,
					PI_CODING_AGENT_DIR: agentDir,
					DEEPSEEK_API_KEY: apiKey,
				},
				stdio: ["ignore", "pipe", "pipe"],
			},
		);

		let stdout = "";
		let stderr = "";
		child.stdout.on("data", (chunk) => (stdout += chunk));
		child.stderr.on("data", (chunk) => (stderr += chunk));
		const timer = setTimeout(() => child.kill(), 90_000);
		const exitCode = await new Promise((resolveExit) => child.on("close", resolveExit));
		clearTimeout(timer);
		const parsedEvents = stdout
			.split(/\r?\n/)
			.filter(Boolean)
			.map((line) => {
				try {
					return JSON.parse(line);
				} catch {
					return undefined;
				}
			})
			.filter(Boolean);
		const eventTypes = [...new Set(parsedEvents.map((event) => event.type).filter(Boolean))];
		const failures = parsedEvents
			.map((event) => event.message)
			.filter((message) => message?.stopReason === "error")
			.map((message) => String(message.errorMessage || "unknown error").slice(0, 500));
		const assistantText = parsedEvents
			.map((event) => event.message)
			.filter((message) => message?.role === "assistant")
			.flatMap((message) => Array.isArray(message.content) ? message.content : [])
			.filter((content) => content?.type === "text")
			.map((content) => String(content.text || ""))
			.join("\n");
		const hasOK = /\bOK\b/.test(assistantText);
		console.log(JSON.stringify({ name, exitCode, hasOK, assistantText: assistantText.slice(0, 200), eventTypes, failures: [...new Set(failures)], stderr: stderr.trim().slice(0, 500) }));
		assert.equal(exitCode, 0, `${name} exited with ${exitCode}`);
		assert.equal(hasOK, true, `${name} produced no visible assistant text`);
	} finally {
		await rm(agentDir, { recursive: true, force: true });
	}
}

const commonModel = {
	id: modelID,
	name: modelID,
	reasoning: true,
	contextWindow: 32768,
	maxTokens: 64,
	cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
};

if (!process.env.PROBE_PROFILE || process.env.PROBE_PROFILE === "infernex-generic") await runProfile("infernex-generic", {
	baseUrl: "https://api.deepseek.com/v1",
	api: "openai-completions",
	apiKey: "$DEEPSEEK_API_KEY",
	models: [{
		...commonModel,
		compat: {
			supportsDeveloperRole: false,
			supportsReasoningEffort: false,
			supportsStore: false,
			supportsUsageInStreaming: true,
			supportsStrictMode: false,
			maxTokensField: "max_tokens",
		},
	}],
});

if (!process.env.PROBE_PROFILE || process.env.PROBE_PROFILE === "deepseek-recommended") await runProfile("deepseek-recommended", {
	baseUrl: "https://api.deepseek.com",
	api: "openai-completions",
	apiKey: "$DEEPSEEK_API_KEY",
	models: [{
		...commonModel,
		compat: {
			supportsDeveloperRole: false,
			maxTokensField: "max_tokens",
			thinkingFormat: "deepseek",
			requiresReasoningContentOnAssistantMessages: true,
		},
	}],
});
