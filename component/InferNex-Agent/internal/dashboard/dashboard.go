/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

package dashboard

import (
	"encoding/json"
	"net/http"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

type SnapshotReader interface {
	Load() supervisor.Snapshot
}

func New(reader SnapshotReader) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = response.Write([]byte(indexHTML))
	})
	mux.HandleFunc("/api/v1/snapshot", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		encoder := json.NewEncoder(response)
		encoder.SetEscapeHTML(true)
		if err := encoder.Encode(reader.Load()); err != nil {
			http.Error(response, "encode snapshot", http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !reader.Load().Ready {
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = response.Write([]byte("waiting for first scan\n"))
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	})
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(
			"Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self' 'unsafe-inline'; connect-src 'self'; "+
				"img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'",
		)
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>InferNex Agent Dashboard</title>
  <style>
    :root {
      color-scheme: dark;
      --bg: #08111d;
      --surface: #0f1c2b;
      --surface-2: #142438;
      --line: #263b52;
      --text: #e8f0f8;
      --muted: #91a4b9;
      --accent: #35d0ba;
      --good: #55d187;
      --warning: #ffbe55;
      --critical: #ff6b78;
      --info: #6bb8ff;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      background:
        radial-gradient(circle at 80% -10%, rgba(53, 208, 186, .15), transparent 35rem),
        linear-gradient(180deg, #09131f, var(--bg));
      color: var(--text);
      font: 14px/1.55 Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    main { width: min(1440px, calc(100% - 40px)); margin: 0 auto; padding: 36px 0 64px; }
    header { display: flex; justify-content: space-between; gap: 24px; align-items: flex-start; margin-bottom: 26px; }
    h1 { margin: 0; font-size: clamp(26px, 4vw, 42px); letter-spacing: -.035em; }
    h1 span { color: var(--accent); }
    .subtitle, .meta { color: var(--muted); }
    .subtitle { margin: 6px 0 0; font-size: 15px; }
    .connection { display: flex; align-items: center; gap: 9px; padding-top: 10px; white-space: nowrap; }
    .dot { width: 9px; height: 9px; border-radius: 50%; background: var(--warning); box-shadow: 0 0 14px currentColor; }
    .dot.ok { background: var(--good); }
    .dot.error { background: var(--critical); }
    .metrics { display: grid; grid-template-columns: repeat(6, minmax(120px, 1fr)); gap: 12px; margin-bottom: 24px; }
    .metric, .namespace, .service, .empty {
      background: linear-gradient(145deg, rgba(20, 36, 56, .96), rgba(13, 26, 41, .96));
      border: 1px solid var(--line);
      border-radius: 14px;
      box-shadow: 0 16px 44px rgba(0, 0, 0, .16);
    }
    .metric { padding: 16px 18px; }
    .metric .value { display: block; font-size: 27px; font-weight: 750; letter-spacing: -.04em; }
    .metric .label { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .08em; }
    .metric.critical .value { color: var(--critical); }
    .metric.warning .value { color: var(--warning); }
    .metric.good .value { color: var(--good); }
    .namespace { padding: 18px; margin-top: 16px; }
    .namespace-head, .service-head { display: flex; justify-content: space-between; gap: 16px; align-items: center; }
    .namespace h2 { margin: 0; font-size: 19px; }
    .services { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 480px), 1fr)); gap: 14px; margin-top: 15px; }
    .service { padding: 17px; background: rgba(8, 18, 30, .55); }
    .service h3 { margin: 0; font-size: 17px; overflow-wrap: anywhere; }
    .badges { display: flex; flex-wrap: wrap; gap: 7px; margin: 10px 0 13px; }
    .badge { border: 1px solid var(--line); border-radius: 999px; padding: 3px 9px; color: var(--muted); font-size: 12px; }
    .badge.good { color: var(--good); border-color: rgba(85, 209, 135, .4); background: rgba(85, 209, 135, .08); }
    .badge.critical { color: var(--critical); border-color: rgba(255, 107, 120, .4); background: rgba(255, 107, 120, .08); }
    .issue { display: grid; grid-template-columns: 8px 1fr; gap: 10px; padding: 9px 0; border-top: 1px solid rgba(38, 59, 82, .65); }
    .issue-dot { width: 7px; height: 7px; border-radius: 50%; margin-top: 7px; background: var(--info); }
    .issue-dot.warning { background: var(--warning); }
    .issue-dot.critical { background: var(--critical); }
    .issue-code { color: var(--muted); font: 11px/1.2 ui-monospace, SFMono-Regular, Consolas, monospace; }
    .analysis { margin-top: 13px; padding: 13px; border: 1px solid rgba(53, 208, 186, .27); border-radius: 10px; background: rgba(53, 208, 186, .055); }
    .analysis-title { color: var(--accent); font-weight: 700; margin-bottom: 5px; }
    .analysis-body { white-space: pre-wrap; overflow-wrap: anywhere; }
    .error { color: var(--critical); }
    .empty { padding: 38px; text-align: center; color: var(--muted); }
    footer { margin-top: 24px; color: var(--muted); font-size: 12px; text-align: right; }
    @media (max-width: 900px) {
      .metrics { grid-template-columns: repeat(3, 1fr); }
      header { flex-direction: column; }
    }
    @media (max-width: 560px) {
      main { width: min(100% - 24px, 1440px); padding-top: 24px; }
      .metrics { grid-template-columns: repeat(2, 1fr); }
      .namespace-head, .service-head { align-items: flex-start; flex-direction: column; }
    }
  </style>
</head>
<body>
<main>
  <header>
    <div>
      <h1>InferNex <span>Agent</span></h1>
      <p class="subtitle">PD 推理服务持续巡检、证据汇总与分析建议</p>
    </div>
    <div class="connection"><span id="dot" class="dot"></span><span id="connection">正在连接</span></div>
  </header>
  <section id="metrics" class="metrics"></section>
  <section id="content"><div class="empty">等待首次巡检结果…</div></section>
  <footer id="footer"></footer>
</main>
<script>
  const byId = id => document.getElementById(id);
  const el = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  };
  const metric = (value, label, tone) => {
    const card = el("div", "metric " + (tone || ""));
    card.append(el("span", "value", String(value || 0)), el("span", "label", label));
    return card;
  };
  const badge = (text, tone) => el("span", "badge " + (tone || ""), text);
  const fmtTime = value => value ? new Date(value).toLocaleString() : "尚未完成";

  function render(data) {
    const summary = data.summary || {};
    const metrics = byId("metrics");
    metrics.replaceChildren(
      metric(summary.services, "服务"),
      metric(summary.readyServices, "健康", "good"),
      metric(summary.degradedServices, "异常", summary.degradedServices ? "critical" : ""),
      metric(summary.issues, "问题"),
      metric(summary.criticalIssues, "严重", summary.criticalIssues ? "critical" : ""),
      metric(summary.warningIssues, "告警", summary.warningIssues ? "warning" : "")
    );

    const content = byId("content");
    content.replaceChildren();
    const namespaces = data.namespaces || [];
    if (!data.ready || namespaces.length === 0) {
      content.append(el("div", "empty", "等待首次巡检结果…"));
    }
    for (const ns of namespaces) {
      const section = el("section", "namespace");
      const head = el("div", "namespace-head");
      head.append(el("h2", "", ns.name), el("div", "meta", (ns.total || 0) + " 个服务 · " + ns.scanMillis + " ms"));
      section.append(head);
      if (ns.error) section.append(el("p", "error", ns.error));
      const services = el("div", "services");
      for (const item of (ns.services || [])) {
        const service = item.detail.service;
        const card = el("article", "service");
        const serviceHead = el("div", "service-head");
        serviceHead.append(el("h3", "", service.name), badge(service.ready ? "Ready" : "Not Ready", service.ready ? "good" : "critical"));
        card.append(serviceHead);
        const badges = el("div", "badges");
        badges.append(badge(service.mode || "mode unknown"));
        if (service.model && service.model.name) badges.append(badge(service.model.name));
        badges.append(badge("generation " + service.observedGeneration + "/" + service.generation));
        card.append(badges);

        const issues = item.issues || [];
        if (issues.length === 0) card.append(el("div", "meta", "未发现控制面异常"));
        for (const issue of issues) {
          const row = el("div", "issue");
          row.append(el("span", "issue-dot " + issue.severity));
          const body = el("div");
          const resource = issue.resource ? " · " + issue.resource : "";
          body.append(el("div", "issue-code", issue.code + resource), el("div", "", issue.message));
          row.append(body);
          card.append(row);
        }
        if (item.analysis) {
          const analysis = el("div", "analysis");
          const title = item.analysis.status === "complete"
            ? "模型分析 · " + (item.analysis.model || "OpenAI-compatible")
            : "模型分析 · " + item.analysis.status;
          analysis.append(el("div", "analysis-title", title));
          analysis.append(el("div", "analysis-body " + (item.analysis.error ? "error" : ""), item.analysis.content || item.analysis.error || "等待下一轮分析"));
          card.append(analysis);
        }
        if (item.remediation) {
          const remediation = el("div", "analysis");
          const target = item.remediation.name
            ? " · " + item.remediation.namespace + "/" + item.remediation.name
            : "";
          const change = item.remediation.changeId
            ? " · change " + item.remediation.changeId.slice(0, 12)
            : "";
          remediation.append(el("div", "analysis-title", "自动恢复 · " + item.remediation.status + target + change));
          const detail = item.remediation.error || item.remediation.message ||
            ("连续严重巡检 " + item.remediation.failureScans + " 次");
          remediation.append(el("div", "analysis-body " + (item.remediation.error ? "error" : ""), detail));
          card.append(remediation);
        }
        services.append(card);
      }
      if ((ns.services || []).length === 0) services.append(el("div", "empty", "该命名空间没有 InferNexService"));
      section.append(services);
      content.append(section);
    }
    byId("footer").textContent = "版本 " + data.version + " · 最近巡检 " + fmtTime(data.generatedAt) + " · 周期 " + data.scanInterval;
    byId("dot").className = "dot " + (data.ready ? "ok" : "");
    byId("connection").textContent = data.ready ? "巡检运行中" : "等待首次巡检";
  }

  async function refresh() {
    try {
      const response = await fetch("./api/v1/snapshot", {cache: "no-store"});
      if (!response.ok) throw new Error("HTTP " + response.status);
      render(await response.json());
    } catch (error) {
      byId("dot").className = "dot error";
      byId("connection").textContent = "连接失败";
    }
  }
  refresh();
  window.setInterval(refresh, 10000);
</script>
</body>
</html>`
