package inventoryview

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestPageHandlerServesExternalScriptWithSecurityHeaders(t *testing.T) {
	handler := PageHandler()
	for _, item := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/", "text/html; charset=utf-8", `<script defer src="/assets/inventory.js"></script>`},
		{"/assets/inventory.js", "text/javascript; charset=utf-8", `"Authorization":"Bearer "+state.token`},
	} {
		request := httptest.NewRequest(http.MethodGet, item.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", item.path, response.Code)
		}
		if got := response.Header().Get("Content-Type"); got != item.contentType {
			t.Fatalf("GET %s content-type = %q", item.path, got)
		}
		if !strings.Contains(response.Body.String(), item.contains) {
			t.Fatalf("GET %s missing %q", item.path, item.contains)
		}
		if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'self'") || strings.Contains(got, "script-src 'self' 'unsafe-inline'") {
			t.Fatalf("unsafe or missing script CSP: %q", got)
		}
		for name, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY"} {
			if got := response.Header().Get(name); got != want {
				t.Fatalf("GET %s header %s = %q, want %q", item.path, name, got, want)
			}
		}
	}
}

func TestPageHandlerRejectsOtherPathsAndMethods(t *testing.T) {
	handler := PageHandler()
	for _, item := range []struct {
		method string
		path   string
		want   int
	}{{http.MethodGet, "/api/v1/inventory/records", http.StatusNotFound}, {http.MethodGet, "/assets/missing.js", http.StatusNotFound}, {http.MethodPost, "/", http.StatusMethodNotAllowed}, {http.MethodPost, "/assets/inventory.js", http.StatusMethodNotAllowed}} {
		request := httptest.NewRequest(item.method, item.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != item.want {
			t.Fatalf("%s %s status = %d, want %d", item.method, item.path, response.Code, item.want)
		}
	}
	for _, path := range []string{"/?token=must-not-enter-url", "/assets/inventory.js?v=1"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want 400", path, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodHead, "/assets/inventory.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("HEAD script = status %d body %q", response.Code, response.Body.String())
	}
}

func TestInventoryScriptKeepsTokenInMemoryAndUsesTextNodes(t *testing.T) {
	for _, forbidden := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "localStorage", "sessionStorage", "document.cookie", "window.name", "eval("} {
		if strings.Contains(inventoryJS, forbidden) {
			t.Fatalf("inventory script contains forbidden browser API %q", forbidden)
		}
	}
	for _, required := range []string{
		`state.token=entered`, `state.token=""`, `byId("token").value=""`,
		`"Authorization":"Bearer "+state.token`, `credentials:"omit"`, `redirect:"error"`,
		`node.textContent=String(text)`, `data.next`, `?after="+encodeURIComponent(cursor)`,
		`之后快照确认未见的实体（不等于删除）`, `至少一份快照不完整、属于续页或覆盖键不可比`,
		`const visibleGapCount=coverage.filter(item=>item.state!=="complete").length+issues.length`,
		`metric(visibleGapCount,"可见缺口/问题")`, `metric(omittedIssueCount,"省略问题数")`,
		`item.namespace||"不适用/非命名空间范围"`,
		`function renderFactChanges(changes)`, `function renderDiffEntityChange(item)`,
		`function renderDiffRelationChange(item)`, `来源`,
	} {
		if !strings.Contains(inventoryJS, required) {
			t.Fatalf("inventory script missing required behavior %q", required)
		}
	}
	if strings.Contains(indexHTML, "Authorization") || strings.Contains(indexHTML, "localStorage") {
		t.Fatal("HTML shell contains authentication state or persistence code")
	}
	if strings.Contains(indexHTML, `id="token" name=`) || strings.Contains(indexHTML, `name="inventory-access-token"`) {
		t.Fatal("password field name could serialize the token during native form fallback")
	}
	if strings.Contains(inventoryJS, "?token=") || strings.Contains(inventoryJS, "&token=") || strings.Contains(inventoryJS, "access_token") {
		t.Fatal("script appears to place a token in a query parameter")
	}
	if strings.Contains(inventoryJS, "JSON.stringify(item)") {
		t.Fatal("diff falls back to an unreadable raw JSON item")
	}
}

func TestInventoryScriptSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	command := exec.Command(node, "--check")
	command.Stdin = strings.NewReader(inventoryJS)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("node --check: %v\n%s", err, output)
	}
}

func TestInventoryScriptBearerPaginationAndLogoutBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	harness := `
const vm=require("node:vm");
class E {
  constructor(tag,id=""){this.tag=tag;this.id=id;this.children=[];this.textContent="";this.className="";this.value="";this.listeners={};this.disabled=false;this.type="";}
  append(...values){this.children.push(...values)}
  replaceChildren(...values){this.children=values}
  addEventListener(name,fn){this.listeners[name]=fn}
  focus(){}
  get firstChild(){return this.children[0]||null}
  get lastChild(){return this.children[this.children.length-1]||null}
  get classList(){const self=this;const read=()=>new Set(self.className.split(/\s+/).filter(Boolean));const write=s=>self.className=[...s].join(" ");return {add(...v){const s=read();v.forEach(x=>s.add(x));write(s)},remove(...v){const s=read();v.forEach(x=>s.delete(x));write(s)},toggle(v,force){const s=read();const on=force===undefined?!s.has(v):force;if(on)s.add(v);else s.delete(v);write(s);return on},contains(v){return read().has(v)}}}
}
const ids=["login","token","session","app","status","record-detail","before","after","diff","environments","snapshots","environment-count","snapshot-count","more-environments","more-snapshots","logout","refresh","compare"];
const nodes=Object.fromEntries(ids.map(id=>[id,new E("div",id)]));nodes.login.className="auth";nodes.session.className="auth hidden";nodes.app.className="layout hidden";nodes.status.className="notice hidden";
const created=[];const document={getElementById:id=>nodes[id],createElement:tag=>{created.push(tag);return new E(tag)}};
const calls=[];let unauthorized=false;
const response=(status,data)=>({status,ok:status>=200&&status<300,json:async()=>data});
const fetch=async(url,options)=>{calls.push({url,options});if(unauthorized)return response(401,{error:{code:"unauthorized",message:"bad"}});if(url.includes("/diff?"))return response(200,{before:{kind:"InventorySnapshot",id:"22222222-2222-4222-8222-222222222222",revision:1},after:{kind:"InventorySnapshot",id:"33333333-3333-4333-8333-333333333333",revision:1},direction:"before-to-after",environment:{id:"env",runtime:"docker",revision:{before:1,after:1,changed:false}},coverage:{comparable:true,beforeComplete:true,afterComplete:true},entities:{added:[],newlyObserved:[],removed:[],notObserved:[],changed:[{identity:{entityKind:"container",runtime:"docker",nativeId:"native-1"},beforeEntityId:"old-eid",afterEntityId:"new-eid",facts:{added:[],newlyObserved:[],removed:[],notObserved:[],changed:[{field:"phase",before:{value:{type:"string",stringValue:"old"},status:"observed",observedAt:"2026-10-07T00:00:00Z",source:{collector:"test",fieldPath:"before.phase"}},after:{value:{type:"string",stringValue:"new"},status:"conflict",observedAt:"2026-10-07T00:01:00Z",source:{collector:"test",fieldPath:"after.phase"}}}]}}]},relations:{added:[],newlyObserved:[],removed:[],notObserved:[],changed:[{before:{type:"runsOn",fromEntityId:"a",toEntityId:"b",status:"observed"},after:{type:"runsOn",fromEntityId:"a",toEntityId:"c",status:"conflict"}}]}});if(url.includes("after="))return response(200,{records:[],returned:0,truncated:false});if(url.includes("kind=Environment"))return response(200,{records:[{reference:{kind:"Environment",id:"<img src=x>",revision:1},createdAt:"2026-10-07T00:00:00Z",status:"ok"}],returned:1,truncated:true,next:"opaque+/cursor"});return response(200,{records:[],returned:0,truncated:false});};
const sandbox={document,fetch,URLSearchParams,Date,Number,Promise,JSON,Map,Set,Array,String,Error};vm.runInNewContext(SCRIPT,sandbox);
(async()=>{
  nodes.token.value="memory-secret";await nodes.login.listeners.submit({preventDefault(){}});
  if(calls.length!==2||calls.some(c=>c.options.headers.Authorization!=="Bearer memory-secret"))process.exit(1);
  const dump=n=>String(n.textContent||"")+" "+n.children.map(dump).join(" ");
  if(!dump(nodes.environments).includes("<img src=x>")||created.includes("img"))process.exit(2);
  await nodes["more-environments"].listeners.click();
  if(!calls.some(c=>c.url.includes("after=opaque%2B%2Fcursor")))process.exit(3);
  nodes.before.value="22222222-2222-4222-8222-222222222222";nodes.after.value="33333333-3333-4333-8333-333333333333";await nodes.compare.listeners.click();
  const diffText=dump(nodes.diff);if(!diffText.includes("native-1")||!diffText.includes("phase")||!diffText.includes("old")||!diffText.includes("new")||!diffText.includes("before.phase")||!diffText.includes("a  —runsOn→  c"))process.exit(6);
  unauthorized=true;await nodes.refresh.listeners.click();
  if(nodes.login.classList.contains("hidden")||!nodes.app.classList.contains("hidden")||nodes.token.value!=="")process.exit(4);
})().catch(error=>{console.error(error);process.exit(5)});`
	command := exec.Command(node)
	command.Stdin = strings.NewReader("const SCRIPT=" + strconv.Quote(inventoryJS) + ";\n" + harness)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inventory browser behavior: %v\n%s", err, output)
	}
}

func TestPageHandlerBodiesContainNoInlineExecutableScript(t *testing.T) {
	server := httptest.NewServer(PageHandler())
	defer server.Close()
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "<script>") || strings.Contains(text, "javascript:") || !strings.Contains(text, `type="password"`) {
		t.Fatal("dashboard shell has inline script or lacks password token entry")
	}
}
