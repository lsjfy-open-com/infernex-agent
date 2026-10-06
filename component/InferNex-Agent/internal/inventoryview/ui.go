package inventoryview

import (
	"net/http"
)

// PageHandler serves the private inventory dashboard shell and its external
// script. API and authentication routes are provided by New and can be mounted
// alongside this handler by the standalone inventory view server.
func PageHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(response, request)
			return
		}
		if request.URL.RawQuery != "" {
			http.Error(response, "page query parameters are not accepted", http.StatusBadRequest)
			return
		}
		serveUIAsset(response, request, "text/html; charset=utf-8", indexHTML)
	})
	mux.HandleFunc("/assets/inventory.js", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.RawQuery != "" {
			http.Error(response, "asset query parameters are not accepted", http.StatusBadRequest)
			return
		}
		serveUIAsset(response, request, "text/javascript; charset=utf-8", inventoryJS)
	})
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		mux.ServeHTTP(response, request)
	})
}

func serveUIAsset(response http.ResponseWriter, request *http.Request, contentType, body string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = response.Write([]byte(body))
	}
}

const indexHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>InferNex 私域清单</title>
  <style>
    :root{color-scheme:dark;--bg:#07111c;--surface:#0e1c2b;--surface2:#142538;--line:#294057;--text:#e8f1f8;--muted:#91a6ba;--accent:#35d0ba;--good:#56d68c;--warn:#ffc05c;--bad:#ff6e7d;--info:#6dbbff}
    *{box-sizing:border-box} body{margin:0;min-height:100vh;background:radial-gradient(circle at 82% -15%,rgba(53,208,186,.17),transparent 36rem),linear-gradient(180deg,#091522,var(--bg));color:var(--text);font:14px/1.55 Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
    button,input,select{font:inherit} button{cursor:pointer} .hidden{display:none!important}.muted{color:var(--muted)}.error{color:var(--bad)}.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;overflow-wrap:anywhere}
    main{width:min(1500px,calc(100% - 40px));margin:0 auto;padding:34px 0 60px} header{display:flex;justify-content:space-between;gap:24px;align-items:flex-start;margin-bottom:24px} h1{margin:0;font-size:clamp(27px,4vw,42px);letter-spacing:-.04em} h1 span{color:var(--accent)} h2{margin:0;font-size:19px} h3{margin:0;font-size:16px;overflow-wrap:anywhere}.subtitle{margin:6px 0 0;color:var(--muted)}
    .auth{display:flex;gap:8px;align-items:center;flex-wrap:wrap;justify-content:flex-end}.auth input{width:min(320px,65vw)} input,select{min-height:39px;color:var(--text);background:#091624;border:1px solid var(--line);border-radius:9px;padding:8px 11px;outline:none}input:focus,select:focus,button:focus-visible{border-color:var(--accent);box-shadow:0 0 0 3px rgba(53,208,186,.14)}button{min-height:39px;border:1px solid rgba(53,208,186,.42);border-radius:9px;padding:8px 13px;color:var(--text);background:rgba(53,208,186,.11)}button:hover{background:rgba(53,208,186,.2)}button.secondary{border-color:var(--line);background:var(--surface2)}button.danger{border-color:rgba(255,110,125,.45);background:rgba(255,110,125,.1)}button:disabled{cursor:not-allowed;opacity:.5}
    .notice,.panel,.record-card,.entity,.empty{border:1px solid var(--line);border-radius:14px;background:linear-gradient(145deg,rgba(20,37,56,.97),rgba(12,26,41,.97));box-shadow:0 16px 44px rgba(0,0,0,.15)}.notice{padding:11px 14px;margin-bottom:16px}.notice.bad{border-color:rgba(255,110,125,.4)}
    .layout{display:grid;grid-template-columns:310px minmax(0,1fr);gap:16px}.sidebar{display:flex;flex-direction:column;gap:16px}.panel{padding:17px;min-width:0}.panel-head{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:13px}.record-list{display:flex;flex-direction:column;gap:8px}.record-card{width:100%;text-align:left;padding:12px;border-radius:10px;background:rgba(7,17,28,.55)}.record-card.active{border-color:var(--accent);background:rgba(53,208,186,.08)}.record-card strong,.record-card span{display:block}.record-card span{font-size:12px;color:var(--muted);margin-top:3px}.more{width:100%;margin-top:10px}.workspace{display:flex;flex-direction:column;gap:16px;min-width:0}
    .metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(125px,1fr));gap:10px;margin:13px 0}.metric{padding:13px;border:1px solid var(--line);border-radius:11px;background:rgba(7,17,28,.46)}.metric strong{display:block;font-size:24px;letter-spacing:-.03em}.metric span{font-size:11px;color:var(--muted);letter-spacing:.06em;text-transform:uppercase}.badges{display:flex;flex-wrap:wrap;gap:7px;margin:9px 0}.badge{display:inline-flex;border:1px solid var(--line);border-radius:999px;padding:3px 9px;color:var(--muted);font-size:12px}.badge.good{color:var(--good);border-color:rgba(86,214,140,.4)}.badge.warn{color:var(--warn);border-color:rgba(255,192,92,.4)}.badge.bad{color:var(--bad);border-color:rgba(255,110,125,.4)}
    .section{margin-top:18px}.section-title{display:flex;justify-content:space-between;gap:12px;align-items:center;margin-bottom:9px}.table-wrap{overflow:auto;border:1px solid var(--line);border-radius:10px}table{width:100%;border-collapse:collapse;min-width:650px}th,td{padding:9px 11px;text-align:left;border-bottom:1px solid rgba(41,64,87,.68);vertical-align:top}th{position:sticky;top:0;color:var(--muted);font-size:11px;letter-spacing:.05em;text-transform:uppercase;background:#102033}tr:last-child td{border-bottom:0}
    .entities{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,430px),1fr));gap:12px}.entity{padding:14px;background:rgba(7,17,28,.5)}details summary{cursor:pointer;color:var(--accent)}.facts{margin-top:10px}.fact{padding:8px 0;border-top:1px solid rgba(41,64,87,.6)}.fact:first-child{border-top:0}.fact-value{white-space:pre-wrap;overflow-wrap:anywhere}.issue{display:grid;grid-template-columns:8px 1fr;gap:10px;padding:9px 0;border-top:1px solid rgba(41,64,87,.6)}.issue-dot{width:7px;height:7px;border-radius:50%;margin-top:7px;background:var(--warn)}
    .diff-controls{display:grid;grid-template-columns:1fr 1fr auto;gap:9px;align-items:end}.diff-col label{display:block;color:var(--muted);font-size:12px;margin-bottom:4px}.diff-col select{width:100%}.diff-group{margin-top:14px}.diff-item{padding:8px 0;border-top:1px solid rgba(41,64,87,.6)}.empty{padding:28px;text-align:center;color:var(--muted);box-shadow:none}.loading{animation:pulse 1.2s ease-in-out infinite}@keyframes pulse{50%{opacity:.55}}
    @media(max-width:960px){.layout{grid-template-columns:1fr}.sidebar{display:grid;grid-template-columns:1fr 1fr}.sidebar>.panel:first-child{grid-column:1/-1}}
    @media(max-width:650px){main{width:min(100% - 24px,1500px);padding-top:22px}header{flex-direction:column}.auth{width:100%;justify-content:flex-start}.auth input{flex:1;width:auto}.sidebar{display:flex}.diff-controls{grid-template-columns:1fr}.panel{padding:14px}}
  </style>
  <script defer src="/assets/inventory.js"></script>
</head>
<body>
<main>
  <header>
    <div><h1>InferNex <span>私域清单</span></h1><p class="subtitle">只读查看已登记环境、不可变快照与有限证据</p></div>
    <form id="login" class="auth" autocomplete="off"><label class="muted" for="token">访问令牌</label><input id="token" type="password" required autocomplete="off" spellcheck="false" aria-describedby="token-note"><button type="submit">连接</button></form>
    <div id="session" class="auth hidden"><span id="session-state" class="muted">已认证</span><button id="refresh" type="button" class="secondary">刷新</button><button id="logout" type="button" class="danger">退出</button></div>
  </header>
  <p id="token-note" class="notice">令牌仅保存在当前页面内存中，不写入 URL、Cookie 或浏览器存储。关闭或刷新页面后需要重新输入。</p>
  <div id="status" class="notice hidden" role="status" aria-live="polite"></div>
  <section id="app" class="layout hidden" aria-label="私域清单工作区">
    <aside class="sidebar">
      <section class="panel"><div class="panel-head"><h2>环境</h2><span id="environment-count" class="muted"></span></div><div id="environments" class="record-list"></div><button id="more-environments" class="more secondary hidden" type="button">更多环境</button></section>
      <section class="panel"><div class="panel-head"><h2>快照</h2><span id="snapshot-count" class="muted"></span></div><div id="snapshots" class="record-list"></div><button id="more-snapshots" class="more secondary hidden" type="button">更多快照</button></section>
    </aside>
    <div class="workspace">
      <section id="record-detail" class="panel"><div class="empty">选择一个环境或快照查看详情</div></section>
      <section class="panel"><div class="panel-head"><div><h2>快照对比</h2><div class="muted">“未见”仅表示两份快照中的记录差异，不等于资源已删除。</div></div></div><div class="diff-controls"><div class="diff-col"><label for="before">之前快照</label><select id="before"><option value="">请选择</option></select></div><div class="diff-col"><label for="after">之后快照</label><select id="after"><option value="">请选择</option></select></div><button id="compare" type="button">对比</button></div><div id="diff"><div class="empty">选择两份快照后查看有限差异</div></div></section>
    </div>
  </section>
</main>
</body>
</html>`

const inventoryJS = `(() => {
  "use strict";
  const API = "/api/v1/inventory";
  const state = {token:"", environments:[], snapshots:[], environmentNext:"", snapshotNext:"", selected:"", generation:0};
  const byId = id => document.getElementById(id);
  const el = (tag, className, text) => { const node=document.createElement(tag); if(className)node.className=className; if(text!==undefined)node.textContent=String(text); return node; };
  const append = (parent, ...children) => { parent.append(...children); return parent; };
  const value = (input, fallback="") => input === undefined || input === null ? fallback : input;
  const array = input => Array.isArray(input) ? input : [];
  const formatTime = input => { if(!input)return "未知时间"; const date=new Date(input); return Number.isNaN(date.getTime()) ? String(input) : date.toLocaleString(); };
  const refKey = ref => ref ? [value(ref.kind), value(ref.id), value(ref.revision)].join("/") : "";
  const badgeTone = status => status === "complete" || status === "observed" || status === "ok" || status === true ? "good" : status === "failed" || status === "conflict" || status === "corrupt" || status === false ? "bad" : "warn";
  const badge = text => el("span", "badge " + badgeTone(text), text);

  function setStatus(message, tone="") { const node=byId("status"); node.className="notice"+(tone?" "+tone:""); node.textContent=message; node.classList.remove("hidden"); }
  function clearStatus(){ byId("status").classList.add("hidden"); }
  function showAuthenticated(yes){ byId("login").classList.toggle("hidden",yes); byId("session").classList.toggle("hidden",!yes); byId("app").classList.toggle("hidden",!yes); }
  function resetViews(){ state.environments=[];state.snapshots=[];state.environmentNext="";state.snapshotNext="";state.selected="";renderLists();byId("record-detail").replaceChildren(el("div","empty","选择一个环境或快照查看详情"));resetDiff(); }
  function logout(message="已退出，页面中的令牌已清除。") { state.generation++; state.token=""; byId("token").value=""; resetViews(); showAuthenticated(false); setStatus(message); byId("token").focus(); }

  async function request(path){
    if(!state.token)throw new Error("请先输入访问令牌");
    const generation=state.generation;
    const response=await fetch(path,{method:"GET",headers:{"Accept":"application/json","Authorization":"Bearer "+state.token},cache:"no-store",credentials:"omit",redirect:"error"});
    if(generation!==state.generation)throw new Error("会话已经结束");
    let data=null; try{data=await response.json();}catch(_){data=null;}
    if(response.status===401||response.status===403){ logout("认证失败，令牌已从页面内存清除。"); throw new Error("认证失败"); }
    if(!response.ok){ const message=data&&data.error&&data.error.message?data.error.message:"请求失败（HTTP "+response.status+"）"; throw new Error(message); }
    return data||{};
  }

  function recordButton(item){
    const ref=item.reference||{}; const key=refKey(ref); const button=el("button","record-card"+(state.selected===key?" active":"")); button.type="button";
    append(button,el("strong","",ref.kind==="Environment"?("环境 · "+value(ref.id)):"快照"),el("span","mono",key),el("span","",formatTime(item.createdAt)+" · "+value(item.status,"unknown")));
    if(item.problem)button.append(el("span","error",item.problem));
    button.addEventListener("click",()=>selectRecord(ref)); return button;
  }
  function renderLists(){
    const envRoot=byId("environments"),snapRoot=byId("snapshots"); envRoot.replaceChildren();snapRoot.replaceChildren();
    for(const item of state.environments)envRoot.append(recordButton(item)); for(const item of state.snapshots)snapRoot.append(recordButton(item));
    if(!state.environments.length)envRoot.append(el("div","empty","没有可见环境记录")); if(!state.snapshots.length)snapRoot.append(el("div","empty","没有可见快照记录"));
    byId("environment-count").textContent=state.environments.length+" 条已载入";byId("snapshot-count").textContent=state.snapshots.length+" 条已载入";
    byId("more-environments").classList.toggle("hidden",!state.environmentNext);byId("more-snapshots").classList.toggle("hidden",!state.snapshotNext); renderDiffOptions();
  }
  async function loadList(kind, more=false){
    const cursor=kind==="Environment"?state.environmentNext:state.snapshotNext;
    const query=more&&cursor?"?after="+encodeURIComponent(cursor):"?kind="+encodeURIComponent(kind)+"&limit=20";
    const data=await request(API+"/records"+query); const records=array(data.records);
    if(kind==="Environment"){state.environments=more?state.environments.concat(records):records;state.environmentNext=value(data.next);}else{state.snapshots=more?state.snapshots.concat(records):records;state.snapshotNext=value(data.next);} renderLists();
  }
  async function loadInitial(){clearStatus();setStatus("正在读取环境和快照…");await Promise.all([loadList("Environment"),loadList("InventorySnapshot")]);clearStatus();}
  async function selectRecord(ref){
    state.selected=refKey(ref);renderLists();const root=byId("record-detail");root.replaceChildren(el("div","empty loading","正在核验并读取记录…"));
    try{const query=new URLSearchParams({kind:value(ref.kind),id:value(ref.id),revision:String(value(ref.revision))});const data=await request(API+"/record?"+query.toString());renderRecord(data.record||{});}catch(error){root.replaceChildren(el("div","empty error",error.message));}
  }

  const metric=(number,label)=>append(el("div","metric"),el("strong","",number),el("span","",label));
  function recordHeader(record,title){const box=el("div");append(box,el("h2","",title),el("div","badges"));const badges=box.lastChild;badges.append(badge(value(record.kind,"未知类型")),badge("revision "+value(record.revision,"?")),badge(formatTime(record.createdAt)));if(record.digest)badges.append(el("span","badge mono",record.digest));return box;}
  function renderRecord(record){if(record.kind==="Environment")renderEnvironment(record);else if(record.kind==="InventorySnapshot")renderSnapshot(record);else byId("record-detail").replaceChildren(el("div","empty error","服务返回了未知记录类型"));}
  function renderEnvironment(record){
    const root=byId("record-detail"),spec=record.spec||{};root.replaceChildren();root.append(recordHeader(record,"环境 "+value(record.id)));
    const metrics=el("div","metrics");metrics.append(metric(value(spec.runtime,"unknown"),"运行时"),metric(spec.enabled?"启用":"停用","登记状态"),metric(array(spec.allowedNamespaces).length,"授权命名空间"));root.append(metrics);
    const facts=el("div","section");append(facts,el("h3","","登记边界"),el("div","badges"));const row=facts.lastChild;row.append(badge("网络策略 "+value(spec.networkPolicy,"unknown")));if(spec.hostID)row.append(badge("主机 "+spec.hostID));if(spec.clusterID)row.append(badge("集群 "+spec.clusterID));for(const namespace of array(spec.allowedNamespaces))row.append(badge("namespace "+namespace));root.append(facts);
    root.append(el("p","muted","连接地址、凭据引用和身份指纹不会由查看接口返回。环境记录只说明登记边界，不证明当前端点可达或工作负载健康。"));
  }
  function factText(fact){const v=fact&&fact.value;if(!v)return "未知";if(v.type==="string")return value(v.stringValue);if(v.type==="integer")return String(value(v.integerValue));if(v.type==="boolean")return value(v.booleanValue)?"true":"false";if(v.type==="string-list")return array(v.stringListValue).join("，");return "未知";}
  function objectRefText(ref){if(!ref)return "";if(ref.snapshotRef)return refKey(ref.snapshotRef)+" / "+value(ref.entityId);return refKey(ref);}
  function entityLabel(entity){const identity=entity.identity||{};return value(entity.entityKind,"entity")+" · "+value(identity.resourceKind,value(identity.nativeId,value(entity.entityId)));}
  function renderSnapshot(record){
    const root=byId("record-detail"),spec=record.spec||{},entities=array(spec.entities),facts=entities.flatMap(item=>array(item.facts)),relations=array(spec.relations),issues=array(spec.issues),coverage=array(spec.coverage);root.replaceChildren();root.append(recordHeader(record,"清单快照 "+value(record.id)));
    const summary=el("div","badges");summary.append(badge(value(spec.completeness,"unknown")),badge("开始 "+formatTime(spec.startedAt)),badge("完成 "+formatTime(spec.finishedAt)),badge("环境 "+refKey(spec.environmentRef)),badge("采集器 "+value(spec.collectorVersion,"unknown")),badge("规则 "+value(spec.rulesetVersion,"unknown")));if(spec.previousSnapshotRef)summary.append(badge("续页来源 "+refKey(spec.previousSnapshotRef)));root.append(summary);
    if(spec.completeness!=="complete"||spec.previousSnapshotRef)root.append(el("p","notice bad",spec.previousSnapshotRef?"这是分页继续扫描生成的独立快照。即使本页标为 complete，也不代表整个环境在同一时间点完整可见；未出现的资源不能判断为不存在或已删除。":"该快照不完整。未出现的资源只能视为本次未观察到，不能判断为不存在或已删除。"));
    const kindCounts=new Map();for(const entity of entities)kindCounts.set(entity.entityKind,(kindCounts.get(entity.entityKind)||0)+1);const statusCounts=new Map();for(const fact of facts)statusCounts.set(fact.status,(statusCounts.get(fact.status)||0)+1);const visibleGapCount=coverage.filter(item=>item.state!=="complete").length+issues.length;const omittedIssueCount=Number(value(spec.omittedIssueCount,0));
    const metrics=el("div","metrics");metrics.append(metric(entities.length,"实体记录"),metric(facts.length,"事实记录"),metric(relations.length,"关系记录"),metric(visibleGapCount,"可见缺口/问题"),metric(omittedIssueCount,"省略问题数"));for(const [kind,count] of kindCounts)metrics.append(metric(count,kind+" 记录"));root.append(metrics);
    root.append(el("p","muted","数量表示快照内的领域记录或资源声明，不等同于正在运行的服务实例、可用副本、设备容量或健康状态。"));
    const statuses=el("div","badges");for(const name of ["observed","declared","inferred","unknown","conflict"])statuses.append(badge(name+" "+(statusCounts.get(name)||0)));root.append(statuses);
    renderCoverage(root,coverage);renderIssues(root,coverage,issues,omittedIssueCount);renderConfigurations(root,entities);renderEntities(root,entities);renderRelations(root,relations);
  }
  function renderCoverage(root,coverage){const section=el("section","section");append(section,el("div","section-title"));section.firstChild.append(el("h3","","覆盖范围"),el("span","muted",coverage.length+" 项"));if(!coverage.length){section.append(el("div","empty","没有覆盖范围记录"));root.append(section);return;}const wrap=el("div","table-wrap"),table=el("table");const head=el("tr");for(const label of ["资源","命名空间/范围","计数","状态","原因"])head.append(el("th","",label));const thead=el("thead");thead.append(head);table.append(thead);const body=el("tbody");for(const item of coverage){const row=el("tr");for(const text of [item.resourceKind||"—",item.namespace||"不适用/非命名空间范围",value(item.count,0),item.state||"unknown",item.reason||"—"])row.append(el("td","",text));body.append(row);}table.append(body);wrap.append(table);section.append(wrap);root.append(section);}
  function renderIssues(root,coverage,issues,omitted){const gaps=coverage.filter(item=>item.state!=="complete");const section=el("section","section");append(section,el("div","section-title"));section.firstChild.append(el("h3","","缺口与问题"),el("span","muted",(gaps.length+issues.length)+" 项可见 · 另有 "+omitted+" 项问题未列出"));if(!gaps.length&&!issues.length&&!omitted)section.append(el("div","empty","快照未报告缺口；这不表示业务或硬件健康已经验证。"));for(const item of gaps){const row=el("div","issue");append(row,el("span","issue-dot"),append(el("div"),el("strong","",value(item.state,"unknown")+" · "+value(item.resourceKind,"resource")),el("div","muted",(item.namespace||"不适用/非命名空间范围")+(item.reason?" · "+item.reason:""))));section.append(row);}for(const item of issues){const row=el("div","issue");append(row,el("span","issue-dot"),append(el("div"),el("strong","",value(item.code,"unknown")+" · "+value(item.stage,"unknown")),el("div","",value(item.description,"未提供说明")),el("div","muted",(item.retryable?"可重试":"不可确认重试")+(item.objectRef?" · "+objectRefText(item.objectRef):""))));section.append(row);}root.append(section);}
  function renderConfigurations(root,entities){const configs=entities.filter(item=>item.entityKind==="configuration");const section=el("section","section");append(section,el("div","section-title"));section.firstChild.append(el("h3","","配置来源引用"),el("span","muted",configs.length+" 条"));section.append(el("p","muted","仅展示快照白名单内已观察、声明或推断的来源路径/引用；不读取配置正文，也不把引用当作当前文件存在证明。"));if(!configs.length)section.append(el("div","empty","本快照没有配置来源记录"));for(const item of configs){const card=el("article","entity");append(card,el("h3","",entityLabel(item)),el("div","mono muted",value(item.entityId)));for(const fact of array(item.facts)){if(["sourceRef","format","templateRevision","parameterNames","redacted"].includes(fact.field))card.append(append(el("div","fact"),el("strong","",fact.field),el("div","fact-value",factText(fact)),el("div","muted",value(fact.status,"unknown")+" · "+formatTime(fact.observedAt))));}section.append(card);}root.append(section);}
  function renderEntities(root,entities){const section=el("section","section");append(section,el("div","section-title"));section.firstChild.append(el("h3","","实体与事实"),el("span","muted",entities.length+" 条"));const grid=el("div","entities");if(!entities.length)grid.append(el("div","empty","没有实体记录"));for(const item of entities){const identity=item.identity||{},card=el("article","entity");append(card,el("h3","",entityLabel(item)),el("div","mono muted",value(item.entityId)),el("div","badges"));const tags=card.lastChild;for(const text of [identity.runtime,identity.namespace,identity.hostID,identity.clusterID,identity.resourceKind])if(text)tags.append(badge(text));const details=el("details");details.append(el("summary","","事实 "+array(item.facts).length+" 条"));const facts=el("div","facts");for(const fact of array(item.facts)){const source=fact.source||{};facts.append(append(el("div","fact"),el("strong","",value(fact.field,"unknown")),el("div","fact-value",factText(fact)),el("div","muted",value(fact.status,"unknown")+" · "+formatTime(fact.observedAt)),el("div","mono muted",value(source.collector)+(source.fieldPath?" · "+source.fieldPath:"")+(source.objectRef?" · "+objectRefText(source.objectRef):""))));}details.append(facts);card.append(details);grid.append(card);}section.append(grid);root.append(section);}
  function renderRelations(root,relations){const section=el("section","section");append(section,el("div","section-title"));section.firstChild.append(el("h3","","关系"),el("span","muted",relations.length+" 条"));if(!relations.length)section.append(el("div","empty","没有关系记录"));else{const wrap=el("div","table-wrap"),table=el("table"),head=el("tr");for(const name of ["类型","来源实体","目标实体","状态","证据数"])head.append(el("th","",name));const thead=el("thead");thead.append(head);table.append(thead);const body=el("tbody");for(const relation of relations){const row=el("tr");for(const text of [relation.type,relation.fromEntityId,relation.toEntityId,relation.status,array(relation.evidenceRefs).length])row.append(el("td","mono",value(text,"—")));body.append(row);}table.append(body);wrap.append(table);section.append(wrap);}root.append(section);}

  function resetDiff(){byId("before").replaceChildren(option("","请选择"));byId("after").replaceChildren(option("","请选择"));byId("diff").replaceChildren(el("div","empty","选择两份快照后查看有限差异"));}
  function option(valueText,label){const node=el("option","",label);node.value=valueText;return node;}
  function renderDiffOptions(){const beforeValue=byId("before").value,afterValue=byId("after").value;byId("before").replaceChildren(option("","请选择"));byId("after").replaceChildren(option("","请选择"));for(const item of state.snapshots){const ref=item.reference||{},label=formatTime(item.createdAt)+" · "+value(ref.id);byId("before").append(option(value(ref.id),label));byId("after").append(option(value(ref.id),label));}byId("before").value=beforeValue;byId("after").value=afterValue;}
  function collectDiffGroups(data){const groups=[];const entities=data.entities||{},relations=data.relations||{};for(const [source,key,label,type] of [[entities,"added","确认仅在之后快照出现的实体","entity"],[entities,"newlyObserved","之后快照新增观察（覆盖不可比）","entity"],[entities,"changed","事实发生变化的实体","entity-change"],[entities,"removed","之后快照确认未见的实体（不等于删除）","entity"],[entities,"notObserved","之后快照未观察到的实体（覆盖不可比，不等于删除）","entity"],[relations,"added","确认仅在之后快照出现的关系","relation"],[relations,"newlyObserved","之后快照新增观察的关系（覆盖不可比）","relation"],[relations,"changed","状态发生变化的关系","relation-change"],[relations,"removed","之后快照确认未见的关系（不等于删除）","relation"],[relations,"notObserved","之后快照未观察到的关系（覆盖不可比，不等于删除）","relation"]])if(Array.isArray(source[key])&&source[key].length)groups.push({title:label,items:source[key],type});return groups;}
  function identityTitle(identity,kind){return value(kind,value(identity.entityKind,"entity"))+" · "+value(identity.resourceKind,value(identity.nativeId,"未知原生标识"));}
  function appendIdentity(card,identity){const tags=el("div","badges");for(const text of [identity.runtime,identity.namespace,identity.hostID,identity.clusterID,identity.resourceKind])if(text)tags.append(badge(text));card.append(tags);if(identity.nativeId)card.append(el("div","mono muted","nativeID · "+identity.nativeId));}
  function renderDiffEntity(item){const identity=item.identity||{},card=el("article","entity");card.append(el("h3","",identityTitle(identity,item.entityKind)));appendIdentity(card,identity);if(item.entityId)card.append(el("div","mono muted","entityID · "+item.entityId));return card;}
  function sourceDetails(fact,label){const source=fact&&fact.source||{};const parts=[source.collector,source.fieldPath,source.objectRef?objectRefText(source.objectRef):""].filter(Boolean);if(!parts.length)return null;const details=el("details");details.append(el("summary","",label+"来源"),el("div","mono muted",parts.join(" · ")));return details;}
  function factSide(fact,emptyText){const cell=el("td");if(!fact){cell.append(el("span","muted",emptyText));return cell;}cell.append(el("div","fact-value",factText(fact)),badge(value(fact.status,"unknown")),el("div","muted",formatTime(fact.observedAt)));const details=sourceDetails(fact,"");if(details)cell.append(details);return cell;}
  function factRows(changes){const rows=[];for(const fact of array(changes.added))rows.push({field:fact.field,before:null,after:fact,certainty:"确认新增"});for(const fact of array(changes.newlyObserved))rows.push({field:fact.field,before:null,after:fact,certainty:"新增观察"});for(const item of array(changes.changed))rows.push({field:item.field,before:item.before,after:item.after,certainty:"值或状态变化"});for(const fact of array(changes.removed))rows.push({field:fact.field,before:fact,after:null,certainty:"确认未见"});for(const fact of array(changes.notObserved))rows.push({field:fact.field,before:fact,after:null,certainty:"未观察到"});return rows;}
  function renderFactChanges(changes){const rows=factRows(changes||{});if(!rows.length)return el("div","empty","实体身份匹配，但没有可展示的事实差异");const wrap=el("div","table-wrap"),table=el("table"),head=el("tr");for(const label of ["字段","之前值 / 状态","之后值 / 状态","判定"])head.append(el("th","",label));const thead=el("thead");thead.append(head);table.append(thead);const body=el("tbody");for(const item of rows){const row=el("tr");row.append(el("td","mono",value(item.field,"unknown")),factSide(item.before,"未记录"),factSide(item.after,"未见"),el("td","",item.certainty));body.append(row);}table.append(body);wrap.append(table);return wrap;}
  function renderDiffEntityChange(item){const identity=item.identity||{},card=el("article","entity");card.append(el("h3","",identityTitle(identity,identity.entityKind)));appendIdentity(card,identity);card.append(el("div","mono muted","entityID · "+value(item.beforeEntityId,"—")+" → "+value(item.afterEntityId,"—")),renderFactChanges(item.facts));return card;}
  function relationLine(item){return value(item.fromEntityId,"未知来源")+"  —"+value(item.type,"unknown")+"→  "+value(item.toEntityId,"未知目标");}
  function renderDiffRelation(item){const card=el("article","entity");card.append(el("h3","",value(item.type,"关系")),el("div","mono",relationLine(item)),el("div","badges"));card.lastChild.append(badge(value(item.status,"unknown")));return card;}
  function renderDiffRelationChange(item){const card=el("article","entity"),before=item.before||{},after=item.after||{};card.append(el("h3","",value(after.type,value(before.type,"关系"))),el("div","fact"));card.lastChild.append(el("strong","","之前"),el("div","mono",relationLine(before)),badge(value(before.status,"unknown")));card.append(el("div","fact"));card.lastChild.append(el("strong","","之后"),el("div","mono",relationLine(after)),badge(value(after.status,"unknown")));return card;}
  function renderDiffItem(type,item){if(type==="entity-change")return renderDiffEntityChange(item);if(type==="relation")return renderDiffRelation(item);if(type==="relation-change")return renderDiffRelationChange(item);return renderDiffEntity(item);}
  function renderDiff(data){const root=byId("diff");root.replaceChildren();const coverage=data.coverage||{},environment=data.environment||{},revision=environment.revision||{};const comparable=coverage.comparable===true&&coverage.beforeComplete===true&&coverage.afterComplete===true;const badges=el("div","badges");badges.append(badge("方向 "+value(data.direction,"用户选择顺序")),badge("之前 "+refKey(data.before)),badge("之后 "+refKey(data.after)),badge("之前覆盖 "+(coverage.beforeComplete?"完整":"不完整")),badge("之后覆盖 "+(coverage.afterComplete?"完整":"不完整")),badge(coverage.comparable?"覆盖键可比":"覆盖键不可比"));if(environment.id)badges.append(badge("环境 "+environment.id));if(revision.before!==undefined)badges.append(badge("环境 revision "+revision.before+" → "+revision.after+(revision.changed?"（已变化）":"")));root.append(badges);root.append(el("p",comparable?"notice":"notice bad",comparable?"两份快照覆盖范围完整且可比；差异仍只表示两个时间点的记录，不证明资源何时变化或由谁删除。":"至少一份快照不完整、属于续页或覆盖键不可比；“新增观察”和“未观察到”可能来自权限、超时、分页或采集缺口，不能解释为创建或删除。"));const groups=collectDiffGroups(data);if(!groups.length){root.append(el("div","empty","接口未报告可见差异；这不证明环境在时间区间内没有变化。"));return;}for(const groupData of groups){const group=el("section","diff-group");group.append(el("h3","",groupData.title));const cards=el("div","entities");for(const item of groupData.items)cards.append(renderDiffItem(groupData.type,item));group.append(cards);root.append(group);}}
  async function compare(){const before=byId("before").value,after=byId("after").value;if(!before||!after){setStatus("请选择之前和之后两份快照。","bad");return;}if(before===after){setStatus("请选择两份不同快照。","bad");return;}clearStatus();byId("diff").replaceChildren(el("div","empty loading","正在核验并对比快照…"));try{const query=new URLSearchParams({before,after});const data=await request(API+"/diff?"+query.toString());renderDiff(data);}catch(error){byId("diff").replaceChildren(el("div","empty error",error.message));}}

  byId("login").addEventListener("submit",async event=>{event.preventDefault();const entered=byId("token").value;if(!entered){setStatus("请输入访问令牌。","bad");return;}state.token=entered;state.generation++;byId("token").value="";showAuthenticated(true);resetViews();try{await loadInitial();}catch(error){if(state.token)setStatus(error.message,"bad");}});
  byId("logout").addEventListener("click",()=>logout());byId("refresh").addEventListener("click",async()=>{try{await loadInitial();}catch(error){if(state.token)setStatus(error.message,"bad");}});byId("more-environments").addEventListener("click",async()=>{try{await loadList("Environment",true);}catch(error){setStatus(error.message,"bad");}});byId("more-snapshots").addEventListener("click",async()=>{try{await loadList("InventorySnapshot",true);}catch(error){setStatus(error.message,"bad");}});byId("compare").addEventListener("click",compare);
  showAuthenticated(false);byId("token").focus();
})();`
