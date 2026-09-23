import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync } from "node:child_process";
import { mkdtemp, chmod, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { executionSpec, runHostCommand, networkCommand, pfcCommand, hcclCommand, onSSH, quote, registerHostTools } from "./host-tools.ts";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

test("network probes reject injected destinations and explicitly bound traffic", () => {
 assert.throws(() => networkCommand({ probe: "ping", target: "host; id" }), /hostname/);
 assert.throws(() => networkCommand({ probe: "tcp-connect", target: "node-1", port: 0 }), /port/);
 assert.throws(() => networkCommand({ probe: "ethtool-stats", iface: "-f" }), /interface/);
 assert.throws(() => onSSH("uname", "-oProxyCommand=id"), /SSH/);
 assert.match(networkCommand({ probe: "iperf-client", target: "node-1" }), /'10'/);
 assert.match(networkCommand({ probe: "rdma-counters", sshTarget: "root@node-1" }), /StrictHostKeyChecking=yes/);
 assert.equal(execFileSync("/bin/sh", ["-c", `printf '%s' ${quote("a'b;$(id)")}`], { encoding: "utf8" }), "a'b;$(id)");
});

test("PFC samples retain raw counters and timestamps; no write flags", () => {
 const command = pfcCommand({ deviceIds: [0, 1, 1], samples: 3, intervalSeconds: 2 });
 assert.match(command, /for device in 0 1;/);
 assert.match(command, /-stat -g/);
 assert.match(command, /date -u/);
 assert.throws(() => pfcCommand({ deviceIds: [64] }), /deviceId/);
 assert.throws(() => pfcCommand({ deviceIds: [0], samples: 0 }), /samples/);
});

test("HCCL benchmark validates topology, bytes and root launcher", () => {
 const base = { executable: "/opt/hccl/bin/all_reduce_test", ranks: 8, devicesPerNode: 8 };
 assert.match(hcclCommand(base, "root"), /--allow-run-as-root/);
 assert.doesNotMatch(hcclCommand(base, "normal"), /--allow-run-as-root/);
 assert.throws(() => hcclCommand({ ...base, ranks: 16 }, "root"), /hostfile/);
 assert.match(hcclCommand({ ...base, ranks: 16, hostfile: "/etc/hccl/hosts", setupScript: "/opt/cann/set_env.sh" }, "normal"), /source/);
 assert.throws(() => hcclCommand({ ...base, minBytes: 1000, maxBytes: 500 }, "normal"), /maxBytes/);
 assert.throws(() => hcclCommand({ ...base, executable: "/bin/sh" }, "root"), /supported HCCL/);
});

test("mode command does not change identity when verification fails; builtins cannot bypass", async () => {
 const commands = new Map<string, any>(); const tools: any[] = []; const events = new Map<string, any>(); const notices: string[] = [];
 const state = registerHostTools({ registerCommand: (name: string, cmd: any) => commands.set(name, cmd), registerTool: (tool: any) => tools.push(tool), on: (name: string, cb: any) => events.set(name, cb) } as unknown as ExtensionAPI);
 const ctx = { hasUI: true, ui: { notify: (s: string) => notices.push(s), setStatus: () => {}, confirm: async () => false } };
 assert.equal(state.mode(), "normal");
 await commands.get("mode_change").handler("invalid", ctx);
 assert.equal(state.mode(), "normal");
 for (const toolName of ["bash", "read", "write", "edit", "grep", "find", "ls"]) assert.equal((await events.get("tool_call")({ toolName })).block, true);
 await assert.rejects(tools[0].execute("1", { command: "id" }, undefined, undefined, ctx), /denied/);
 if (process.getuid?.() !== 0) {
  await commands.get("mode_change").handler("root", ctx);
  assert.equal(state.mode(), "normal");
  assert.match(notices.at(-1)!, /unchanged/);
 }
});

// This test is also run under sudo in Linux CI, using a dedicated unprivileged
// account with an existing home. It checks OS enforcement, not a displayed flag.
const privileged = process.platform === "linux" && process.getuid?.() === 0 && !!process.env.INFERNEX_TEST_HOST_USER;
test("real UID drop, root file denial, environment isolation, timeout and cancellation", { skip: !privileged }, async () => {
 const normalUser = process.env.INFERNEX_TEST_HOST_USER!;
 const dir = await mkdtemp(join(tmpdir(), "infernex-root-contract-"));
 await chmod(dir, 0o700); await writeFile(join(dir, "secret"), "root-only", { mode: 0o600 });
 process.env.INFERNEX_PI_API_KEY = "must-not-reach-shell";
 process.env.BASH_ENV = join(dir, "malicious-env");
 try {
  const normal = await runHostCommand("normal", "id -u; id -G; grep NoNewPrivs /proc/self/status; printf '%s' \"${INFERNEX_PI_API_KEY-unset}\"", 5, undefined, normalUser, dir);
  assert.equal(normal.exitCode, 0); assert.notEqual(normal.uid, 0);
  assert.equal(Number(normal.stdout.split('\n')[0]), normal.uid);
  assert.match(normal.stdout, /NoNewPrivs:\s+1/); assert.match(normal.stdout, /unset/);
  const denied = await runHostCommand("normal", `cat ${quote(join(dir, "secret"))}`, 5, undefined, normalUser, dir);
  assert.notEqual(denied.exitCode, 0); assert.doesNotMatch(denied.stdout, /root-only/);
  const root = await runHostCommand("root", `id -u; cat ${quote(join(dir, "secret"))}`, 5, undefined, normalUser, dir);
  assert.equal(root.uid, 0); assert.equal(root.stdout, "0\nroot-only");
  const timeout = await runHostCommand("normal", "sleep 30 & wait", 1, undefined, normalUser, dir);
  assert.equal(timeout.timedOut, true); assert.equal(timeout.exitCode, null);
  const controller = new AbortController();
  const pending = runHostCommand("normal", "sleep 30 & wait", 30, controller.signal, normalUser, dir);
  setTimeout(() => controller.abort(), 100);
  assert.equal((await pending).cancelled, true);
  const commands = new Map<string, any>(), notices: string[] = [];
  process.env.INFERNEX_HOST_USER = normalUser; process.env.INFERNEX_WORKSPACE_ROOT = dir;
  const state = registerHostTools({ registerCommand: (n: string, c: any) => commands.set(n, c), registerTool: () => {}, on: () => {} } as unknown as ExtensionAPI);
  const ctx = { hasUI: true, ui: { notify: (s: string) => notices.push(s), setStatus: () => {} } };
  await commands.get("mode_change").handler("root", ctx); assert.equal(state.mode(), "root", notices.join('\n'));
  await commands.get("mode_change").handler("normal", ctx); assert.equal(state.mode(), "normal", notices.join('\n'));
 } finally { delete process.env.BASH_ENV; delete process.env.INFERNEX_PI_API_KEY; await rm(dir, { recursive: true, force: true }); }
});

test("PFC executes each requested device and propagates a failing sample", async () => {
 const dir = await mkdtemp(join(tmpdir(), "infernex-pfc-fixture-"));
 try {
  await writeFile(join(dir, "hccn_tool"), '#!/bin/sh\necho "device=$2 pfc_rx_pause=12"\n[ "$2" != 1 ]\n', { mode: 0o755 });
  const command = pfcCommand({ deviceIds: [0, 1], samples: 2, intervalSeconds: 1 });
  try { execFileSync('/bin/sh', ['-c', command], { env: { PATH: dir + ':/usr/bin:/bin' }, encoding: 'utf8', timeout: 10000 }); assert.fail('failure was hidden'); }
  catch (error: any) {
   assert.equal(error.status, 1); assert.equal(error.stdout.match(/pfc_rx_pause=12/g)?.length, 4);
   assert.equal(error.stdout.match(/\d{4}-\d{2}-\d{2}T/g)?.length, 2);
  }
 } finally { await rm(dir, { recursive: true, force: true }); }
});

test("HCCL invokes MPI with the requested ranks after sourcing CANN setup", async () => {
 const dir = await mkdtemp(join(tmpdir(), "infernex-hccl-fixture-"));
 try {
  await writeFile(join(dir, 'mpirun'), '#!/bin/sh\nprintf "CANN=%s\\n" "$CANN_TEST_SETUP"\nprintf "arg=%s\\n" "$@"\n', { mode: 0o755 });
  await writeFile(join(dir, 'setup.sh'), 'export CANN_TEST_SETUP=loaded\n');
  const command = hcclCommand({ executable: '/opt/hccl/bin/all_reduce_test', ranks: 16, devicesPerNode: 8, hostfile: '/etc/hccl/hosts', setupScript: join(dir, 'setup.sh') }, 'normal');
  const output = execFileSync('/bin/bash', ['-c', command], { env: { PATH: dir + ':/usr/bin:/bin' }, encoding: 'utf8', timeout: 10000 });
  assert.match(output, /CANN=loaded/); assert.match(output, /arg=-n\narg=16/); assert.match(output, /arg=-p\narg=8/);
  assert.match(output, /arg=-f\narg=\/etc\/hccl\/hosts/);
 } finally { await rm(dir, { recursive: true, force: true }); }
});

test("full access classifies effects rather than trusting risk claims or shell text", async () => {
 const { readOnlyHostCommand, needsHostApproval, needsMCPApproval } = await import('./host-tools.ts');
 for (const command of ['id -u', 'sleep 10', 'sleep 0.5', 'ls -lah /var/log', 'cat /var/log/app.log', 'grep -n timeout /var/log/app.log', 'kubectl -n models logs pod-a --tail=100', 'kubectl -n models exec pod-a -c main -- cat /var/log/plog.log', "ssh root@node-a 'ip route show'", 'ethtool -S eth0']) assert.equal(readOnlyHostCommand(command),true,command);
 for (const command of ['reboot', 'sleep 3600', 'sleep 10m', 'hostname changed', 'date -s now', 'ip route flush table all', 'kubectl delete pod foo', 'kubectl get --raw /api/proxy/unsafe', 'kubectl -n models exec p -- sh -c id', 'ssh -oProxyCommand=reboot host cat /log', "ssh host 'ip link set eth0 down'", 'cat /log; reboot', 'cat /log | sh', 'cat $(reboot)', 'cat /log > /etc/config', "cat '/log\nreboot'", 'python3 -c print(1)', 'find / -exec reboot', 'sudo cat /log', 'cat /dev/sda', "c''at /log"]) assert.equal(readOnlyHostCommand(command),false,command);
 assert.equal(needsHostApproval('full','infernex_sample_pfc',{}),false);
 assert.equal(needsHostApproval('full','infernex_network_probe',{probe:'iperf-client'}),true);
 assert.equal(needsHostApproval('full','infernex_run_hccl_test',{}),true);
 assert.equal(needsHostApproval('full','infernex_network_probe',{probe:'unknown'}),true);
 assert.equal(needsHostApproval('manual','infernex_host_exec',{command:'id'}),true);
 for (const name of ['infernex_create_markdown_report','infernex_remember','infernex_forget_memory','infernex_start_plog_capture','infernex_start_collector_run']) assert.equal(needsMCPApproval('full',name,false),false);
 for (const name of ['infernex_deploy_model','infernex_delete_model','infernex_start_experiment','unknown_write']) assert.equal(needsMCPApproval('full',name,false),true);
 assert.equal(needsMCPApproval('full','infernex_delete_model',true),true);
});

test("full access executes safe commands without dialogs but preserves gates and busy identity", async () => {
 const commands = new Map<string,any>(), tools = new Map<string,any>(), events = new Map<string,any>();
 let executions = 0, confirmations = 0; const notices: string[] = [];
 const execute = async (mode: any, command: string) => { executions++; return {mode,uid:mode==='root'?0:1000,cwd:'/tmp',command,exitCode:0,stdout:mode==='root'?'0\n':'1000\n',stderr:'',truncated:false,timedOut:false,cancelled:false}; };
 const state = registerHostTools({registerCommand:(n:string,c:any)=>commands.set(n,c),registerTool:(t:any)=>tools.set(t.name,t),on:(n:string,f:any)=>events.set(n,f)} as unknown as ExtensionAPI, undefined, undefined, execute);
 const ctx = {hasUI:true,isIdle:()=>true,ui:{notify:(s:string)=>notices.push(s),setStatus:()=>{},confirm:async()=>{confirmations++;return false;}}};
 await commands.get('mode_change').handler('full',ctx);
 assert.equal(state.access(),'full'); assert.equal(state.mode(),'normal'); assert.equal(executions,0);
 await tools.get('infernex_host_exec').execute('1',{command:'id -u'},undefined,undefined,ctx);
 await tools.get('infernex_sample_pfc').execute('2',{deviceIds:[0]},undefined,undefined,ctx);
 assert.equal(executions,2); assert.equal(confirmations,0);
 for (const [name,params] of [['infernex_host_exec',{command:'kubectl delete pod p'}],['infernex_network_probe',{probe:'iperf-client',target:'node-a'}],['infernex_run_hccl_test',{executable:'/opt/all_reduce_test',ranks:1,devicesPerNode:1}]] as const) await assert.rejects(tools.get(name).execute('3',params,undefined,undefined,ctx),/denied/);
 assert.equal(executions,2); assert.equal(confirmations,3); assert.equal(state.wasDenied(),true);
 await state.operation(async()=>{ await commands.get('mode_change').handler('root',ctx); assert.equal(state.mode(),'normal'); });
 await commands.get('mode_change').handler('root full',ctx); assert.equal(state.mode(),'root'); assert.equal(state.access(),'full');
 await commands.get('mode_change').handler('normal manual',ctx); assert.equal(state.mode(),'normal'); assert.equal(state.access(),'manual');
 await commands.get('mode_change').handler('full',{...ctx,hasUI:false}); assert.equal(state.access(),'manual');
 await commands.get('mode_change').handler('full',ctx); events.get('session_start')({},ctx); assert.equal(state.access(),'manual'); assert.equal(state.mode(),'normal');
});
