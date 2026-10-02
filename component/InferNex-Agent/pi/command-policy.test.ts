import assert from "node:assert/strict";
import test from "node:test";
import { classifyHostCommand, readOnlyHostCommand } from "./command-policy.ts";

const level = (command: string) => classifyHostCommand(command).level;

test("preserves the established local read and bounded diagnostic contract", () => {
	for (const command of [
		"id -u", "sleep 10", "sleep 0.5", "ls -lah /var/log", "cat /var/log/app.log",
		"grep -n timeout /var/log/app.log", "ethtool -S eth0", "ip route show", "ps aux",
		"rg -n timeout /var/log", "sort -n /tmp/counts", "uniq -c /tmp/names", "wc -l /tmp/log", "cut -f 2 /tmp/table",
	]) assert.equal(readOnlyHostCommand(command), true, `${command}: ${JSON.stringify(classifyHostCommand(command))}`);
	for (const command of [
		"reboot", "sleep 3600", "sleep 10m", "hostname changed", "date -s now", "ip route flush table all",
		"cat /dev/sda", "cat /dev", "python3 -c print(1)", "find / -exec reboot", "sudo cat /log", "c''at /log",
		"sort -o /tmp/output /tmp/input", "uniq /tmp/input /tmp/output", "uniq -- -c /tmp/output", "rg --pre cat token",
	]) assert.equal(level(command), "unknown", command);
	assert.equal(level("sleep 60"), "bounded-diagnostic");
	assert.equal(level("cat /tmp/a | grep -n x | head -n 3"), "read-only");
});

test("shell grammar accepts quoted data but rejects execution syntax and unsafe pipelines", () => {
	for (const command of [
		"cat /log; reboot", "cat /log && id", "cat $(reboot)", "cat `reboot`", "cat /log > /etc/config",
		"cat /log | sh", "kubectl delete pod p | cat", "cat '/log\nreboot'", "cat \"$HOME/log\"",
		"cat /tmp/*", "cat /tmp/a\\\nreboot",
	]) assert.equal(level(command), "unknown", command);
	assert.equal(level("cat '/tmp/a;still-a-name'"), "read-only");
	assert.equal(level("printf x | cat"), "unknown");
});

test("kubectl read verbs accept finite globals and output formats", () => {
	for (const command of [
		"kubectl --kubeconfig /etc/kubernetes/admin.conf --context prod -n models get pods -o yaml",
		"kubectl get pods --namespace=models -o=json",
		"kubectl -n=models get pods -o=jsonpath='{.items[0].metadata.name}'",
		"kubectl describe pod/p -n models",
		"kubectl logs pod/p -n models -c main --tail 100 --timestamps", "kubectl logs pod/p --since=1h",
		"kubectl top pods -A --sort-by cpu",
		"kubectl version --client -o yaml", "kubectl api-resources -o name", "kubectl api-versions",
		"kubectl cluster-info", "kubectl auth can-i get pods -n models",
		"kubectl rollout status deployment/api -n models --timeout=60s",
		"kubectl rollout history deployment/api --revision 2",
		"kubectl wait pod/p --for condition=Ready --timeout 90s -n models",
	]) assert.equal(readOnlyHostCommand(command), true, `${command}: ${JSON.stringify(classifyHostCommand(command))}`);
	assert.equal(level("kubectl get pods -o json"), "read-only");
	assert.equal(level("kubectl logs pod/p --tail=10"), "bounded-diagnostic");
	assert.equal(level("kubectl wait pod/p --for=condition=Ready --timeout=10m"), "bounded-diagnostic");
});

test("kubectl rejects raw endpoints, credentials, cache/output files and unbounded or unknown flags", () => {
	for (const command of [
		"kubectl get --raw /api/proxy/unsafe", "kubectl get pods --server https://other",
		"kubectl get pods --token secret", "kubectl get pods --username admin", "kubectl get pods --cache-dir /tmp/cache",
		"kubectl get pods --output-file /tmp/out", "kubectl get pods -o template --template-file /tmp/t",
		"kubectl cluster-info dump", "kubectl proxy", "kubectl plugin list", "kubectl wait pod/p --for condition=Ready",
		"kubectl wait pod/p --for condition=Ready --timeout=11m", "kubectl logs pod/p --follow",
		"kubectl auth can-i get pods --as system:admin", "kubectl get pods --kubeconfig /dev/stdin",
	]) assert.equal(level(command), "unknown", command);
});

test("kubectl exec preserves argv boundaries and recursively limits the executable", () => {
	for (const command of [
		"kubectl -n models exec pod-a -c main -- cat /var/log/plog.log",
		"kubectl exec pod-a -n=models -c=main -- head -n 10 /var/log/plog.log",
		"kubectl exec pod-a -- ps aux", "kubectl exec pod-a -- id -u",
		"kubectl exec pod-a -- df -h", "kubectl exec pod-a -- df -hT /", "kubectl exec pod-a -- free -h",
		"kubectl exec pod-a -- ip route show", "kubectl exec pod-a -- ss -s",
		"kubectl exec pod-a -- npu-smi info", "kubectl exec pod-a -- nvidia-smi -L",
		"kubectl exec pod-a -- hccn_tool -i 0 -stat -g",
		"kubectl exec pod-a -- cat '|'", "kubectl exec pod-a -- cat -- /var/log/app.log",
	]) assert.equal(level(command), "bounded-diagnostic", `${command}: ${JSON.stringify(classifyHostCommand(command))}`);
	for (const command of [
		"kubectl exec pod-a -- sh -c id", "kubectl exec pod-a -- bash -lc 'cat /log'",
		"kubectl exec pod-a -it -- cat /log", "kubectl exec pod-a -- cat /dev/mem",
		"kubectl exec pod-a -- nvidia-smi -pm 1", "kubectl exec pod-a -- hccn_tool -i 0 -netdetect -g",
	]) assert.equal(level(command), "unknown", command);
});

test("well-formed kubectl mutations are explicit cluster changes", () => {
	for (const command of [
		"kubectl apply -f /tmp/deploy.yaml -n models", "kubectl delete pod p -n models",
		"kubectl scale deployment/api --replicas=3", "kubectl patch deployment/api -p '{\"spec\":{\"replicas\":2}}'",
		"kubectl rollout restart deployment/api", "kubectl rollout undo deployment/api --to-revision 2",
		"kubectl cordon node-a", "kubectl drain node-a --ignore-daemonsets --timeout 5m",
	]) assert.equal(level(command), "cluster-change", `${command}: ${JSON.stringify(classifyHostCommand(command))}`);
	for (const command of ["kubectl scale deployment/api", "kubectl patch deployment/api", "kubectl rollout restart", "kubectl drain node-a --delete-everything"]) assert.equal(level(command), "unknown", command);
});

test("SSH reparses the remote shell string and excludes forwarding/config wrappers", () => {
	const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;
	const generated = `ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -- ${quote("node-a")} ${quote("'ip' 'route' 'show'")}`;
	for (const command of [
		"ssh root@node-a 'ip route show'", "ssh -- node-a 'cat /var/log/app.log'",
		"ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -- node-a 'kubectl get pods -o yaml'",
		generated,
	]) assert.equal(level(command), "bounded-diagnostic", `${command}: ${JSON.stringify(classifyHostCommand(command))}`);
	for (const command of [
		"ssh -oProxyCommand=reboot node-a cat /log", "ssh -L 8080:localhost:80 node-a cat /log",
		"ssh -R 8080:localhost:80 node-a cat /log", "ssh -D 1080 node-a cat /log", "ssh -F /tmp/config node-a cat /log",
		"ssh node-a 'ip link set eth0 down'", "ssh node-a 'cat /log; reboot'", "ssh node-a cat '/log; reboot'",
		"ssh node-a 'cat /log | head'",
	]) assert.equal(level(command), "unknown", command);
});

test("helm permits only release reads with finite flags", () => {
	for (const command of [
		"helm list -A -o json", "helm status api -n models --show-resources", "helm history api --max 5",
		"helm get values api -n models -o yaml", "helm get manifest api --revision 2",
		"helm --kubeconfig=/etc/kubernetes/admin.conf get values api",
	]) assert.equal(level(command), "read-only", `${command}: ${JSON.stringify(classifyHostCommand(command))}`);
	for (const command of [
		"helm get all api", "helm install api chart", "helm plugin list", "helm list --registry-config /tmp/auth",
		"helm get values api --post-renderer /tmp/script", "helm status api --kube-apiserver https://other",
	]) assert.equal(level(command), "unknown", command);
});
