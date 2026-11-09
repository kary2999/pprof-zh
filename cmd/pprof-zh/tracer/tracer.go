// Package tracer 是“XHProf 式”的调用统计运行时：
// 记录每条调用路径（入口 -> ... -> 函数）的调用次数、墙钟耗时（含子调用 / 自身）与累计分配字节。
//
// 业务代码不需要手写任何埋点：由 `pprof-zh instrument` 在编译期（go build -overlay）
// 自动给函数插入 `defer Enter(...)()`，源码文件不会被修改。
//
// 限制（务必知晓）：
//   - 只用于开发 / 测试构建，每次函数调用都有额外开销，且被插样的函数无法内联；
//   - 父子关系按 goroutine 追踪：go func(){} 里的调用不会挂到发起它的请求下；
//   - 分配字节取自进程级计数器，并发请求下会互相串，单请求串行最准；
//   - 没有 per-function CPU 时间（Go 做不到），CPU 由 pprof 采样按函数符号对应。
package tracer

import (
	"encoding/json"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type frame struct {
	name, symbol, path string
	t0                 time.Time
	a0                 int64
	childNs            int64
}

type gstate struct{ stack []*frame }

type fstat struct {
	calls, wallNs, selfNs, allocB int64
	symbol                        string
}
type pstat struct{ calls, wallNs, selfNs, allocB int64 }

const maxDepth = 200

var (
	gs      sync.Map // goroutine id -> *gstate
	mu      sync.Mutex
	funcs   = map[string]*fstat{}
	paths   = map[string]*pstat{}
	dirty   atomic.Bool
	active  atomic.Int32    // 当前处于入口之内的 goroutine 数；为 0 时非入口函数直接返回，几乎零开销
	entries map[string]bool // 由生成的 zz_entries.go 赋值；为空表示不限入口
	outFile = "calls.json"
)

func init() {
	if p := os.Getenv("PPROF_ZH_CALLS_OUT"); p != "" {
		outFile = p
	}
	go func() { // 定期落盘，进程被 kill 也不丢数据
		for range time.Tick(2 * time.Second) {
			if dirty.Swap(false) {
				Dump(outFile)
			}
		}
	}()
}

func noop() {}

func goid() uint64 {
	var b [48]byte
	n := runtime.Stack(b[:], false) // "goroutine 123 [running]:..."
	var id uint64
	for _, c := range b[10:n] {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
	}
	return id
}

func allocBytes() int64 {
	s := [1]metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(s[:])
	if s[0].Value.Kind() == metrics.KindUint64 {
		return int64(s[0].Value.Uint64())
	}
	return 0
}

// Enter 由自动插入的代码调用：defer Enter("pkg.Func", "import/path.Func")()。
func Enter(name, symbol string) func() {
	if len(entries) > 0 && active.Load() == 0 && !entries[name] {
		return noop
	}
	id := goid()
	var st *gstate
	if v, ok := gs.Load(id); ok {
		st = v.(*gstate)
	}
	if st == nil {
		if len(entries) > 0 && !entries[name] {
			return noop // 不在任何入口之内，忽略
		}
		st = &gstate{}
		gs.Store(id, st)
		active.Add(1)
	}
	if len(st.stack) >= maxDepth {
		return noop
	}
	fr := &frame{name: name, symbol: symbol, path: name, a0: allocBytes()}
	if n := len(st.stack); n > 0 {
		fr.path = st.stack[n-1].path + "\x00" + name
	}
	st.stack = append(st.stack, fr)
	fr.t0 = time.Now()
	return func() { exit(id, st, fr) }
}

func exit(id uint64, st *gstate, fr *frame) {
	d := time.Since(fr.t0).Nanoseconds()
	a := allocBytes() - fr.a0
	// 弹出到 fr 为止（panic 等场景下可能有未配对的帧）
	for i := len(st.stack) - 1; i >= 0; i-- {
		if st.stack[i] == fr {
			st.stack = st.stack[:i]
			break
		}
	}
	self := d - fr.childNs
	if self < 0 {
		self = 0
	}
	if n := len(st.stack); n > 0 {
		st.stack[n-1].childNs += d
	} else {
		gs.Delete(id)
		active.Add(-1)
	}
	mu.Lock()
	f := funcs[fr.name]
	if f == nil {
		f = &fstat{symbol: fr.symbol}
		funcs[fr.name] = f
	}
	f.calls++
	f.wallNs += d
	f.selfNs += self
	f.allocB += a
	p := paths[fr.path]
	if p == nil {
		p = &pstat{}
		paths[fr.path] = p
	}
	p.calls++
	p.wallNs += d
	p.selfNs += self
	p.allocB += a
	mu.Unlock()
	dirty.Store(true)
}

// Report 是导出的 JSON 结构。
type Report struct {
	GeneratedAt string     `json:"generated_at"`
	Funcs       []FuncStat `json:"funcs"`
	Paths       []PathStat `json:"paths"`
}

type FuncStat struct {
	Name       string `json:"name"`
	Calls      int64  `json:"calls"`
	WallNs     int64  `json:"wall_ns"` // 含子调用
	SelfNs     int64  `json:"self_ns"` // 自身
	AllocBytes int64  `json:"alloc_bytes"`
	Symbol     string `json:"symbol,omitempty"` // Go 符号名，用于匹配 pprof 的 CPU 采样
}

// PathStat 是“从入口到该函数”的完整调用路径的聚合，用于还原调用树。
type PathStat struct {
	Path       []string `json:"path"`
	Calls      int64    `json:"calls"`
	WallNs     int64    `json:"wall_ns"`
	SelfNs     int64    `json:"self_ns"`
	AllocBytes int64    `json:"alloc_bytes"`
}

// Snapshot 返回当前统计快照。
func Snapshot() Report {
	mu.Lock()
	defer mu.Unlock()
	r := Report{GeneratedAt: time.Now().Format(time.RFC3339)}
	for n, f := range funcs {
		r.Funcs = append(r.Funcs, FuncStat{n, f.calls, f.wallNs, f.selfNs, f.allocB, f.symbol})
	}
	for k, p := range paths {
		r.Paths = append(r.Paths, PathStat{strings.Split(k, "\x00"), p.calls, p.wallNs, p.selfNs, p.allocB})
	}
	sort.Slice(r.Funcs, func(i, j int) bool { return r.Funcs[i].WallNs > r.Funcs[j].WallNs })
	sort.Slice(r.Paths, func(i, j int) bool {
		return strings.Join(r.Paths[i].Path, "\x00") < strings.Join(r.Paths[j].Path, "\x00")
	})
	return r
}

// Dump 把统计原子地写入 JSON 文件。
func Dump(path string) error {
	b, err := json.MarshalIndent(Snapshot(), "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Flush 立即落盘（插样工具会在 main.main 里自动 defer 它）。
func Flush() { Dump(outFile) }
