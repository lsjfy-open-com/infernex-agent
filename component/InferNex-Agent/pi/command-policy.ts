export type HostCommandLevel = "read-only" | "bounded-diagnostic" | "cluster-change" | "unknown";
export type HostCommandClassification = { level: HostCommandLevel; reason: string };

type Token = { value: string; raw: string; quoted: boolean; emptyQuote: boolean };
type ParsedShell = { pipelines: Token[][] } | { error: string };

const result = (level: HostCommandLevel, reason: string): HostCommandClassification => ({ level, reason });
const unknown = (reason: string) => result("unknown", reason);
const safeLevels = new Set<HostCommandLevel>(["read-only", "bounded-diagnostic"]);
const simpleName = /^[a-zA-Z0-9][a-zA-Z0-9_.:@%+,-]*$/;
const resourceName = /^[a-zA-Z0-9][a-zA-Z0-9_.:/@%+-]*$/;

/** Parse only the small shell subset the policy understands. */
function parseShell(command: string): ParsedShell {
	if (!command.trim()) return { error: "empty command" };
	if (command.length > 32768) return { error: "command is too long" };
	const pipelines: Token[][] = [[]];
	let value = "", raw = "", quote: "'" | '"' | undefined;
	let started = false, quoted = false, emptyQuote = false, quoteChars = 0;
	const finish = () => {
		if (!started) return;
		pipelines.at(-1)!.push({ value, raw, quoted, emptyQuote });
		value = ""; raw = ""; started = false; quoted = false; emptyQuote = false; quoteChars = 0;
	};
	for (let i = 0; i < command.length; i++) {
		const c = command[i];
		if (/[\x00-\x08\x0a-\x1f\x7f]/.test(c)) return { error: "control character" };
		if (quote) {
			raw += c;
			if (c === quote) {
				if (quoteChars === 0) emptyQuote = true;
				quote = undefined;
			} else {
				if (quote === '"' && (c === "$" || c === "`" || c === "\\")) return { error: "expansion in double quotes" };
				value += c; quoteChars++;
			}
			continue;
		}
		if (/\s/.test(c)) { finish(); continue; }
		if (c === "'" || c === '"') {
			started = true; quoted = true; raw += c; quote = c; quoteChars = 0; continue;
		}
		if (c === "\\") {
			const next = command[++i];
			if (next === undefined || /[\x00-\x1f\x7f]/.test(next)) return { error: "invalid shell escape" };
			started = true; quoted = true; raw += `\\${next}`; value += next; continue;
		}
		if (c === "|") {
			finish();
			if (!pipelines.at(-1)!.length || command[i + 1] === "|") return { error: "invalid pipeline" };
			pipelines.push([]); continue;
		}
		// Redirects, command/control operators, expansion and globbing are outside
		// the policy grammar. A backslash escape above is decoded as one literal
		// argument character; recursive SSH classification exposes it again when
		// OpenSSH constructs the remote shell command.
		if (";&<>$`() *?[]{}!~".replace(" ", "").includes(c) || (c === "#" && !started)) return { error: `unsupported shell syntax ${c}` };
		started = true; raw += c; value += c;
	}
	if (quote) return { error: "unterminated quote" };
	finish();
	if (!pipelines.at(-1)!.length) return { error: "incomplete pipeline" };
	return { pipelines };
}

function executableName(token: Token): string | undefined {
	const isOneNonemptyQuote = /^'[^']+'$/.test(token.raw) || /^"[^"$`\\]+"$/.test(token.raw);
	if ((token.quoted && !isOneNonemptyQuote) || token.emptyQuote) return undefined;
	const raw = token.value;
	if (raw.includes("/")) {
		const match = raw.match(/^\/(?:usr\/)?bin\/([a-z0-9_-]+)$/);
		return match?.[1];
	}
	return /^[a-z0-9][a-z0-9_-]*$/.test(raw) ? raw : undefined;
}

function unsafeReadPath(value: string): boolean {
	return value === "/dev" || value.startsWith("/dev/") || value === "/proc/kcore";
}

function operandsAndFlags(args: string[], allowed: RegExp, requireOperand = false): boolean {
	let operands = 0, end = false;
	for (const arg of args) {
		if (!end && arg === "--") { end = true; continue; }
		if (!end && arg.startsWith("-")) { if (!allowed.test(arg)) return false; }
		else { if (unsafeReadPath(arg)) return false; operands++; }
	}
	return !requireOperand || operands > 0;
}

function durationSeconds(value: string): number | undefined {
	const match = value.match(/^(\d+(?:\.\d+)?)(ms|s|m|h|d)?$/);
	if (!match) return undefined;
	const scale = match[2] === "ms" ? .001 : match[2] === "m" ? 60 : match[2] === "h" ? 3600 : match[2] === "d" ? 86400 : 1;
	return Number(match[1]) * scale;
}

function classifyLocal(name: string, args: string[]): HostCommandClassification | undefined {
	if (args.some(unsafeReadPath)) return unknown("device and kernel-memory paths are not automatic reads");
	switch (name) {
		case "id": return args.length === 0 || (args.length === 1 && /^-[ugGn]$/.test(args[0])) ? result("read-only", "identity query") : unknown("unsupported id arguments");
		case "uname": return operandsAndFlags(args, /^-[asnrvmop]+$/) ? result("read-only", "kernel identity query") : unknown("unsupported uname arguments");
		case "hostname": case "uptime": case "whoami": return args.length === 0 ? result("read-only", `${name} query`) : unknown(`${name} arguments can change or obscure intent`);
		case "sleep": {
			const seconds = args.length === 1 ? durationSeconds(args[0]) : undefined;
			return seconds !== undefined && seconds > 0 && seconds <= 60 ? result("bounded-diagnostic", "bounded diagnostic delay") : unknown("sleep must be between 0 and 60 seconds");
		}
		case "date": return args.length === 0 || (args.length === 1 && args[0] === "-u") ? result("read-only", "clock query") : unknown("date setting and formatting are not classified");
		case "ls": return operandsAndFlags(args, /^(?:--|-[lahndtSr]+|--color=never)$/) ? result("read-only", "directory listing") : unknown("unsupported ls flag");
		case "cat": return operandsAndFlags(args, /^(?:--|-[nbsETv]+)$/, true) ? result("read-only", "file read") : unknown("cat requires ordinary files and read flags");
		case "head": case "tail": {
			let expectNumber = false;
			for (const arg of args) {
				if (expectNumber) { if (!/^\d+$/.test(arg)) return unknown(`unsupported ${name} count`); expectNumber = false; continue; }
				if (["-n", "-c"].includes(arg)) { expectNumber = true; continue; }
				if (/^-[nc]?\d+$/.test(arg) || /^--(?:lines|bytes)=\d+$/.test(arg) || arg === "--" || !arg.startsWith("-")) { if (unsafeReadPath(arg)) return unknown("unsafe read path"); continue; }
				return unknown(`unsupported ${name} flag`);
			}
			return !expectNumber ? result("read-only", `bounded ${name} selection`) : unknown(`missing ${name} count`);
		}
		case "grep": {
			let need: "number" | "value" | undefined;
			for (const arg of args) {
				if (need) { if (need === "number" && !/^\d+$/.test(arg)) return unknown("invalid grep context"); need = undefined; continue; }
				if (["-A", "-B", "-C"].includes(arg)) { need = "number"; continue; }
				if (["-e", "-f"].includes(arg)) { need = "value"; continue; }
				if (arg === "--" || /^-[nEiFHv]+$/.test(arg) || /^-[ABC]\d+$/.test(arg) || !arg.startsWith("-")) continue;
				return unknown("unsupported grep flag");
			}
			return !need ? result("read-only", "text selection") : unknown("missing grep option value");
		}
		case "rg": {
			let need: "number" | "value" | undefined;
			for (const arg of args) {
				if (need) { if (need === "number" && !/^\d+$/.test(arg)) return unknown("invalid rg bound"); need = undefined; continue; }
				if (["-A", "-B", "-C", "-m", "--max-count", "--max-depth"].includes(arg)) { need = "number"; continue; }
				if (["-g", "--glob", "-t", "--type", "--type-not", "-e", "--regexp", "-f", "--file"].includes(arg)) { need = "value"; continue; }
				if (arg === "--" || /^(?:-[nHiFvl]+|--line-number|--no-heading|--with-filename|--fixed-strings|--invert-match|--files|--files-with-matches|--hidden|--no-ignore|--color=never|--sort=(?:path|modified|accessed|created))$/.test(arg) || /^--(?:max-count|max-depth)=\d+$/.test(arg) || !arg.startsWith("-")) continue;
				return unknown("unsupported rg flag (preprocessors and arbitrary options are excluded)");
			}
			return !need ? result("read-only", "bounded ripgrep selection") : unknown("missing rg option value");
		}
		case "sort": {
			let need = false;
			for (const arg of args) {
				if (need) { need = false; continue; }
				if (["-k", "--key", "-t", "--field-separator"].includes(arg)) { need = true; continue; }
				if (arg === "--" || /^(?:-[bnrdfMhiVcsu]+|--reverse|--numeric-sort|--human-numeric-sort|--unique|--stable|--ignore-case|--check(?:=quiet|=diagnose-first)?|--key=.+|--field-separator=.)$/.test(arg) || !arg.startsWith("-")) continue;
				return unknown("sort output, temporary files, and unsupported flags are excluded");
			}
			return !need ? result("read-only", "stdout sort") : unknown("missing sort option value");
		}
		case "uniq": {
			let need = false, operands = 0, end = false;
			for (const arg of args) {
				if (need) { if (!/^\d+$/.test(arg)) return unknown("invalid uniq selection"); need = false; continue; }
				if (!end && arg === "--") { end = true; continue; }
				if (!end && ["-f", "--skip-fields", "-s", "--skip-chars", "-w", "--check-chars"].includes(arg)) { need = true; continue; }
				if (!end && /^(?:-[cdui]+|--count|--repeated|--unique|--ignore-case|--skip-fields=\d+|--skip-chars=\d+|--check-chars=\d+)$/.test(arg)) continue;
				if ((!end && arg.startsWith("-")) || unsafeReadPath(arg) || ++operands > 1) return unknown("uniq output files and unsupported flags are excluded");
			}
			return !need ? result("read-only", "stdout duplicate selection") : unknown("missing uniq option value");
		}
		case "wc": return operandsAndFlags(args, /^(?:--|-[lwcmbL]+|--(?:lines|words|bytes|chars|max-line-length))$/) ? result("read-only", "file count") : unknown("unsupported wc flag");
		case "cut": {
			let need = false, selected = false;
			for (const arg of args) {
				if (need) { need = false; selected = true; continue; }
				if (["-b", "--bytes", "-c", "--characters", "-f", "--fields", "-d", "--delimiter", "--output-delimiter"].includes(arg)) { need = true; continue; }
				if (arg === "--" || /^(?:-[sz]|--only-delimited|--zero-terminated|--complement|--(?:bytes|characters|fields)=.+|--delimiter=.|--output-delimiter=.+)$/.test(arg)) { if (/^(?:--(?:bytes|characters|fields)=)/.test(arg)) selected = true; continue; }
				if (arg.startsWith("-") || unsafeReadPath(arg)) return unknown("unsupported cut flag");
			}
			return !need && selected ? result("read-only", "column selection") : unknown("cut requires a complete selection");
		}
		case "ps": return args.length === 0 || (args.length === 1 && ["aux", "-ef", "-e"].includes(args[0])) ? result("read-only", "process listing") : unknown("unsupported ps arguments");
		case "df": return ["-h", "-hT", "-h /", "-hT /"].includes(args.join(" ")) ? result("bounded-diagnostic", "filesystem capacity query") : unknown("only df -h or df -hT, optionally for /, is classified");
		case "free": return args.length === 1 && ["-h", "-m"].includes(args[0]) ? result("bounded-diagnostic", "memory capacity query") : unknown("only free -h or free -m is classified");
		case "ip": return ["-brief address", "address show", "addr show", "route show", "route show table all", "link show"].includes(args.join(" ")) ? result("bounded-diagnostic", "network state query") : unknown("unsupported ip operation");
		case "ss": return args.length === 1 && /^-[santulp]+$/.test(args[0]) ? result("bounded-diagnostic", "socket query") : unknown("unsupported ss arguments");
		case "nstat": return args.join(" ") === "-az" ? result("bounded-diagnostic", "network counter query") : unknown("unsupported nstat arguments");
		case "rdma": return ["link show", "statistic show"].includes(args.join(" ")) ? result("bounded-diagnostic", "RDMA state query") : unknown("unsupported rdma operation");
		case "ethtool": return args.length === 2 && args[0] === "-S" && /^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,14}$/.test(args[1]) ? result("bounded-diagnostic", "interface statistics query") : unknown("unsupported ethtool operation");
		case "hccn_tool": return args.length === 4 && args[0] === "-i" && /^(?:[0-9]|[1-5][0-9]|6[0-3])$/.test(args[1]) && args[2] === "-stat" && args[3] === "-g" ? result("bounded-diagnostic", "HCCN statistics query") : unknown("only hccn_tool -i DEVICE -stat -g is classified");
		case "npu-smi": {
			if (args[0] !== "info") return unknown("only npu-smi info queries are classified");
			if (args.length === 1 || (args.length === 2 && args[1] === "-l")) return result("bounded-diagnostic", "NPU inventory query");
			const parsed = parseFlags(args.slice(1), { "-i": { value: v => /^(?:[0-9]|[1-5][0-9]|6[0-3])$/.test(v) }, "-c": { value: v => /^(?:[0-9]|[1-5][0-9]|6[0-3])$/.test(v) }, "-t": { value: v => /^(?:board|health|power|temperature|frequency|usages|memory|ecc)$/.test(v) } });
			return parsed && !parsed.operands.length ? result("bounded-diagnostic", "NPU information query") : unknown("unsupported npu-smi info flags");
		}
		case "nvidia-smi": {
			if (!args.length || (args.length === 1 && ["-L", "-q"].includes(args[0])) || args.join(" ") === "topo -m") return result("bounded-diagnostic", "GPU information query");
			const query = args.find(v => v.startsWith("--query-gpu="));
			const format = args.find(v => v.startsWith("--format="));
			if (args.length === 2 && query && format && /^--query-gpu=[a-zA-Z0-9_.]+(?:,[a-zA-Z0-9_.]+)*$/.test(query) && /^--format=csv(?:,(?:noheader|nounits)){0,2}$/.test(format)) return result("bounded-diagnostic", "bounded GPU field query");
			if (args.length === 3 && args[0] === "-q" && args[1] === "-i" && simpleName.test(args[2])) return result("bounded-diagnostic", "GPU information query");
			if (args.length === 3 && ["pmon", "dmon"].includes(args[0]) && args[1] === "-c" && /^(?:[1-9]|10)$/.test(args[2])) return result("bounded-diagnostic", "bounded GPU monitor sample");
			return unknown("nvidia-smi setting and unbounded monitor flags are excluded");
		}
		default: return undefined;
	}
}

type FlagSpec = { value?: (value: string) => boolean };
function parseFlags(args: string[], specs: Record<string, FlagSpec>): { operands: string[]; seen: Map<string, string[]> } | undefined {
	const operands: string[] = [], seen = new Map<string, string[]>();
	let end = false;
	for (let i = 0; i < args.length; i++) {
		const arg = args[i];
		if (!end && arg === "--") { end = true; continue; }
		if (end || !arg.startsWith("-") || arg === "-") { operands.push(arg); continue; }
		const eq = arg.indexOf("=");
		const key = eq > 0 ? arg.slice(0, eq) : arg;
		const spec = specs[key];
		if (!spec) return undefined;
		let value = "";
		if (spec.value) {
			value = eq > 0 ? arg.slice(eq + 1) : args[++i];
			if (value === undefined || !spec.value(value)) return undefined;
		} else if (eq > 0 && !/^(?:true|false)$/.test(arg.slice(eq + 1))) return undefined;
		seen.set(key, [...(seen.get(key) || []), value]);
	}
	return { operands, seen };
}

const nameValue = (value: string) => simpleName.test(value);
const resourceValue = (value: string) => resourceName.test(value);
const integerValue = (value: string) => /^\d+$/.test(value);
const booleanValue = (value: string) => /^(?:true|false)$/.test(value);
const boundedDuration = (value: string) => { const seconds = durationSeconds(value); return seconds !== undefined && seconds >= 0 && seconds <= 600; };
const boundedLookback = (value: string) => { const seconds = durationSeconds(value); return seconds !== undefined && seconds > 0 && seconds <= 7 * 86400; };
const selectorValue = (value: string) => value.length > 0 && value.length <= 1024 && !/[\x00\r\n]/.test(value);
const outputValue = (value: string) => /^(?:json|yaml|wide|name)$/.test(value) || /^(?:jsonpath|jsonpath-as-json)=.{1,2048}$/.test(value);
const outputFlagSpecs: Record<string, FlagSpec> = { "-o": { value: outputValue }, "--output": { value: outputValue } };

function stripKubectlGlobals(args: string[]): string[] | undefined {
	const clean: string[] = [];
	for (let i = 0; i < args.length; i++) {
		const arg = args[i], eq = arg.indexOf("="), key = eq > 0 ? arg.slice(0, eq) : arg;
		const validator = key === "-n" || key === "--namespace" || key === "--context" ? nameValue
			: key === "--kubeconfig" ? (v: string) => v.startsWith("/") && !unsafeReadPath(v) && !/[\x00\r\n]/.test(v)
			: key === "--request-timeout" ? boundedDuration
			: undefined;
		if (!validator) { clean.push(arg); continue; }
		const value = eq > 0 ? arg.slice(eq + 1) : args[++i];
		if (value === undefined || !validator(value)) return undefined;
	}
	return clean;
}

const commonKubeRead: Record<string, FlagSpec> = {
	"-A": {}, "--all-namespaces": {}, "-l": { value: selectorValue }, "--selector": { value: selectorValue },
	"--field-selector": { value: selectorValue }, "--no-headers": {}, ...outputFlagSpecs,
};

function classifyKubectl(args: string[], depth: number): HostCommandClassification {
	const rawSeparator = args.indexOf("--");
	const beforeSeparator = rawSeparator >= 0 ? args.slice(0, rawSeparator) : args;
	const stripped = stripKubectlGlobals(beforeSeparator);
	const clean = stripped && rawSeparator >= 0 ? [...stripped, "--", ...args.slice(rawSeparator + 1)] : stripped;
	if (!clean?.length) return unknown("kubectl verb is required");
	const [verb, ...rest] = clean;
	if (verb === "exec") {
		const separator = rest.indexOf("--");
		if (separator < 1 || separator === rest.length - 1) return unknown("kubectl exec requires one pod and an explicit command separator");
		const prefix = parseFlags(rest.slice(0, separator), { "-c": { value: nameValue }, "--container": { value: nameValue } });
		if (!prefix || prefix.operands.length !== 1 || !resourceValue(prefix.operands[0])) return unknown("unsupported kubectl exec target or flags");
		const remote = classifyTokens(rest.slice(separator + 1).map(value => ({ value, raw: value, quoted: false, emptyQuote: false })), depth + 1);
		return safeLevels.has(remote.level) ? result("bounded-diagnostic", `kubectl exec of ${remote.reason}`) : unknown(`kubectl exec command is not diagnostic: ${remote.reason}`);
	}
	let parsed: ReturnType<typeof parseFlags>;
	switch (verb) {
		case "get":
			parsed = parseFlags(rest, { ...commonKubeRead, "--show-labels": {}, "--sort-by": { value: selectorValue }, "--chunk-size": { value: integerValue }, "--ignore-not-found": {}, "--show-kind": {} });
			break;
		case "describe": parsed = parseFlags(rest, { "-A": {}, "--all-namespaces": {}, "-l": { value: selectorValue }, "--selector": { value: selectorValue }, "--show-events": {} }); break;
		case "logs": parsed = parseFlags(rest, { "-c": { value: nameValue }, "--container": { value: nameValue }, "--all-containers": {}, "-p": {}, "--previous": {}, "--timestamps": {}, "--prefix": {}, "--tail": { value: integerValue }, "--since": { value: boundedLookback }, "--since-time": { value: selectorValue }, "--limit-bytes": { value: integerValue }, "--max-log-requests": { value: integerValue } }); break;
		case "top": parsed = parseFlags(rest, { "-A": {}, "--all-namespaces": {}, "--containers": {}, "--sort-by": { value: v => /^(?:cpu|memory)$/.test(v) }, "--sum": {}, "--no-headers": {}, "-l": { value: selectorValue }, "--selector": { value: selectorValue } }); break;
		case "version": parsed = parseFlags(rest, { "--client": {}, ...outputFlagSpecs }); if (parsed?.operands.length) parsed = undefined; break;
		case "api-resources": parsed = parseFlags(rest, { "--api-group": { value: nameValue }, "--namespaced": { value: booleanValue }, "--verbs": { value: selectorValue }, "--no-headers": {}, "--sort-by": { value: nameValue }, "-o": { value: v => /^(?:wide|name)$/.test(v) }, "--output": { value: v => /^(?:wide|name)$/.test(v) } }); if (parsed?.operands.length) parsed = undefined; break;
		case "api-versions": parsed = parseFlags(rest, {}); if (parsed?.operands.length) parsed = undefined; break;
		case "cluster-info": parsed = parseFlags(rest, {}); if (parsed?.operands.length) parsed = undefined; break;
		case "auth": {
			if (rest[0] !== "can-i") return unknown("only kubectl auth can-i is a read query");
			parsed = parseFlags(rest.slice(1), { "-A": {}, "--all-namespaces": {}, "--list": {}, "--quiet": {}, "--subresource": { value: nameValue } });
			break;
		}
		case "rollout": {
			if (!["status", "history"].includes(rest[0])) return classifyKubectlChange(verb, rest);
			parsed = parseFlags(rest.slice(1), { "--revision": { value: integerValue }, "--timeout": { value: boundedDuration }, "--watch": {} });
			if (!parsed?.operands.length) parsed = undefined;
			break;
		}
		case "wait": {
			parsed = parseFlags(rest, { "--for": { value: selectorValue }, "--timeout": { value: boundedDuration }, "-l": { value: selectorValue }, "--selector": { value: selectorValue }, "--all": {} });
			const timeoutSeen = parsed && (parsed.seen.has("--timeout"));
			if (!parsed?.operands.length || !parsed.seen.has("--for") || !timeoutSeen) parsed = undefined;
			break;
		}
		case "apply": case "delete": case "scale": case "patch": case "cordon": case "drain": return classifyKubectlChange(verb, rest);
		default: return unknown("kubectl verb is not in the diagnostic or explicit change allowlist");
	}
	if (!parsed || parsed.operands.some(v => !resourceValue(v))) return unknown(`unsupported kubectl ${verb} operands or flags`);
	return result(verb === "wait" || verb === "logs" || verb === "rollout" ? "bounded-diagnostic" : "read-only", `kubectl ${verb} query`);
}

function classifyKubectlChange(verb: string, rest: string[]): HostCommandClassification {
	let parsed: ReturnType<typeof parseFlags>;
	switch (verb) {
		case "apply": parsed = parseFlags(rest, { "-f": { value: selectorValue }, "--filename": { value: selectorValue }, "-k": { value: selectorValue }, "--kustomize": { value: selectorValue }, "--dry-run": { value: v => /^(?:none|server|client)$/.test(v) }, "--server-side": {}, "--force-conflicts": {}, "--field-manager": { value: nameValue }, "--validate": { value: selectorValue }, "--prune": {}, "-l": { value: selectorValue }, "--selector": { value: selectorValue }, "--wait": {}, "--timeout": { value: boundedDuration } }); break;
		case "delete": parsed = parseFlags(rest, { "-f": { value: selectorValue }, "--filename": { value: selectorValue }, "-k": { value: selectorValue }, "--kustomize": { value: selectorValue }, "--force": {}, "--grace-period": { value: integerValue }, "--now": {}, "--wait": {}, "--timeout": { value: boundedDuration }, "--ignore-not-found": {}, "--cascade": { value: selectorValue }, "-l": { value: selectorValue }, "--selector": { value: selectorValue }, "--all": {} }); break;
		case "scale": parsed = parseFlags(rest, { "--replicas": { value: integerValue }, "--current-replicas": { value: integerValue }, "--resource-version": { value: integerValue }, "--timeout": { value: boundedDuration } }); if (!parsed?.seen.has("--replicas")) parsed = undefined; break;
		case "patch": parsed = parseFlags(rest, { "-p": { value: selectorValue }, "--patch": { value: selectorValue }, "--patch-file": { value: selectorValue }, "--type": { value: v => /^(?:json|merge|strategic)$/.test(v) }, "--subresource": { value: nameValue }, "--dry-run": { value: v => /^(?:none|server|client)$/.test(v) }, "--field-manager": { value: nameValue } }); if (parsed && !parsed.seen.has("-p") && !parsed.seen.has("--patch") && !parsed.seen.has("--patch-file")) parsed = undefined; break;
		case "rollout": {
			if (!["restart", "undo"].includes(rest[0])) return unknown("unsupported kubectl rollout operation");
			parsed = parseFlags(rest.slice(1), { "--to-revision": { value: integerValue }, "--dry-run": { value: v => /^(?:none|server|client)$/.test(v) } });
			break;
		}
		case "cordon": parsed = parseFlags(rest, {}); break;
		case "drain": parsed = parseFlags(rest, { "--ignore-daemonsets": {}, "--delete-emptydir-data": {}, "--disable-eviction": {}, "--force": {}, "--grace-period": { value: integerValue }, "--pod-selector": { value: selectorValue }, "--skip-wait-for-delete-timeout": { value: integerValue }, "--timeout": { value: boundedDuration } }); break;
		default: return unknown("unsupported kubectl change");
	}
	if (!parsed || (!parsed.operands.length && !parsed.seen.has("-f") && !parsed.seen.has("--filename") && !parsed.seen.has("-k") && !parsed.seen.has("--kustomize")) || parsed.operands.some(v => !resourceValue(v))) return unknown(`malformed kubectl ${verb} change`);
	return result("cluster-change", `kubectl ${verb} changes cluster state`);
}

function stripHelmGlobals(args: string[]): string[] | undefined {
	const clean: string[] = [];
	for (let i = 0; i < args.length; i++) {
		const arg = args[i], eq = arg.indexOf("="), key = eq > 0 ? arg.slice(0, eq) : arg;
		const validator = key === "-n" || key === "--namespace" || key === "--kube-context" ? nameValue
			: key === "--kubeconfig" ? (v: string) => v.startsWith("/") && !unsafeReadPath(v) : undefined;
		if (!validator) { clean.push(arg); continue; }
		const value = eq > 0 ? arg.slice(eq + 1) : args[++i];
		if (value === undefined || !validator(value)) return undefined;
	}
	return clean;
}

function classifyHelm(args: string[]): HostCommandClassification {
	const clean = stripHelmGlobals(args);
	if (!clean?.length) return unknown("helm command is required");
	const [verb, ...rest] = clean;
	let parsed: ReturnType<typeof parseFlags>;
	const helmOutput = { "-o": { value: (v: string) => /^(?:table|json|yaml)$/.test(v) }, "--output": { value: (v: string) => /^(?:table|json|yaml)$/.test(v) } };
	if (verb === "list") {
		parsed = parseFlags(rest, { "-A": {}, "--all-namespaces": {}, "-a": {}, "--all": {}, "--deployed": {}, "--failed": {}, "--pending": {}, "--uninstalled": {}, "--superseded": {}, "-q": {}, "--short": {}, "--no-headers": {}, "--date": {}, "--reverse": {}, "--filter": { value: selectorValue }, "--max": { value: integerValue }, "--offset": { value: integerValue }, ...helmOutput });
		if (parsed?.operands.length) parsed = undefined;
	} else if (verb === "status") {
		parsed = parseFlags(rest, { "--show-desc": {}, "--show-resources": {}, "--show-tests": {}, "--revision": { value: integerValue }, ...helmOutput });
		if (parsed?.operands.length !== 1) parsed = undefined;
	} else if (verb === "history") {
		parsed = parseFlags(rest, { "--max": { value: integerValue }, ...helmOutput });
		if (parsed?.operands.length !== 1) parsed = undefined;
	} else if (verb === "get" && ["values", "manifest"].includes(rest[0])) {
		parsed = parseFlags(rest.slice(1), { "-a": {}, "--all": {}, "--revision": { value: integerValue }, ...helmOutput });
		if (parsed?.operands.length !== 1 || (rest[0] === "manifest" && (parsed?.seen.has("-a") || parsed?.seen.has("--all")))) parsed = undefined;
	} else return unknown("helm command is not an allowlisted release read");
	if (!parsed || parsed.operands.some(v => !nameValue(v))) return unknown(`unsupported helm ${verb} operands or flags`);
	return result("read-only", `helm ${verb} release query`);
}

function classifySSH(args: string[], depth: number): HostCommandClassification {
	let i = 0;
	while (i < args.length) {
		if (args[i] === "--") { i++; break; }
		if (args[i] === "-o") {
			const option = args[i + 1];
			if (!/^(?:BatchMode=yes|StrictHostKeyChecking=yes|ConnectTimeout=(?:[1-9]|[1-5]\d|60))$/.test(option || "")) return unknown("SSH option can alter routing, credentials, forwarding, or execution");
			i += 2; continue;
		}
		if (/^-o(?:BatchMode=yes|StrictHostKeyChecking=yes|ConnectTimeout=(?:[1-9]|[1-5]\d|60))$/.test(args[i])) { i++; continue; }
		break;
	}
	const host = args[i++];
	if (!host || !/^[a-zA-Z0-9][a-zA-Z0-9._@:-]{0,252}$/.test(host) || i >= args.length) return unknown("SSH requires a plain host and remote command");
	// OpenSSH joins remaining argv with spaces and passes it to a remote shell.
	// Reparse exactly that string: local quotes are already gone, while nested
	// quotes intentionally carried inside a generated command remain present.
	const remoteText = args.slice(i).join(" ");
	const remote = classifyCommand(remoteText, depth + 1);
	return safeLevels.has(remote.level) ? result("bounded-diagnostic", `SSH ${remote.reason}`) : unknown(`SSH remote command is not diagnostic: ${remote.reason}`);
}

function classifyTokens(tokens: Token[], depth: number): HostCommandClassification {
	if (depth > 3 || !tokens.length) return unknown("nested command limit exceeded");
	const name = executableName(tokens[0]);
	if (!name) return unknown("executable must be a plain name or /bin or /usr/bin path");
	const args = tokens.slice(1).map(token => token.value);
	const local = classifyLocal(name, args);
	if (local) return local;
	if (name === "kubectl") return classifyKubectl(args, depth);
	if (name === "helm") return classifyHelm(args);
	if (name === "ssh") return classifySSH(args, depth);
	return unknown("executable is not in the bounded command policy");
}

function classifyCommand(command: string, depth: number): HostCommandClassification {
	const parsed = parseShell(command);
	if ("error" in parsed) return unknown(parsed.error);
	if (depth > 0 && parsed.pipelines.length > 1) return unknown("nested remote pipelines are not classified");
	const classifications = parsed.pipelines.map(tokens => classifyTokens(tokens, depth));
	if (parsed.pipelines.length > 1) {
		if (classifications.some(item => !safeLevels.has(item.level))) return unknown("pipelines require every stage to be an allowlisted read or bounded diagnostic");
		return result(classifications.some(item => item.level === "bounded-diagnostic") ? "bounded-diagnostic" : "read-only", "pipeline of allowlisted read commands");
	}
	return classifications[0];
}

export function classifyHostCommand(command: string): HostCommandClassification {
	return classifyCommand(command, 0);
}

/** Compatibility predicate for the existing full-mode approval gate. */
export function readOnlyHostCommand(command: string): boolean {
	return safeLevels.has(classifyHostCommand(command).level);
}
