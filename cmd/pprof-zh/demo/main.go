// demo：生成一组有真实调用层次的 profile，用于演示 pprof-zh 各个视图。
//
//	go run ./cmd/pprof-zh/demo ./cmd/pprof-zh/demo/out
//
// 产出：cpu / heap / goroutine / mutex / block 五个 .pprof 文件。
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"sync"
	"time"
)

var keep [][]byte // 让堆里留下可见的 inuse 数据

func main() {
	out := "out"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	must(os.MkdirAll(out, 0o755))
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)

	cpu, err := os.Create(filepath.Join(out, "cpu.pprof"))
	must(err)
	must(pprof.StartCPUProfile(cpu))

	handleRequests() // 模拟业务：多个“接口”混合负载
	contention()     // 锁竞争 + channel 阻塞
	apiScenario()    // N+1 循环查询 vs 批量查询（调用统计页面演示）

	pprof.StopCPUProfile()
	cpu.Close()

	stop := make(chan struct{})
	for i := 0; i < 20; i++ { // 留一批存活 goroutine，goroutine 视图才有内容
		go workerIdle(stop)
	}
	time.Sleep(100 * time.Millisecond)

	write(out, "heap", "heap")
	write(out, "goroutine", "goroutine")
	write(out, "mutex", "mutex")
	write(out, "block", "block")
	close(stop)
	fmt.Println("已生成到", out)
}

// ---- 业务模拟：下单 / 行情 / 清算 三类接口 ----

func handleRequests() {
	end := time.Now().Add(4 * time.Second)
	for time.Now().Before(end) {
		serve(placeOrderHandler)
		serve(queryTickerHandler)
		serve(settleHandler)
	}
}

// serve 模拟一次 HTTP 请求打到 handler。instrument 会按函数签名自动把
// func(http.ResponseWriter, *http.Request) 识别为入口，业务代码里不需要任何标记。
func serve(h http.HandlerFunc) {
	h(httptest.NewRecorder(), httptest.NewRequest("GET", "/demo", nil))
}

func placeOrderHandler(w http.ResponseWriter, r *http.Request)  { placeOrder(); w.Write([]byte("ok")) }
func queryTickerHandler(w http.ResponseWriter, r *http.Request) { queryTicker(); w.Write([]byte("ok")) }
func settleHandler(w http.ResponseWriter, r *http.Request)      { settle(); w.Write([]byte("ok")) }

func placeOrder() { // 校验 -> 序列化 -> 签名 -> 落盘压缩
	o := genOrders(200)
	validate(o)
	b := encodeJSON(o)
	signPayload(b)
	compress(b)
}

func queryTicker() { // 排序 + 内存分配
	xs := make([]float64, 20000)
	for i := range xs {
		xs[i] = rand.Float64()
	}
	sort.Float64s(xs)
	keep = append(keep, make([]byte, 64<<10))
	if len(keep) > 200 {
		keep = keep[100:]
	}
}

func settle() { // 纯计算，递归
	fib(12)
}

type order struct {
	ID    int
	Price float64
	Qty   float64
	Tags  []string
}

func genOrders(n int) []order {
	r := make([]order, n)
	for i := range r {
		r[i] = order{i, rand.Float64() * 100, rand.Float64() * 10, []string{"btc", "usdt", "perp"}}
	}
	return r
}

func validate(o []order) {
	for _, x := range o {
		if x.Price < 0 || x.Qty < 0 {
			panic("bad order")
		}
	}
}

func encodeJSON(o []order) []byte {
	b, _ := json.Marshal(o)
	return b
}

func signPayload(b []byte) {
	for i := 0; i < 50; i++ {
		sum := sha256.Sum256(b)
		b = append(b[:0:0], sum[:]...)
	}
}

func compress(b []byte) {
	noise := make([]byte, 128<<10)
	rand.Read(noise)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(noise)
	zw.Write(b)
	zw.Close()
}

func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

// ---- 锁竞争与阻塞 ----

func contention() {
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				lockedWork(&mu)
			}
		}()
	}
	wg.Wait()

	ch := make(chan int)
	go func() {
		for i := 0; i < 20; i++ {
			time.Sleep(5 * time.Millisecond)
			ch <- i
		}
		close(ch)
	}()
	for range ch { // 等待生产者，形成 block
	}
}

func lockedWork(mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	time.Sleep(200 * time.Microsecond)
}

func workerIdle(stop <-chan struct{}) { <-stop }

func write(dir, file, name string) {
	f, err := os.Create(filepath.Join(dir, file+".pprof"))
	must(err)
	defer f.Close()
	runtime.GC()
	must(pprof.Lookup(name).WriteTo(f, 0))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ---- 调用统计演示：同一个“订单列表”接口的两种写法 ----
//
// 坏写法：对每个订单在循环里单独查一次交易对信息（N+1，100 条订单 = 100 次查询）。
// 好写法：先收集 id，外层一次批量查询。
// pprof 采样只能看到“耗时”，看不出“被调用了 100 次”；tracer 的调用次数能直接暴露。

type symbolInfo struct {
	Name     string
	Contract float64
}

func apiScenario() {
	for i := 0; i < 20; i++ {
		serve(listOrdersBad)
		serve(listOrdersGood)
	}
}

func listOrdersBad(w http.ResponseWriter, r *http.Request) { // 接口入口：订单列表（循环查询）
	orders := dbQueryOrders(100)
	for _, o := range orders {
		_ = dbGetSymbolInfo(o.ID % 10) // 每个订单查一次，典型 N+1
	}
}

func listOrdersGood(w http.ResponseWriter, r *http.Request) { // 接口入口：订单列表（批量查询）
	orders := dbQueryOrders(100)
	ids := make([]int, 0, len(orders))
	for _, o := range orders {
		ids = append(ids, o.ID%10)
	}
	_ = dbGetSymbolInfoBatch(ids) // 外层只查一次
}

func dbQueryOrders(n int) []order {
	time.Sleep(400 * time.Microsecond) // 模拟网络 + 数据库耗时
	return genOrders(n)
}

func dbGetSymbolInfo(id int) symbolInfo {
	time.Sleep(300 * time.Microsecond)
	return symbolInfo{Name: fmt.Sprintf("SYM-%d", id), Contract: 0.01}
}

func dbGetSymbolInfoBatch(ids []int) []symbolInfo {
	time.Sleep(500 * time.Microsecond)
	r := make([]symbolInfo, len(ids))
	for i, id := range ids {
		r[i] = symbolInfo{Name: fmt.Sprintf("SYM-%d", id), Contract: 0.01}
	}
	return r
}
