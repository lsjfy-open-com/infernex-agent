import { execFileSync, spawn } from "node:child_process";
import { userInfo } from "node:os";
import type { ExtensionAPI, ToolDefinition } from "@earendil-works/pi-coding-agent";

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

export type AccessMode = "manual" | "full";

// Deliberately parse a single simple command, not the shell language. Unknown
// syntax, scripts, redirects and compound commands keep the operator gate.
export function readOnlyHostCommand(command: string, depth = 0): boolean {
 if (depth > 2 || /[\x00-\x1f\x7f$`;|&<>\\*?{}\[\]!~]/.test(command)) return false;
 const words: string[] = [];
 let previous = 0;
 for (const match of command.matchAll(/'[^']*'|"[^"]*"|[^\s'"]+/g)) {
  if (match.index! > previous && !/^\s+$/.test(command.slice(previous, match.index))) return false;
  if (match.index === previous && previous !== 0) return false;
  words.push(match[0].replace(/^(['"])(.*)\1$/, "$2"));
  previous = match.index! + match[0].length;
 }
 if (!/^\s*$/.test(command.slice(previous)) || !words.length) return false;
 const [raw, ...args] = words;
 if (raw.includes("/") && !/^\/(?:usr\/)?bin\/[a-z0-9_-]+$/.test(raw)) return false;
 const executable = raw.split("/").at(-1)!;
 if (args.some(a => a.startsWith("/dev/") || a === "/proc/kcore")) return false;
 const flags = (allowed: RegExp) => args.every(a => !a.startsWith("-") || allowed.test(a));
 switch (executable) {
  case "id": return args.length === 0 || (args.length === 1 && /^-[ugGn]$/.test(args[0]));
  case "uname": return flags(/^-[asnrvmop]+$/);
  case "hostname": case "uptime": case "whoami": return args.length === 0;
  case "date": return args.length === 0 || (args.length === 1 && args[0] === "-u");
  case "ls": return flags(/^(?:--|-[lahndtSr]+|--color=never)$/);
  case "cat": return args.length > 0 && flags(/^(?:--|-[nbsETv]+)$/);
  case "head": case "tail": return flags(/^(?:--|-[nc]|-[nc]?[0-9]+)$/);
  case "grep": return flags(/^(?:--|-[nEiFHv]+|-[ABC][0-9]+)$/);
  case "ps": return args.length === 0 || (args.length === 1 && ["aux", "-ef", "-e"].includes(args[0]));
  case "ip": return ["-brief address", "address show", "addr show", "route show", "route show table all", "link show"].includes(args.join(" "));
  case "ss": return args.length === 1 && /^-[santulp]+$/.test(args[0]);
  case "nstat": return args.join(" ") === "-az";
  case "rdma": return ["link show", "statistic show"].includes(args.join(" "));
  case "ethtool": return args.length === 2 && args[0] === "-S" && /^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,14}$/.test(args[1]);
  case "ssh": {
   // No caller-supplied SSH options (ProxyCommand, forwarding, config files).
   if (!/^[a-zA-Z0-9][a-zA-Z0-9._@:-]{0,252}$/.test(args[0] || "") || args.length < 2) return false;
   return readOnlyHostCommand(args.slice(1).join(" "), depth + 1);
  }
  case "kubectl": {
   let index = 0;
   while (["-n", "--namespace", "--context"].includes(args[index])) {
    if (!/^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$/.test(args[index+1] || "")) return false;
    index += 2;
   }
   const verb = args[index++], rest = args.slice(index);
   if (verb === "exec") {
    const separator = rest.indexOf("--");
    if (separator < 1) return false;
    const prefix = rest.slice(0,separator);
    if (!/^[a-zA-Z0-9][a-zA-Z0-9.-]*$/.test(prefix[0])) return false;
    if (prefix.length !== 1 && !(prefix.length === 3 && prefix[1] === "-c" && /^[a-zA-Z0-9][a-zA-Z0-9.-]*$/.test(prefix[2]))) return false;
    return readOnlyHostCommand(rest.slice(separator+1).join(" "), depth+1);
   }
   if (!["get", "describe", "logs", "top", "version", "api-resources"].includes(verb)) return false;
   return rest.every(a => !a.startsWith("-") || /^(?:-A|--all-namespaces|--previous|-p|--timestamps|--all-containers|--no-headers|-n|--namespace|-c|--container|-l|--selector|-o|--output|--tail|--since|--limit-bytes|--field-selector)(?:=[a-zA-Z0-9_.,:/=-]+)?$/.test(a));
  }
  default: return false;
 }
}

// Explicit local-only mutations. Unknown MCP writes always retain approval;
// model-supplied risk labels or confirm=true cannot bypass this policy.
const autonomousLocalTools = new Set([
 "infernex_create_markdown_report", "infernex_remember", "infernex_forget_memory",
 "infernex_start_plog_capture", "infernex_stop_plog_capture",
 "infernex_start_collector_run", "infernex_stop_collector_run",
]);
export function needsMCPApproval(access: AccessMode, name: string, readOnly: boolean): boolean {
 if (["infernex_deploy_model", "infernex_delete_model", "infernex_start_experiment"].includes(name)) return true;
 if (readOnly) return false;
 return access !== "full" || !autonomousLocalTools.has(name);
}
export function needsHostApproval(access: AccessMode, name: string, input: any): boolean {
 if (access !== "full") return true;
 if (name === "infernex_sample_pfc") return false;
 if (name === "infernex_network_probe") return !["addresses", "routes", "sockets", "tcp-counters", "rdma-links", "rdma-counters", "ping", "dns", "traceroute", "tcp-connect", "ethtool-stats"].includes(input.probe);
 if (name === "infernex_host_exec") return !readOnlyHostCommand(input.command);
 return true; // HCCL benchmarks, future tools, and unknown commands.
}

export function registerHostTools(pi: ExtensionAPI, formatResult: (text: string) => Promise<{ text: string }> = async text => ({ text }), renderer?: (label: string) => Pick<ToolDefinition, "renderShell" | "renderCall" | "renderResult">, executeCommand: typeof runHostCommand = runHostCommand) {
	let mode: HostMode = "normal", access: AccessMode = "manual", busy = 0, denied = false;
	const normalUser = process.env.INFERNEX_HOST_USER || "infernex-agent";
	const rootWorkspace = process.env.INFERNEX_WORKSPACE_ROOT || process.cwd();
	const modeLabel = () => `${mode} · ${access === "full" ? "完全访问（集群影响需批准）" : "逐次批准"} · local commands as ${mode === "root" ? "root" : normalUser}`;
	pi.registerCommand("mode_change", {
		description: "Set identity and access: /mode_change [root|normal] [full|manual] | status",
		handler: async (args, ctx) => {
   const tokens = args.trim().toLowerCase().split(/\s+/).filter(Boolean);
   if (!tokens.length || tokens.join(" ") === "status") { ctx.ui.notify(`${modeLabel()}; background MCP and Pod/SSH remote identity are separate`, "info"); return; }
   if (tokens.length > 2 || new Set(tokens).size !== tokens.length || tokens.some(t => !["normal", "root", "full", "manual"].includes(t)) || (tokens.includes("root") && tokens.includes("normal")) || (tokens.includes("full") && tokens.includes("manual"))) { ctx.ui.notify("Usage: /mode_change [root|normal] [full|manual] | status", "error"); return; }
   if (!ctx.hasUI || busy || (ctx.isIdle && !ctx.isIdle())) { ctx.ui.notify("Wait for active operations to finish; mode changes require an idle interactive terminal", "error"); return; }
   const next = (tokens.find(t => t === "root" || t === "normal") || mode) as HostMode;
   const nextAccess = (tokens.find(t => t === "full" || t === "manual") || access) as AccessMode;
			try {
				busy++;
				const check = next === mode ? undefined : await executeCommand(next, "/usr/bin/id -u", 5, undefined, normalUser, rootWorkspace);
				if (check && (check.exitCode !== 0 || !/^\d+\n$/.test(check.stdout) || Number(check.stdout.trim()) !== check.uid)) throw new Error(check.stderr || "UID check failed");
				mode = next; access = nextAccess;
				ctx.ui.setStatus("host-mode", modeLabel());
				ctx.ui.notify(`${modeLabel()}; uid=${check?.uid ?? "unchanged"}. Existing collectors are not stopped by a mode switch.`, "info");
			} catch (error) { ctx.ui.notify(`Mode unchanged: ${String(error)}`, "error"); }
			finally { busy--; }
		},
	});
	pi.on("session_start", (_event, ctx) => { mode = "normal"; access = "manual"; denied = false; ctx.ui.setStatus("host-mode", modeLabel()); });
	// All local execution goes through the credential-aware tool, including
	// filesystem inspection; root-owned builtins cannot bypass normal mode.
	pi.on("tool_call", async (event) => {
		if (["bash", "read", "write", "edit", "grep", "find", "ls"].includes(event.toolName)) return { block: true, reason: "Use infernex_host_exec so /mode_change controls the actual command UID" };
	});
	const register = (name: string, description: string, properties: Record<string, unknown>, required: string[], build: (input: any, selected: HostMode) => string, defaultTimeout: number) => {
		pi.registerTool({ ...renderer?.(({ infernex_host_exec: "本机命令", infernex_network_probe: "网络探测", infernex_sample_pfc: "PFC 采样", infernex_run_hccl_test: "HCCL 测试" } as Record<string, string>)[name] || name), name, label: name, description, parameters: { type: "object", properties: { ...properties, timeoutSeconds: { type: "integer", minimum: 1, maximum: 600 } }, required, additionalProperties: false } as any,
			async execute(_id, params, signal, _update, ctx) {
				if (!ctx.hasUI) throw new Error("Host execution requires an interactive terminal");
				if (busy) throw new Error("Another host command or mode transition is active");
				busy++;
				const selected = mode;
				try {
					const command = build(params, selected);
					const requiresApproval = needsHostApproval(access, name, params);
 if (requiresApproval && !await ctx.ui.confirm(`Run ${name} · ${modeLabel()}`, `${command}\n\n人工判定：此操作可能改变运行状态、产生压测负载，或无法可靠判定影响。`)) { denied = true; throw new Error("Operator denied host command; do not retry or bypass this decision"); }
					const output = await executeCommand(selected, command, (params as any).timeoutSeconds ?? defaultTimeout, signal, normalUser, rootWorkspace);
					const formatted = await formatResult(JSON.stringify(output));
					return { content: [{ type: "text", text: formatted.text }], details: { access, approval: requiresApproval ? "operator" : "automatic", mode: output.mode, uid: output.uid, exitCode: output.exitCode, timedOut: output.timedOut, cancelled: output.cancelled, truncated: output.truncated } };
				} finally { busy--; }
			},
		});
	};
	const string = { type: "string" }, number = { type: "integer" };
	register("infernex_host_exec", "Execute an approved host shell command as the current /mode_change user. Supports SSH, kubectl exec/plog, files and installed diagnostic tools. Returns actual local UID, command, exit code and bounded output; never assume remote UID or RBAC from local mode.", { command: string }, ["command"], input => input.command, 60);
	register("infernex_network_probe", "Inspect addresses, routes, sockets, TCP/RDMA counters, DNS, ping, traceroute, TCP connectivity or ethtool statistics locally or via SSH. iperf-client generates 10 seconds of traffic. Tools must be installed on the execution target.", { probe: { enum: ["addresses", "routes", "sockets", "tcp-counters", "rdma-links", "rdma-counters", "ping", "dns", "traceroute", "tcp-connect", "ethtool-stats", "iperf-client"] }, target: string, port: number, iface: string, sshTarget: string }, ["probe"], networkCommand, 60);
	register("infernex_sample_pfc", "Collect timestamped hccn_tool -stat -g snapshots for selected devices locally or over SSH. Compare counter deltas; a nonzero lifetime counter alone does not prove current backpressure. Raw fields vary by driver. Long Pod collection remains available through CollectorRun.", { deviceIds: { type: "array", items: number, minItems: 1, maxItems: 64 }, samples: number, intervalSeconds: number, sshTarget: string }, ["deviceIds"], pfcCommand, 600);
	register("infernex_run_hccl_test", "Run an approved HCCL collective benchmark using installed mpirun and an explicit test binary. Consumes NPU/network resources. Optional hostfile enables multi-node execution; optional CANN setupScript prepares libraries. Timeout stops the local process group; verify remote MPI ranks have exited before retrying. Exit success alone is not a performance acceptance result.", { executable: string, ranks: number, devicesPerNode: number, minBytes: number, maxBytes: number, hostfile: string, setupScript: string }, ["executable", "ranks", "devicesPerNode"], hcclCommand, 300);
	return { mode: () => mode, access: () => access, label: modeLabel, wasDenied: () => denied, resetDenied: () => { denied = false; },
  async operation<T>(action: () => Promise<T>): Promise<T> {
   if (busy) throw new Error("Another host command or mode transition is active");
   busy++;
   try { return await action(); } finally { busy--; }
  },
 };
}
