import { execFileSync, spawn } from "node:child_process";
import { userInfo } from "node:os";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

export type HostMode = "normal" | "root";
const safePath = "/opt/infernex-agent/tools/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/Ascend/driver/tools";
const outputLimit = 1024 * 1024;
const identityWrapper = '/usr/bin/id -u >&3; exec 3>&-; exec /bin/bash --noprofile --norc -c "$1"';
export const quote = (s: string) => "'" + s.replaceAll("'", "'\\''") + "'";

export function executionSpec(mode: HostMode, command: string, normalUser = "infernex-agent", rootWorkspace = process.cwd()) {
	if (process.platform !== "linux") throw new Error("Host permission modes require Linux");
	if (!/^[a-z_][a-z0-9_-]*[$]?$/.test(normalUser) || normalUser === "root") throw new Error("normal host user must be a non-root account");
	const uid = process.getuid!();
	let home: string, cwd: string, file: string, args: string[], targetUID: number;
	if (mode === "root") {
		if (uid !== 0) throw new Error("Root mode requires launching sudo /opt/infernex-agent/bin/tui.sh");
		home = userInfo().homedir; cwd = rootWorkspace; file = "/bin/bash"; args = ["--noprofile", "--norc", "-c", identityWrapper, "infernex-host", command]; targetUID = 0;
	} else {
		const account = execFileSync("/usr/bin/getent", ["passwd", normalUser], { encoding: "utf8", env: { PATH: safePath }, timeout: 3000 }).trim().split(":");
		targetUID = Number(account[2]);
		if (account[0] !== normalUser || !Number.isInteger(targetUID) || targetUID <= 0) throw new Error("normal host user lookup failed");
		home = account[5]; cwd = home;
		// setpriv clears inherited groups, drops all capabilities and prevents
		// setuid/sudo from undoing a normal-mode command's identity transition.
		file = "/usr/bin/setpriv";
		args = ["--no-new-privs"];
		if (uid === 0) args.push("--reuid", account[2], "--regid", account[3], "--init-groups", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all");
		else if (uid !== targetUID) throw new Error(`Normal mode requires ${normalUser} or a root-launched TUI`);
		args.push("/bin/bash", "--noprofile", "--norc", "-c", identityWrapper, "infernex-host", command);
	}
	// Do not pass model API keys, sudo state, BASH_ENV or loader injection into
	// diagnostic commands. CANN setup scripts can be explicitly sourced.
	return { file, args, cwd, uid: targetUID, env: { PATH: safePath, HOME: home, USER: mode === "root" ? "root" : normalUser, LOGNAME: mode === "root" ? "root" : normalUser, LANG: "C.UTF-8" } };
}

export async function runHostCommand(mode: HostMode, command: string, timeoutSeconds: number, signal?: AbortSignal, normalUser = "infernex-agent", rootWorkspace = process.cwd()) {
	if (!command.trim() || command.length > 32768 || !Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 || timeoutSeconds > 600) throw new Error("command and timeout (1–600 seconds) are required");
	if (signal?.aborted) throw new Error("Host command cancelled before execution");
	const spec = executionSpec(mode, command, normalUser, rootWorkspace);
	return await new Promise<{ mode: HostMode; uid: number | null; cwd: string; command: string; exitCode: number | null; stdout: string; stderr: string; truncated: boolean; timedOut: boolean; cancelled: boolean }>((resolve, reject) => {
		const child = spawn(spec.file, spec.args, { cwd: spec.cwd, env: spec.env, detached: true, stdio: ["ignore", "pipe", "pipe", "pipe"] });
		const stdout: Buffer[] = [], stderr: Buffer[] = [];
		let identity = "";
		child.stdio[3]?.on("data", (data: Buffer) => { if (identity.length < 32) identity += data.toString().slice(0, 32 - identity.length); });
		let outBytes = 0, errBytes = 0, truncated = false, timedOut = false, cancelled = false;
		const collect = (chunks: Buffer[], size: number, data: Buffer) => { const kept = data.subarray(0, Math.max(0, outputLimit - size)); if (kept.length < data.length) truncated = true; chunks.push(kept); return size + kept.length; };
		child.stdout!.on("data", (data: Buffer) => { outBytes = collect(stdout, outBytes, data); });
		child.stderr!.on("data", (data: Buffer) => { errBytes = collect(stderr, errBytes, data); });
		const stop = () => { if (child.pid) { try { process.kill(-child.pid, "SIGKILL"); } catch (error) { if ((error as NodeJS.ErrnoException).code !== "ESRCH") child.kill("SIGKILL"); } } };
		const abort = () => { cancelled = true; stop(); };
		const timer = setTimeout(() => { timedOut = true; stop(); }, timeoutSeconds * 1000);
		signal?.addEventListener("abort", abort, { once: true });
		if (signal?.aborted) abort();
		const cleanup = () => { clearTimeout(timer); signal?.removeEventListener("abort", abort); };
		child.on("error", error => { cleanup(); reject(error); });
		child.on("close", exitCode => { cleanup(); resolve({ mode, uid: /^\d+\n$/.test(identity) ? Number(identity.trim()) : null, cwd: spec.cwd, command, exitCode, stdout: Buffer.concat(stdout).toString(), stderr: Buffer.concat(stderr).toString(), truncated, timedOut, cancelled }); });
	});
}

const integer = (n: unknown, min: number, max: number, name: string): number => { if (!Number.isInteger(n) || (n as number) < min || (n as number) > max) throw new Error(`${name} must be ${min}–${max}`); return n as number; };
const target = (s: unknown): string => { if (typeof s !== "string" || s.length > 253 || !/^[a-zA-Z0-9][a-zA-Z0-9.:%_-]*$/.test(s)) throw new Error("explicit hostname or IP is required"); return s; };
const absolutePath = (s: unknown): string => { if (typeof s !== "string" || !s.startsWith("/") || /[\x00\r\n]/.test(s)) throw new Error("absolute tool/configuration path is required"); return s; };
export function onSSH(command: string, alias?: string): string {
	if (!alias) return command;
	if (!/^[a-zA-Z0-9][a-zA-Z0-9._@:-]{0,252}$/.test(alias)) throw new Error("invalid SSH alias or user@host");
	return `ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -- ${quote(alias)} ${quote(command)}`;
}

export function networkCommand(input: { probe: string; target?: string; port?: number; iface?: string; sshTarget?: string }): string {
	let args: string[];
	switch (input.probe) {
		case "addresses": args = ["ip", "-brief", "address"]; break;
		case "routes": args = ["ip", "route", "show", "table", "all"]; break;
		case "sockets": args = ["ss", "-s"]; break;
		case "tcp-counters": args = ["nstat", "-az"]; break;
		case "rdma-links": args = ["rdma", "link", "show"]; break;
		case "rdma-counters": args = ["rdma", "statistic", "show"]; break;
		case "ping": args = ["ping", "-n", "-c", "4", "-W", "2", target(input.target)]; break;
		case "dns": args = ["getent", "ahosts", target(input.target)]; break;
		case "traceroute": args = ["traceroute", "-n", "-m", "16", "-w", "1", target(input.target)]; break;
		case "tcp-connect": args = ["nc", "-z", "-v", "-w", "3", target(input.target), String(integer(input.port, 1, 65535, "port"))]; break;
		case "ethtool-stats":
			if (!input.iface || !/^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,14}$/.test(input.iface)) throw new Error("interface is required");
			args = ["ethtool", "-S", input.iface]; break;
		case "iperf-client": args = ["iperf3", "-c", target(input.target), "-p", String(integer(input.port ?? 5201, 1, 65535, "port")), "-t", "10", "-J"]; break;
		default: throw new Error("unsupported network probe");
	}
	return onSSH(args.map(quote).join(" "), input.sshTarget);
}

export function pfcCommand(input: { deviceIds: number[]; samples?: number; intervalSeconds?: number; sshTarget?: string }) {
	if (!Array.isArray(input.deviceIds) || !input.deviceIds.length || input.deviceIds.length > 64) throw new Error("deviceIds are required");
	const devices = [...new Set(input.deviceIds.map(n => integer(n, 0, 63, "deviceId")))];
	const samples = integer(input.samples ?? 2, 2, 12, "samples"), interval = integer(input.intervalSeconds ?? 10, 1, 30, "intervalSeconds");
	const command = `tool=$(command -v hccn_tool || true); [ -n "$tool" ] || tool=/usr/local/Ascend/driver/tools/hccn_tool; failed=0; sample=0; while [ "$sample" -lt ${samples} ]; do date -u '+%Y-%m-%dT%H:%M:%SZ'; for device in ${devices.join(" ")}; do printf 'device=%s\\n' "$device"; "$tool" -i "$device" -stat -g || failed=1; done; sample=$((sample+1)); [ "$sample" -ge ${samples} ] || sleep ${interval}; done; exit "$failed"`;
	return onSSH(command, input.sshTarget);
}

export function hcclCommand(input: { executable: string; ranks: number; devicesPerNode: number; minBytes?: number; maxBytes?: number; hostfile?: string; setupScript?: string }, mode: HostMode) {
	const executable = absolutePath(input.executable);
	if (!/\/(all_reduce|all_gather|broadcast|reduce_scatter)_test$/.test(executable)) throw new Error("supported HCCL executable: all_reduce/all_gather/broadcast/reduce_scatter_test");
	const ranks = integer(input.ranks, 1, 512, "ranks"), devices = integer(input.devicesPerNode, 1, 64, "devicesPerNode");
	if (ranks % devices !== 0 || (!input.hostfile && ranks !== devices)) throw new Error("ranks must match devices per node; multi-node execution requires a hostfile");
	const min = integer(input.minBytes ?? 8192, 1, 1073741824, "minBytes"), max = integer(input.maxBytes ?? 67108864, min, 1073741824, "maxBytes");
	const args = ["mpirun", ...(mode === "root" ? ["--allow-run-as-root"] : []), ...(input.hostfile ? ["-f", absolutePath(input.hostfile)] : []), "-n", String(ranks), executable, "-b", String(min), "-e", String(max), "-f", "2", "-d", "fp32", ...(/\/(all_reduce|reduce_scatter)_test$/.test(executable) ? ["-o", "sum"] : []), "-p", String(devices)];
	return (input.setupScript ? `source ${quote(absolutePath(input.setupScript))} && ` : "") + "exec " + args.map(quote).join(" ");
}

export function registerHostTools(pi: ExtensionAPI, formatResult: (text: string) => Promise<{ text: string }> = async text => ({ text })) {
	let mode: HostMode = "normal", busy = 0;
	const normalUser = process.env.INFERNEX_HOST_USER || "infernex-agent";
	const rootWorkspace = process.env.INFERNEX_WORKSPACE_ROOT || process.cwd();
	const modeLabel = () => `${mode} · local commands as ${mode === "root" ? "root" : normalUser}`;
	pi.registerCommand("mode_change", {
		description: "Switch local command identity: /mode_change root | normal | status",
		handler: async (args, ctx) => {
			const next = args.trim().toLowerCase();
			if (!next || next === "status") { ctx.ui.notify(`${modeLabel()}; background MCP and Pod/SSH remote identity are separate`, "info"); return; }
			if (next !== "normal" && next !== "root") { ctx.ui.notify("Usage: /mode_change root | normal | status", "error"); return; }
			if (!ctx.hasUI || busy) { ctx.ui.notify("Wait for active host commands to finish; mode changes require an interactive terminal", "error"); return; }
			try {
				busy++;
				const check = await runHostCommand(next, "/usr/bin/id -u", 5, undefined, normalUser, rootWorkspace);
				if (check.exitCode !== 0 || !/^\d+\n$/.test(check.stdout) || Number(check.stdout.trim()) !== check.uid) throw new Error(check.stderr || "UID check failed");
				mode = next;
				ctx.ui.setStatus("host-mode", modeLabel());
				ctx.ui.notify(`${modeLabel()}; verified uid=${check.uid}. Existing collectors are not stopped by a mode switch.`, "info");
			} catch (error) { ctx.ui.notify(`Mode unchanged: ${String(error)}`, "error"); }
			finally { busy--; }
		},
	});
	pi.on("session_start", (_event, ctx) => { mode = "normal"; ctx.ui.setStatus("host-mode", modeLabel()); });
	// All local execution goes through the credential-aware tool, including
	// filesystem inspection; root-owned builtins cannot bypass normal mode.
	pi.on("tool_call", async (event) => {
		if (["bash", "read", "write", "edit", "grep", "find", "ls"].includes(event.toolName)) return { block: true, reason: "Use infernex_host_exec so /mode_change controls the actual command UID" };
	});
	const register = (name: string, description: string, properties: Record<string, unknown>, required: string[], build: (input: any, selected: HostMode) => string, defaultTimeout: number) => {
		pi.registerTool({ name, label: name, description, parameters: { type: "object", properties: { ...properties, timeoutSeconds: { type: "integer", minimum: 1, maximum: 600 } }, required, additionalProperties: false } as any,
			async execute(_id, params, signal, _update, ctx) {
				if (!ctx.hasUI) throw new Error("Host execution requires an interactive terminal");
				if (busy) throw new Error("Another host command or mode transition is active");
				busy++;
				const selected = mode;
				try {
					const command = build(params, selected);
					if (!await ctx.ui.confirm(`Run ${name} · ${modeLabel()}`, command)) throw new Error("Operator denied host command");
					const output = await runHostCommand(selected, command, (params as any).timeoutSeconds ?? defaultTimeout, signal, normalUser, rootWorkspace);
					const formatted = await formatResult(JSON.stringify(output));
					return { content: [{ type: "text", text: formatted.text }], details: { mode: output.mode, uid: output.uid, exitCode: output.exitCode, timedOut: output.timedOut, cancelled: output.cancelled, truncated: output.truncated } };
				} finally { busy--; }
			},
		});
	};
	const string = { type: "string" }, number = { type: "integer" };
	register("infernex_host_exec", "Execute an approved host shell command as the current /mode_change user. Supports SSH, kubectl exec/plog, files and installed diagnostic tools. Returns actual local UID, command, exit code and bounded output; never assume remote UID or RBAC from local mode.", { command: string }, ["command"], input => input.command, 60);
	register("infernex_network_probe", "Inspect addresses, routes, sockets, TCP/RDMA counters, DNS, ping, traceroute, TCP connectivity or ethtool statistics locally or via SSH. iperf-client generates 10 seconds of traffic. Tools must be installed on the execution target.", { probe: { enum: ["addresses", "routes", "sockets", "tcp-counters", "rdma-links", "rdma-counters", "ping", "dns", "traceroute", "tcp-connect", "ethtool-stats", "iperf-client"] }, target: string, port: number, iface: string, sshTarget: string }, ["probe"], networkCommand, 60);
	register("infernex_sample_pfc", "Collect timestamped hccn_tool -stat -g snapshots for selected devices locally or over SSH. Compare counter deltas; a nonzero lifetime counter alone does not prove current backpressure. Raw fields vary by driver. Long Pod collection remains available through CollectorRun.", { deviceIds: { type: "array", items: number, minItems: 1, maxItems: 64 }, samples: number, intervalSeconds: number, sshTarget: string }, ["deviceIds"], pfcCommand, 600);
	register("infernex_run_hccl_test", "Run an approved HCCL collective benchmark using installed mpirun and an explicit test binary. Consumes NPU/network resources. Optional hostfile enables multi-node execution; optional CANN setupScript prepares libraries. Timeout stops the local process group; verify remote MPI ranks have exited before retrying. Exit success alone is not a performance acceptance result.", { executable: string, ranks: number, devicesPerNode: number, minBytes: number, maxBytes: number, hostfile: string, setupScript: string }, ["executable", "ranks", "devicesPerNode"], hcclCommand, 300);
	return { mode: () => mode, label: modeLabel };
}
