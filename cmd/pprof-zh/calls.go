package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/pprof/cmd/pprof-zh/tracer"
	"github.com/google/pprof/profile"
)

// callsFile 返回「调用统计」数据文件路径：环境变量 PPROF_ZH_CALLS 优先，
// 否则取命令行中最后一个 .pprof 参数同目录下的 calls.json。
func callsFile() string {
	if p := os.Getenv("PPROF_ZH_CALLS"); p != "" {
		return p
	}
	for i := len(os.Args) - 1; i > 0; i-- {
		if strings.HasSuffix(os.Args[i], ".pprof") || strings.HasSuffix(os.Args[i], ".pb.gz") {
			p := filepath.Join(filepath.Dir(os.Args[i]), "calls.json")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// profPath 是命令行里最后一个 .pprof / .pb.gz 参数，用于给调用路径补充 CPU 采样。
var profPath = func() string {
	for i := len(os.Args) - 1; i > 0; i-- {
		if strings.HasSuffix(os.Args[i], ".pprof") || strings.HasSuffix(os.Args[i], ".pb.gz") {
			return os.Args[i]
		}
	}
	return ""
}()

type funcRow struct {
	Name           string
	Calls          int64
	Wall, Self, Av string
	Alloc          string
	SelfPct        float64
}

// treeRow 是调用路径树中的一行（按深度优先展开）。
type treeRow struct {
	Depth, Indent      int
	Name               string
	Calls              int64
	PerParent          float64
	HasParent, Suspect bool
	HasKids            bool
	Wall, WallPct      string
	WallPctF           float64
	Self, Alloc        string
	CPU, Samples       string
	Advice, ParentName string
}

type callsView struct {
	Funcs     []funcRow
	Tree      []treeRow
	Suspects  []treeRow
	Threshold int
	GenAt     string
	HasCPU    bool
}

type node struct {
	name                          string
	calls, wallNs, selfNs, allocB int64
	kids                          []*node
	parent                        *node
}

// cpuStat 是某个函数在 CPU profile 里的累计（含子调用）采样。
type cpuStat struct{ cumNs, cumN int64 }

// loadCPU 读取 pprof 的 CPU 采样，按函数符号汇总“含子调用”的 CPU 时间与采样数。
func loadCPU(path string) map[string]cpuStat {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	p, err := profile.Parse(f)
	if err != nil {
		return nil
	}
	ci, si := -1, -1
	for i, st := range p.SampleType {
		switch st.Type {
		case "cpu":
			ci = i
		case "samples":
			si = i
		}
	}
	if ci < 0 {
		return nil
	}
	res := map[string]cpuStat{}
	for _, s := range p.Sample {
		seen := map[string]bool{}
		for _, loc := range s.Location {
			for _, ln := range loc.Line {
				if ln.Function == nil || seen[ln.Function.Name] {
					continue
				}
				seen[ln.Function.Name] = true
				c := res[ln.Function.Name]
				c.cumNs += s.Value[ci]
				if si >= 0 {
					c.cumN += s.Value[si]
				}
				res[ln.Function.Name] = c
			}
		}
	}
	return res
}

func durFmt(ns int64) string {
	switch {
	case ns >= 1e9:
		return fmt.Sprintf("%.2fs", float64(ns)/1e9)
	case ns >= 1e6:
		return fmt.Sprintf("%.2fms", float64(ns)/1e6)
	case ns >= 1e3:
		return fmt.Sprintf("%.1fµs", float64(ns)/1e3)
	}
	return fmt.Sprintf("%dns", ns)
}

func bytesFmt(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

func buildCalls(rep tracer.Report, threshold int, cpu map[string]cpuStat) callsView {
	v := callsView{Threshold: threshold, GenAt: rep.GeneratedAt, HasCPU: cpu != nil}
	sym := map[string]string{}
	var totalSelf int64
	for _, f := range rep.Funcs {
		sym[f.Name] = f.Symbol
		totalSelf += f.SelfNs
	}
	for _, f := range rep.Funcs {
		r := funcRow{Name: f.Name, Calls: f.Calls, Wall: durFmt(f.WallNs), Self: durFmt(f.SelfNs),
			Av: durFmt(f.WallNs / max64(f.Calls, 1)), Alloc: bytesFmt(f.AllocBytes)}
		if totalSelf > 0 {
			r.SelfPct = float64(f.SelfNs) * 100 / float64(totalSelf)
		}
		v.Funcs = append(v.Funcs, r)
	}
	// 由路径还原调用树
	nodes := map[string]*node{}
	var roots []*node
	for _, p := range rep.Paths { // rep.Paths 已按路径字典序，父一定在子之前
		key := strings.Join(p.Path, "\x00")
		n := &node{name: p.Path[len(p.Path)-1], calls: p.Calls, wallNs: p.WallNs, selfNs: p.SelfNs, allocB: p.AllocBytes}
		nodes[key] = n
		if len(p.Path) == 1 {
			roots = append(roots, n)
		} else if par := nodes[strings.Join(p.Path[:len(p.Path)-1], "\x00")]; par != nil {
			n.parent = par
			par.kids = append(par.kids, n)
		} else {
			roots = append(roots, n)
		}
	}
	byWall := func(l []*node) { sort.Slice(l, func(i, j int) bool { return l[i].wallNs > l[j].wallNs }) }
	byWall(roots)
	var walk func(n *node, depth int, rootWall int64)
	walk = func(n *node, depth int, rootWall int64) {
		r := treeRow{Depth: depth, Indent: depth * 18, Name: n.name, Calls: n.calls, Wall: durFmt(n.wallNs),
			Self: durFmt(n.selfNs), Alloc: bytesFmt(n.allocB), HasKids: len(n.kids) > 0, CPU: "-", Samples: "-"}
		if rootWall > 0 {
			r.WallPctF = float64(n.wallNs) * 100 / float64(rootWall)
		}
		r.WallPct = fmt.Sprintf("%.1f%%", r.WallPctF)
		if c, ok := cpu[sym[n.name]]; ok && sym[n.name] != "" {
			r.CPU, r.Samples = durFmt(c.cumNs), strconv.FormatInt(c.cumN, 10)
		}
		if n.parent != nil {
			r.HasParent, r.ParentName = true, n.parent.name
			r.PerParent = float64(n.calls) / float64(max64(n.parent.calls, 1))
			if r.PerParent >= float64(threshold) {
				r.Suspect = true
				r.Advice = fmt.Sprintf("%s 每次执行平均调用 %s %.0f 次，累计耗时 %s。若这些调用彼此独立，建议在外层一次批量取回，再在内存里分发。",
					n.parent.name, n.name, r.PerParent, durFmt(n.wallNs))
				v.Suspects = append(v.Suspects, r)
			}
		}
		v.Tree = append(v.Tree, r)
		byWall(n.kids)
		for _, k := range n.kids {
			walk(k, depth+1, rootWall)
		}
	}
	for _, r := range roots {
		walk(r, 0, r.wallNs)
	}
	sort.Slice(v.Suspects, func(i, j int) bool { return v.Suspects[i].PerParent > v.Suspects[j].PerParent })
	return v
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

var callsTmpl = template.Must(template.New("calls").Funcs(template.FuncMap{
	"f1": func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) },
}).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>调用统计 - pprof 中文版</title>
<style>{{.CSS}}
*{box-sizing:border-box}body{margin:0;font-size:14px}a{color:#2a66d9}
.header{display:flex;align-items:center;padding:0 1rem;gap:2px}.header .title h1 a{color:#1f2430;text-decoration:none}.menu-name{display:flex;align-items:center;justify-content:center}.menu-name a{text-decoration:none}.header .description{margin-left:auto;color:#6b7280;font-size:12px}
table{border-spacing:0;width:100%}th,td{padding:.45em .8em;text-align:right}
.pz-page{padding:20px 28px;max-width:1280px;margin:0 auto}
.pz-page h2{font-size:16px;margin:26px 0 10px}
.pz-note{background:#f7f8fb;border:1px solid var(--pz-border);border-radius:10px;padding:10px 14px;color:var(--pz-muted);font-size:13px;line-height:1.7}
.pz-warn{background:#fff4f2;border:1px solid #f5c6bd;border-radius:10px;padding:12px 16px;margin:8px 0;line-height:1.7}
.pz-warn b{color:#c0392b}
.pz-ok{color:#2e7d32}
.bar{height:8px;border-radius:4px;background:#e8ebf5;position:relative;min-width:80px}.bar i{display:block;height:100%;border-radius:4px;background:var(--pz-accent)}
tr.sus td{background:#fff4f2}
th.s{cursor:pointer}.tg{color:var(--pz-muted);user-select:none}th.s:hover{color:var(--pz-accent)}
td.l,th.l{text-align:left}
code{font-family:ui-monospace,Menlo,monospace;font-size:13px}
</style></head><body>
<div class="header"><div class="title"><h1><a href="./flamegraph">pprof</a></h1></div>
<div class="menu-item"><div class="menu-name"><a href="./top">列表</a></div></div>
<div class="menu-item"><div class="menu-name"><a href="./graph">调用图</a></div></div>
<div class="menu-item"><div class="menu-name"><a href="./flamegraph">火焰图</a></div></div>
<div class="menu-item"><div class="menu-name"><a href="./calls"><b>调用统计</b></a></div></div>
<div class="description">数据：{{.V.GenAt}}</div></div>
<div class="pz-page">
<div class="pz-note">类似 XHProf 的调用统计，由 <code>pprof-zh instrument</code> 在编译期自动插桩得到，业务代码无需手写埋点。<br>
<b>调用路径</b>：从入口到出口逐层展开，每行有调用次数、每次父调用平均次数、墙钟耗时、内存；循环里重复查询（N+1）会标红。<br>
说明：Go 无法精确到单函数的 CPU 计时，「CPU 时间 / 采样数」取自 pprof 采样，按<b>函数</b>汇总（含子调用），不区分调用路径；没有采样的函数（如纯等待 I/O）显示 “-”。“内存”是累计分配字节而非峰值，并发请求下会互相串。</div>

<h2>⚠ 疑似循环内重复调用（每次父调用平均 ≥ {{.V.Threshold}} 次）</h2>
{{range .V.Suspects}}<div class="pz-warn"><b>{{.ParentName}} → {{.Name}}</b>：平均每次 <b>{{f1 .PerParent}}</b> 次，共 {{.Calls}} 次，耗时 {{.Wall}}<br>{{.Advice}}</div>
{{else}}<div class="pz-note pz-ok">✓ 未发现平均每次调用 ≥ {{.V.Threshold}} 次的重复调用。可用 <code>?n=5</code> 调低阈值。</div>{{end}}

<h2>调用路径（入口 → 出口） <small style="font-weight:400;color:var(--pz-muted)">点击 ▾ 折叠 / 展开</small></h2>
<table id="tt"><thead><tr><th class="l">函数</th><th>调用次数</th><th>每次父调用</th><th>总耗时</th><th>占入口</th><th>自身耗时</th><th>CPU 时间</th><th>采样数</th><th>内存（含子）</th></tr></thead><tbody>
{{range .V.Tree}}<tr data-d="{{.Depth}}"{{if .Suspect}} class="sus"{{end}}><td class="l" style="padding-left:{{.Indent}}px"><span class="tg">{{if .HasKids}}▾{{else}}&nbsp;{{end}}</span> <code>{{.Name}}</code></td>
<td>{{.Calls}}</td><td>{{if .HasParent}}{{f1 .PerParent}}{{if .Suspect}} ⚠{{end}}{{else}}-{{end}}</td><td>{{.Wall}}</td><td>{{.WallPct}}</td><td>{{.Self}}</td><td>{{.CPU}}</td><td>{{.Samples}}</td><td>{{.Alloc}}</td></tr>{{else}}<tr><td class="l" colspan="9">没有调用路径数据。请确认已用 instrument 插桩构建并运行过程序。</td></tr>{{end}}</tbody></table>

<h2>函数汇总</h2>
<table id="ft"><thead><tr>
<th class="l s">函数</th><th class="s">调用次数</th><th class="s">总耗时（含子调用）</th><th class="s">自身耗时</th><th class="s">平均耗时</th><th class="s">分配内存</th><th class="l">自身占比</th></tr></thead><tbody>
{{range .V.Funcs}}<tr><td class="l"><code>{{.Name}}</code></td><td>{{.Calls}}</td><td>{{.Wall}}</td><td>{{.Self}}</td><td>{{.Av}}</td><td>{{.Alloc}}</td>
<td class="l"><div class="bar"><i style="width:{{f1 .SelfPct}}%"></i></div></td></tr>{{end}}</tbody></table>
</div>
<script>
document.querySelectorAll('#tt .tg').forEach(g=>{if(!g.textContent.trim())return;g.style.cursor='pointer';g.onclick=()=>{
const tr=g.closest('tr'),d=+tr.dataset.d,open=g.textContent=='▾';g.textContent=open?'▸':'▾';
for(let r=tr.nextElementSibling;r&&+r.dataset.d>d;r=r.nextElementSibling){r.style.display=open?'none':'';const t=r.querySelector('.tg');if(open===false&&t&&t.textContent=='▸')t.textContent='▾'}}});
// 点表头排序（数字按数值，文字按字典序）
document.querySelectorAll('table').forEach(t=>t.querySelectorAll('th.s').forEach((th,ci)=>{let asc=false;th.onclick=()=>{asc=!asc;
const rows=[...t.tBodies[0].rows];const val=r=>{const x=r.cells[ci].innerText.trim();const n=parseFloat(x);
const u=/ms$/.test(x)?1e3:/µs$/.test(x)?1:/s$/.test(x)&&!/ms$/.test(x)?1e6:/KB/.test(x)?1e3:/MB/.test(x)?1e6:/GB/.test(x)?1e9:1;return isNaN(n)?x:n*u};
rows.sort((a,b)=>{const x=val(a),y=val(b);return (typeof x=='number'&&typeof y=='number'?x-y:String(x).localeCompare(String(y)))*(asc?1:-1)});rows.forEach(r=>t.tBodies[0].appendChild(r))}}));
</script></body></html>`))

func callsHandler(path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "calls.json not readable: "+err.Error(), http.StatusNotFound)
			return
		}
		var rep tracer.Report
		if err := json.Unmarshal(b, &rep); err != nil {
			http.Error(w, "calls.json invalid: "+err.Error(), http.StatusBadRequest)
			return
		}
		th := 10
		if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n > 0 {
			th = n
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		callsTmpl.Execute(w, map[string]any{"V": buildCalls(rep, th, loadCPU(profPath)), "CSS": template.CSS(zhCSS)})
	})
}

// callsJSONHandler 输出 {符号: {calls, wall_ns}}，供「列表」页补充“调用次数”列。
func callsJSONHandler(path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path)
		var rep tracer.Report
		if err == nil {
			err = json.Unmarshal(b, &rep)
		}
		out := map[string]map[string]int64{}
		if err == nil {
			for _, f := range rep.Funcs {
				if f.Symbol != "" {
					out[f.Symbol] = map[string]int64{"calls": f.Calls, "wall_ns": f.WallNs}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
}
