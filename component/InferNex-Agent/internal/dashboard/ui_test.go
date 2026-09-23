package dashboard

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestDashboardNativeSectionSurvivesOtherRequestFailures(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js unavailable")
	}
	start := strings.Index(indexHTML, "<script>")
	end := strings.Index(indexHTML, "</script>")
	if start < 0 || end <= start {
		t.Fatal("dashboard script missing")
	}
	script := indexHTML[start+len("<script>") : end]
	testJS := `const vm=require('node:vm');class E{constructor(tag){this.tag=tag;this.children=[];this.textContent='';this.className=''}append(...v){this.children.push(...v)}replaceChildren(...v){this.children=v}querySelector(s){return this.children.find(x=>x.className===s.slice(1))||null}};const nodes={};const document={getElementById:id=>nodes[id]??(nodes[id]=new E('div')),createElement:tag=>new E(tag)};const fetch=async url=>{if(url.includes('kubernetes'))return {ok:true,json:async()=>({scope:'kubeconfig 可见范围（全部命名空间）',namespaces:null,kubernetesVersion:'v1.33',nodeCount:1,podCount:1,workloadCount:1,podCountInScope:1,workloads:[{kind:'Deployment',namespace:'models',name:'qwen',desired:1,ready:1,helmRelease:'qwen',helmRevision:2,liveYaml:'kind: Deployment\nspec:\n  replicas: 1\n'}],pods:[],warnings:[]})};throw Error('offline')};vm.runInNewContext(SCRIPT,{document,fetch,window:{setInterval:()=>{}},Date,Number,Promise});setTimeout(()=>{const walk=n=>n.textContent+' '+n.children.map(walk).join(' ');const dump=Object.values(nodes).map(walk).join(' ');if(!dump.includes('models/qwen')||!dump.includes('kind: Deployment')||!dump.includes('最新记录 revision 2')||!dump.includes('InferNexService 巡检请求失败'))process.exit(1);if(!nodes.native.children[0].children.some(x=>x.className==='services'&&x.children.some(y=>y.children.some(z=>z.tag==='details'))))process.exit(2)},50);`
	testJS = strings.Replace(testJS, "SCRIPT", strconv.Quote(script), 1)
	out, err := exec.Command("node", "-e", testJS).CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard JS behavior failed: %v %s", err, out)
	}
}
