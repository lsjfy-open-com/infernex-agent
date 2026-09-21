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
	"context"
	"encoding/json"
	"net/http"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/experiment"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/slo"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

type SnapshotReader interface {
	Load() supervisor.Snapshot
}

type ExperimentReader interface {
	List(context.Context) ([]experiment.Plan, error)
}

type options struct {
	experiments   ExperimentReader
	kubernetes    KubernetesReader
	namespaces    []string
	publicSummary bool
	management    ManagementInfo
}

// ManagementInfo describes where the operator can find the local inputs used
// by the dashboard's SLO and version views. It intentionally contains paths
// and summaries only; it never reads or publishes profile prompts, endpoints,
// credentials, or configuration contents.
type ManagementInfo struct {
	AgentConfigPath        string        `json:"agentConfigPath,omitempty"`
	SLOProfileDirectory    string        `json:"sloProfileDirectory,omitempty"`
	ConfigVersionDirectory string        `json:"configVersionDirectory,omitempty"`
	SLOEnabled             bool          `json:"sloEnabled"`
	SLOProfiles            []slo.Summary `json:"sloProfiles"`
}

type Option func(*options)

func WithExperiments(reader ExperimentReader) Option {
	return func(options *options) {
		options.experiments = reader
	}
}
func WithKubernetes(reader KubernetesReader, namespaces []string) Option {
	return func(o *options) { o.kubernetes = reader; o.namespaces = append([]string(nil), namespaces...) }
}
func WithPublicSummary(enabled bool) Option { return func(o *options) { o.publicSummary = enabled } }
func WithManagementInfo(info ManagementInfo) Option {
	return func(o *options) {
		info.SLOProfiles = append([]slo.Summary(nil), info.SLOProfiles...)
		o.management = info
	}
}
func writeJSON(w http.ResponseWriter, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
	}
}
func publicSnapshot(source supervisor.Snapshot) supervisor.Snapshot {
	out := supervisor.Snapshot{Version: source.Version, StartedAt: source.StartedAt, GeneratedAt: source.GeneratedAt, ScanInterval: source.ScanInterval, ScanDurationMs: source.ScanDurationMs, Ready: source.Ready, AnalyzerEnabled: source.AnalyzerEnabled, Summary: source.Summary, Namespaces: make([]supervisor.NamespaceSnapshot, 0, len(source.Namespaces))}
	for _, ns := range source.Namespaces {
		safe := supervisor.NamespaceSnapshot{Name: ns.Name, ScannedAt: ns.ScannedAt, Truncated: ns.Truncated, Total: ns.Total, ScanMillis: ns.ScanMillis, Services: make([]supervisor.ServiceSnapshot, 0, len(ns.Services))}
		if ns.Error != "" {
			safe.Error = "巡检请求失败"
		}
		for _, item := range ns.Services {
			service := item.Detail.Service
			safe.Services = append(safe.Services, supervisor.ServiceSnapshot{Detail: observer.ServiceDetail{Service: observer.ServiceSummary{Namespace: service.Namespace, Name: service.Name, Mode: service.Mode, Ready: service.Ready, Generation: service.Generation, ObservedGeneration: service.ObservedGeneration}}})
		}
		out.Namespaces = append(out.Namespaces, safe)
	}
	return out
}
func publicExperiments(source []experiment.Plan) []experiment.Plan {
	out := make([]experiment.Plan, 0, len(source))
	for _, plan := range source {
		safe := experiment.Plan{ID: plan.ID, Namespace: plan.Namespace, BaselineName: plan.BaselineName, CandidatePrefix: plan.CandidatePrefix, FeatureProfiles: append([]string(nil), plan.FeatureProfiles...), SLOProfile: plan.SLOProfile, SLOGateMode: plan.SLOGateMode, Status: plan.Status, CurrentStage: plan.CurrentStage, StableService: plan.StableService, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt, Stages: make([]experiment.Stage, 0, len(plan.Stages))}
		for _, stage := range plan.Stages {
			item := experiment.Stage{Index: stage.Index, FeatureProfile: stage.FeatureProfile, BaselineName: stage.BaselineName, CandidateName: stage.CandidateName, Status: stage.Status, StartedAt: stage.StartedAt, ReadyAt: stage.ReadyAt, CompletedAt: stage.CompletedAt}
			if stage.SLO != nil {
				item.SLO = &slo.Result{RunID: stage.SLO.RunID, Decision: stage.SLO.Decision, Baseline: stage.SLO.Baseline, Candidate: stage.SLO.Candidate, EvidenceSHA256: stage.SLO.EvidenceSHA256}
			}
			safe.Stages = append(safe.Stages, item)
		}
		out = append(out, safe)
	}
	return out
}

func New(reader SnapshotReader, optionFunctions ...Option) http.Handler {
	configuration := options{}
	for _, option := range optionFunctions {
		option(&configuration)
	}
	mux := http.NewServeMux()
	if configuration.kubernetes != nil {
		cache := newNativeCache(configuration.kubernetes, configuration.namespaces)
		mux.HandleFunc("/api/v1/kubernetes", cache.serve)
	}
	mux.HandleFunc("/api/v1/management", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		writeJSON(response, configuration.management)
	})
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
		data := reader.Load()
		if configuration.publicSummary {
			data = publicSnapshot(data)
		}
		if err := encoder.Encode(data); err != nil {
			http.Error(response, "encode snapshot", http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/api/v1/experiments", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		plans := []experiment.Plan{}
		if configuration.experiments != nil {
			var err error
			plans, err = configuration.experiments.List(request.Context())
			if err != nil {
				http.Error(response, "list experiments", http.StatusInternalServerError)
				return
			}
		}
		if configuration.publicSummary {
			plans = publicExperiments(plans)
		}
		encoder := json.NewEncoder(response)
		encoder.SetEscapeHTML(true)
		if err := encoder.Encode(plans); err != nil {
			http.Error(response, "encode experiments", http.StatusInternalServerError)
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
	.experiment-stages { margin-top: 10px; }
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
      <p class="subtitle">Kubernetes 与推理服务概览</p>
    </div>
    <div class="connection"><span id="dot" class="dot"></span><span id="connection">正在连接</span></div>
  </header>
  <section id="metrics" class="metrics"></section>
  <section id="management"></section>
  <section id="native"><div class="empty">正在读取 Kubernetes 工作负载…</div></section>
	<section id="experiments"></section>
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

  function renderManagement(data) {
    const root = byId("management");
    root.replaceChildren();
    const section = el("section", "namespace");
    section.append(el("h2", "", "运行配置与版本入口"));
    const rows = el("div", "services");
    const slo = el("article", "service");
    if (data.agentConfigPath) slo.append(el("div", "meta", "Agent 配置：" + data.agentConfigPath));
    slo.append(el("h3", "", "SLO 对照实验"));
    slo.append(el("div", "meta", data.sloEnabled ? "已启用：仅使用管理员批准的 profile" : "未启用：实验不会发送 SLO 请求"));
    if (data.sloProfileDirectory) slo.append(el("div", "meta", "profile 目录：" + data.sloProfileDirectory));
    const profiles = data.sloProfiles || [];
    slo.append(el("div", "meta", "可用 profile：" + (profiles.length ? profiles.map(p => p.id + " · " + p.version).join("，") : "无")));
    rows.append(slo);
    const versions = el("article", "service");
    versions.append(el("h3", "", "Git / snapshot 版本记录"));
    versions.append(el("div", "meta", "记录目录：" + (data.configVersionDirectory || "未配置")));
    versions.append(el("div", "meta", "使用 host CLI：infernex-agent config-version list/show/verify"));
    rows.append(versions);
    section.append(rows);
    root.append(section);
  }

  function render(data) {
    const summary = data.summary || {};
    const metrics = byId("metrics");
    const bridgeNamespaces = data.namespaces || [];
    metrics.replaceChildren(...(bridgeNamespaces.length ? [
      metric(summary.services, "Bridge 服务"),
      metric(summary.readyServices, "健康", "good"),
      metric(summary.degradedServices, "异常", summary.degradedServices ? "critical" : ""),
      metric(summary.issues, "问题"),
      metric(summary.criticalIssues, "严重", summary.criticalIssues ? "critical" : ""),
      metric(summary.warningIssues, "Bridge 告警", summary.warningIssues ? "warning" : "")
    ] : []));

    const content = byId("content");
    content.replaceChildren();
    const namespaces = data.namespaces || [];
    if (!data.ready) {
      content.append(el("div", "empty", "等待首次 InferNexService 巡检结果…"));
    } else if (namespaces.length === 0) {
      content.append(el("div", "empty", "Bridge 巡检未配置；上方仍显示原生 Kubernetes 工作负载。"));
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
        if (issues.length === 0) card.append(el("div", "meta", "详细诊断与证据请通过 Agent 查询"));
        for (const issue of issues) {
          const row = el("div", "issue");
          row.append(el("span", "issue-dot " + issue.severity));
          const body = el("div");
          const resource = issue.resource ? " · " + issue.resource : "";
          body.append(el("div", "issue-code", issue.code + resource), el("div", "", issue.message));
          row.append(body);
          card.append(row);
        }
		const incidents = item.diagnostics ? (item.diagnostics.incidents || []) : [];
		for (const incident of incidents) {
		  const diagnostic = el("div", "analysis");
		  diagnostic.append(el("div", "analysis-title", "关联诊断 · " + incident.rootCategory + " · " + incident.confidence));
		  const scope = [];
		  if ((incident.components || []).length) scope.push("组件 " + incident.components.join(", "));
		  if ((incident.nodes || []).length) scope.push("节点 " + incident.nodes.join(", "));
		  diagnostic.append(el("div", "meta", scope.join(" · ")));
		  diagnostic.append(el("div", "analysis-body", incident.recommendation || "请检查关联时间线"));
		  card.append(diagnostic);
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
    byId("connection").textContent = bridgeNamespaces.length === 0 ? "Bridge 巡检未配置" : (data.ready ? "Bridge 巡检运行中" : "等待首次 Bridge 巡检");
  }

	function renderExperiments(plans) {
	  const root = byId("experiments");
	  root.replaceChildren();
	  if (!plans || plans.length === 0) return;
	  const section = el("section", "namespace");
	  const head = el("div", "namespace-head");
	  head.append(el("h2", "", "渐进式特性实验"), el("div", "meta", plans.length + " 个计划"));
	  section.append(head);
	  const cards = el("div", "services");
	  for (const plan of plans) {
		const card = el("article", "service");
		const cardHead = el("div", "service-head");
		cardHead.append(el("h3", "", plan.namespace + "/" + plan.candidatePrefix), badge(plan.status, plan.status === "completed" ? "good" : (plan.status === "failed" ? "critical" : "")));
		card.append(cardHead);
		const badges = el("div", "badges");
		badges.append(badge("基线 " + plan.baselineName), badge("当前稳定 " + plan.stableService), badge("阶段 " + plan.currentStage + "/" + (plan.stages || []).length));
		badges.append(badge(plan.sloProfile ? "SLO: " + plan.sloProfile : "就绪与诊断门禁 · 未运行 SLO"));
		card.append(badges);
		if (plan.message) card.append(el("div", "meta", plan.message));
		const stages = el("div", "experiment-stages");
		for (const stage of (plan.stages || [])) {
		  const row = el("div", "issue");
		  row.append(el("span", "issue-dot " + (stage.status === "passed" ? "" : (stage.status === "rolled-back" ? "critical" : "warning"))));
		  const body = el("div");
		  body.append(el("div", "issue-code", "S" + (stage.index + 1) + " · " + stage.featureProfile + " · " + stage.status));
		  body.append(el("div", "", stage.baselineName + " → " + stage.candidateName));
		  if (stage.slo) {
			const result = stage.slo;
			const labels = {passed: "通过", regression: "退化", inconclusive: "证据不足"};
			body.append(badge("SLO " + (labels[result.decision] || result.decision), result.decision === "passed" ? "good" : (result.decision === "regression" ? "critical" : "warning")));
			if (result.reason) body.append(el("div", "meta", result.reason));
			if (result.baseline && result.candidate) {
			  body.append(el("div", "meta", "端到端 p95: " + Number(result.baseline.p95Millis || 0).toFixed(1) + " → " + Number(result.candidate.p95Millis || 0).toFixed(1) + " ms；成功率: " + (100 * Number(result.baseline.successRate || 0)).toFixed(1) + "% → " + (100 * Number(result.candidate.successRate || 0)).toFixed(1) + "%"));
			}
			body.append(el("div", "meta", "证据 " + result.runId + (result.evidenceSha256 ? " · SHA256 " + result.evidenceSha256 : " · 未完成")));
		  }
		  if (stage.comparison && (stage.comparison.regressionCategories || []).length) body.append(el("div", "error", "新增异常: " + stage.comparison.regressionCategories.join(", ")));
		  if (stage.message) body.append(el("div", "meta", stage.message));
          else if (stage.status === "rolled-back") body.append(el("div", "meta", "阶段未通过；详细原因请通过 Agent 查询"));
		  row.append(body);
		  stages.append(row);
		}
		card.append(stages);
		cards.append(card);
	  }
	  section.append(cards);
	  root.append(section);
	}

  function renderNative(data) {
    const root=byId("native");root.replaceChildren();const section=el("section","namespace");
    section.append(el("h2","","原生 Kubernetes 工作负载与 Pod"),el("div","meta","范围："+data.scope+((data.namespaces||[]).length?" · "+(data.namespaces||[]).join(", "):"")+" · 与 InferNexService 巡检分别统计"));
    if(data.stale)section.append(el("p","error",data.error||"Kubernetes 数据已过期"));
    section.append(el("div","meta","采集时间："+fmtTime(data.updatedAt)));
    const stats=el("div","badges");stats.append(badge("Kubernetes "+(data.kubernetesVersion||"未知")),badge("集群节点 "+(data.overviewPartial?"未知/部分可见":data.nodeCount)),badge("集群 Pod "+(data.overviewPartial?"未知/部分可见":data.podCount)),badge("范围内工作负载 "+data.workloadCount),badge("范围内 Pod "+data.podCountInScope));section.append(stats);
    for(const warning of (data.warnings||[]))section.append(el("p","error","读取受限："+warning));
    if(data.truncated||data.scopeTruncated)section.append(el("p","meta","展示结果或命名空间范围已截断"));
    const items=el("div","services");for(const work of (data.workloads||[])){const card=el("article","service");card.append(el("h3","",work.namespace+"/"+work.name),el("div","badges",work.kind+" · Ready "+work.ready+"/"+work.desired));items.append(card)}
    for(const pod of (data.pods||[])){const card=el("article","service");card.append(el("h3","",pod.namespace+"/"+pod.name),el("div","badges","Pod · "+pod.phase+" · "+(pod.ready?"Ready":"Not Ready")+" · 重启 "+pod.restarts));items.append(card)}
    if((data.workloads||[]).length+(data.pods||[]).length===0)items.append(el("div","empty",data.warnings&&data.warnings.length?"权限不足或读取受限，无法确认是否存在资源":"当前范围没有匹配的原生工作负载或 Pod"));section.append(items);root.append(section);
  }
  async function refreshPart(url,onSuccess,onError){try{const response=await fetch(url,{cache:"no-store"});if(!response.ok)throw new Error("HTTP "+response.status);onSuccess(await response.json())}catch(error){onError(error)}}
  async function refresh() {
    await Promise.all([
      refreshPart("./api/v1/kubernetes",renderNative,()=>{if(!byId("native").querySelector(".namespace"))byId("native").replaceChildren(el("div","empty error","原生 Kubernetes 请求失败，请稍后重试"));else byId("native").append(el("p","error","原生 Kubernetes 更新失败，保留上次结果"))}),
      refreshPart("./api/v1/management",renderManagement,()=>{byId("management").replaceChildren(el("div","empty error","运行配置读取失败"))}),
      refreshPart("./api/v1/snapshot",render,()=>{byId("dot").className="dot error";byId("connection").textContent="InferNexService 巡检请求失败"}),
      refreshPart("./api/v1/experiments",renderExperiments,()=>{if(!byId("experiments").querySelector(".namespace"))byId("experiments").replaceChildren(el("div","empty error","实验状态请求失败"));else byId("experiments").append(el("p","error","实验状态更新失败，保留上次结果"))})
    ]);
  }
  refresh();
  window.setInterval(refresh, 10000);
</script>
</body>
</html>`
