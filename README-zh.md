# pprof-zh：pprof Web UI 中文版 + 调用统计

本仓库 fork 自 [google/pprof](https://github.com/google/pprof)，**不修改上游任何代码**，新增入口 `cmd/pprof-zh`，提供：

1. **中文界面**：pprof 的 Web 界面全部中文化，并优化了样式、报错提示、菜单与导航。
2. **调用统计（XHProf 式）**：编译期**自动插桩**（`pprof-zh instrument`，业务代码零埋点）统计每条调用路径的**调用次数 / 耗时 / CPU / 分配内存**，自动发现“循环里反复查库（N+1）”。pprof 是采样，只能告诉你“哪里慢”，告诉不了你“这个函数被调了 100 次”。

> 上游 `README.md` 保持原样；本文件只说明本仓库新增的部分。

**目录**

- [一、5 分钟跑通演示（demo）](#一5-分钟跑通演示demo)
- [二、在你自己的项目里使用（自动插桩）](#二在你自己的项目里使用自动插桩)
- [三、分析本地 / 线上 profile](#三分析本地--线上-profile)
- [四、界面功能说明](#四界面功能说明)
- [五、原理、定制与限制](#五原理定制与限制)

---

## 一、5 分钟跑通演示（demo）

### 1. 准备环境

| 依赖 | 说明 |
|---|---|
| Go 1.26+ | 与 `go.mod` 一致 |
| Graphviz | 「调用图」视图需要，`brew install graphviz`（macOS）/ `sudo apt install graphviz`（Ubuntu）。没有它只是调用图不可用，其余视图正常 |

```bash
git clone https://github.com/kary2999/pprof-zh.git
cd pprof-zh
```

### 2. 一键启动

```bash
cmd/pprof-zh/demo/run.sh            # 默认看 CPU profile
cmd/pprof-zh/demo/run.sh heap       # 可选：cpu / heap / goroutine / mutex / block
```

脚本依次做了四件事：编译 demo → 运行 demo 生成 profile（约 6 秒）→ 编译 pprof-zh → 启动 Web 服务。看到下面两行说明成功，浏览器打开 <http://127.0.0.1:8078>：

```
打开 http://127.0.0.1:8078
Web 界面已启动：http://127.0.0.1:8078（Ctrl+C 退出）
```

> 想换端口：编辑 `cmd/pprof-zh/demo/run.sh` 末尾的 `-http=127.0.0.1:8078`。
> 若提示“端口已被占用”，通常是上一次启动的服务还在，先执行 `pkill -f "demo/out/pprof-zh"`。

### 3. demo 里模拟了什么

源码在 [`cmd/pprof-zh/demo/main.go`](cmd/pprof-zh/demo/main.go)，产物在 `cmd/pprof-zh/demo/out/`（已 gitignore）：

| 场景 | 内容 | 在哪里看 |
|---|---|---|
| 业务混合负载 | 下单（校验 → JSON → 签名 → gzip）、行情（排序 + 内存分配）、清算（递归计算） | CPU：列表 / 调用图 / 火焰图 |
| 锁竞争与阻塞 | 8 个 goroutine 争一把锁 + channel 等待 | `run.sh mutex` / `run.sh block` |
| 内存与协程 | 持续分配、20 个存活 goroutine | `run.sh heap` / `run.sh goroutine` |
| **N+1 循环查询** | 同一个“订单列表”接口的两种写法，各调用 20 次 | **视图 → 调用统计** |

### 4. 重点演示：发现“循环里查 100 次”

demo 里的两个接口做同一件事——列出 100 条订单并补全交易对信息：

```go
// 坏写法：每个订单单独查一次（N+1）
for _, o := range orders {
    _ = dbGetSymbolInfo(o.ID%10)             // 100 条订单 = 100 次查询
}

// 好写法：先收集 id，外层只查一次
_ = dbGetSymbolInfoBatch(ids)                 // 1 次批量查询
```

打开页面顶部菜单 **视图 → 调用统计**（或直接访问 `/ui/calls`），典型结果（数值因机器而异）：

```
⚠ 疑似循环内重复调用（每次父调用平均 ≥ 10 次）
main.listOrdersBad → main.dbGetSymbolInfo：平均每次 100.0 次，共 2000 次，耗时 700.87ms

调用路径                          调用次数  每次父调用   总耗时    占入口   自身耗时
▾ main.listOrdersBad                  20       -      732.10ms   100.0%   21.64ms
    main.dbGetSymbolInfo            2000   100.0 ⚠  700.87ms    95.7%  700.87ms
  ▾ main.dbQueryOrders                20     1.0        9.59ms    1.3%    9.42ms
      main.genOrders                  20     1.0       165µs     0.0%    165µs     CPU 20ms / 2 个采样
▾ main.listOrdersGood                 20       -       21.98ms   100.0%   392µs
    main.dbGetSymbolInfoBatch         20     1.0      11.83ms    53.8%   11.83ms
```

demo 的业务代码（[`demo/main.go`](cmd/pprof-zh/demo/main.go)）里**没有任何埋点**，也没有手写入口：`run.sh` 执行 `pprof-zh instrument -dir ./cmd/pprof-zh/demo`，工具按签名把 5 个 `func(http.ResponseWriter, *http.Request)` 自动识别为入口，再用 overlay 编译。注意：demo 的 CPU 图也是插桩后的程序跑出来的，每次函数调用都有额外开销，数值会比不插桩时偏大。

怎么读：

- 两个接口返回的结果一样，但循环写法**总耗时是批量写法的 30 多倍**，原因就是 `db.GetSymbolInfo` 被调了 2000 次（20 次请求 × 100 次）。
- 页面顶部的红色告警和路径树里的红行按“**每次父调用平均调用子函数的次数 ≥ 阈值**”判定，默认阈值 10，可用 `/ui/calls?n=5` 调整。
- 在 pprof 的 CPU 火焰图里**看不到**这个问题：这些调用大部分时间在等待（demo 里用 `sleep` 模拟网络 / 数据库往返），不占 CPU，采样抓不到——这正是需要调用统计的原因。
- 路径树下方还有“函数汇总”表，表头可点击排序。

### 5. 其他视图怎么看

在 CPU profile 下依次打开：**列表**（耗时 Top 函数）→ **火焰图**（横向越宽越耗时）→ **调用图**（节点越大越耗时，滚轮缩放、拖拽平移，点节点选中后用「筛选」菜单聚焦 / 隐藏）。上下游 / 源码 / 反汇编默认隐藏（它们需要源文件或 objdump，通常没有输出），需要时设置环境变量 `PPROF_ZH_FULL=1` 再启动即可恢复。每个视图的含义见[第四节](#四界面功能说明)。

---

## 二、在你自己的项目里使用（自动插桩）

**不需要在业务代码里手写任何埋点。** `pprof-zh instrument` 用 Go 的语法树找出目录下所有函数，在“带埋点的副本”里为每个函数插入一行 `defer Enter(...)()`，再通过 `go build -overlay` 编译。**你的源码文件一行都不会被改**，不想要埋点时用普通 `go build` 即可。插入的语句和 import 都放在原有行内，所以栈信息里的行号不变。

### 1. 生成插桩副本

**命令格式**（参数位置固定：子命令 `instrument` 在最前，后面全是 `-参数=值`，顺序随意）：

```
pprof-zh instrument  [-dir 目录]  [-entry 额外入口]  [-skip 跳过路径]  [-out 输出目录]  [-auto-entry=true]
```

最常见的用法——**什么参数都不用写**，在你项目的根目录（`go.mod` 所在目录）执行：

```bash
cd /你的项目根目录
/path/to/pprof-zh instrument -dir ./internal
```

它会打印识别到的入口、生成的文件数，并告诉你下一步的编译命令。

| 参数 | 是否必填 | 含义 |
|---|---|---|
| `-dir` | 否，默认当前目录 | 要插桩的目录（递归）。只插这个目录里的函数，标准库、第三方库不碰。必须位于某个 go module 内（会向上找 `go.mod`） |
| `-entry` | 否 | **额外**入口，逗号分隔，格式 `包名.函数` 或 `包名.类型.方法`，如 `job.Daily,consumer.Handle`。HTTP handler 已自动识别，一般不用填 |
| `-auto-entry` | 否，默认 `true` | 按函数签名自动识别 HTTP 入口：参数里有 `*http.Request` 或 `*gin.Context` 的函数。`-auto-entry=false` 可关闭 |
| `-skip` | 否 | 额外跳过的路径片段，逗号分隔，如 `-skip=mock,legacy` |
| `-out` | 否，默认 `.pprof-zh-overlay` | 输出目录，里面有 `overlay.json` |

**入口是什么**：一次请求从哪个函数开始算。只有进入入口之后的调用才会被统计，入口之外的函数几乎没有开销。没识别到任何入口时，会把每个 goroutine 最外层的函数都当入口（开销很大，不建议）。

**哪些文件会被跳过**：`_test.go`、`.pb.go`、`*_gen.go`、文件头含 `Code generated` 的、cgo 文件、`vendor/`、`testdata/`、以 `.` 或 `_` 开头的目录。

**识别不到入口的情况**：匿名函数形式的 handler（`r.GET("/x", func(c *gin.Context){...})`）是闭包，不会被识别；请改成具名函数，或用 `-entry` 指定它外层的函数。gRPC、消息消费者、定时任务的签名不同，用 `-entry` 指定。

### 2. 用 overlay 编译并运行

```bash
go build -overlay=$PWD/.pprof-zh-overlay/overlay.json -o app ./cmd/yourapp
PPROF_ZH_CALLS_OUT=./calls.json ./app
```

- `-overlay=` 必须是 `overlay.json` 的**绝对路径**；`-o app ./cmd/yourapp` 换成你平时的构建目标。
- `PPROF_ZH_CALLS_OUT` 指定统计文件写到哪里，不设则写到当前目录的 `calls.json`。
- 运行时每 2 秒自动写一次（有新数据才写）；`main.main` 正常返回时也会写一次，进程被 kill 最多丢 2 秒数据。
- 不想要埋点时，用普通 `go build` 即可，源码没有被改过。

### 3. 用 pprof-zh 查看

```bash
# 同时带上 CPU profile，调用路径里才有“CPU 时间 / 采样数”两列
curl -o cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=30"
PPROF_ZH_CALLS=./calls.json pprof-zh -http=127.0.0.1:8078 -no_browser cpu.pprof
```

不设 `PPROF_ZH_CALLS` 时，会取 `.pprof` 参数同目录的 `calls.json`。打开 **视图 → 调用统计**。

### 4. 页面怎么读

- **「列表」页同样有“调用次数”列**：只有被插桩的函数有值，标准库等显示 “-”。
- **调用路径（入口 → 出口）**：树形展开每条路径，列有调用次数、每次父调用平均次数、总耗时、占入口比例、自身耗时、CPU 时间、采样数、内存。点 ▾ 折叠。
- **N+1 告警**：每次父调用平均调用子函数 ≥ 10 次的行标红，阈值可用 `/ui/calls?n=5` 调整。
- **CPU 时间 / 采样数**同时显示：CPU 取自 pprof 采样，按**函数**汇总（含子调用），同一个函数出现在多条路径上时数值相同；没有采样的函数（等待 I/O 等）显示 “-”。

### 开销与限制（务必知晓）

- **只用于开发 / 测试构建，不要上生产。** 每次函数调用都要读取一次 goroutine id、分配计数和时间，并加锁累加；插入的 `defer` 还会让这些函数**无法被内联**，小函数的耗时会比真实值偏大。**我没有做过基准测试**。
- 指定入口后，入口之外的函数在“没有任何请求在处理”时几乎零开销；但只要有请求在处理，所有并发 goroutine 里的函数都要多做一次 goroutine id 判断。
- **父子关系按 goroutine 追踪**：入口里新开的 `go func(){...}` 里的调用**不会**挂到发起它的请求下。
- **只统计你指定目录下的函数**：第三方库（如 gorm、redis 客户端）内部不可见，你只能看到自己代码调用它的那一层；需要的话可以把相应源码目录用 `-dir` 单独再插桩（须在同一 module 内）。
- 匿名函数（闭包）不单独统计，耗时算在外层函数里。
- “分配内存”是累计分配字节，不是峰值；取自进程级计数器，并发请求下会互相串，单请求串行场景最准。
- 递归调用调用栈深度超过 200 层后不再统计。
- **没有单函数的精确 CPU 计时**：Go 做不到，CPU 只能靠 pprof 采样近似。

---

## 三、分析本地 / 线上 profile

### 1. 编译

```bash
go build -o pprof-zh ./cmd/pprof-zh
```

> 模块路径仍是 `github.com/google/pprof`，所以不能 `go install github.com/kary2999/pprof-zh/...@latest`，请克隆后编译。也可以不编译，直接 `go run ./cmd/pprof-zh <参数>`。

### 2. 打开本地 profile 文件

参数与 `go tool pprof` 完全一致：

```bash
./pprof-zh -http=127.0.0.1:8078 -no_browser cmd/pprof-zh/demo/out/cpu.pprof
```

### 3. 直接分析线上服务

服务端先引入 `net/http/pprof`（注意不要把 `/debug/pprof` 暴露到公网）：

```go
import _ "net/http/pprof"
```

然后：

```bash
# CPU，采样 30 秒
./pprof-zh -http=127.0.0.1:8078 -no_browser "http://<host>:<port>/debug/pprof/profile?seconds=30"

# 堆内存 / 协程 / 锁竞争 / 阻塞
./pprof-zh -http=127.0.0.1:8078 -no_browser "http://<host>:<port>/debug/pprof/heap"
./pprof-zh -http=127.0.0.1:8078 -no_browser "http://<host>:<port>/debug/pprof/goroutine"
```

> mutex、block 默认不采集，需要在程序里调用 `runtime.SetMutexProfileFraction(1)` / `runtime.SetBlockProfileRate(1)`。

---

## 四、界面功能说明

### 视图

| 页面 | 作用 |
|---|---|
| 列表（Top） | 按耗时排序的函数表：**自身** = 函数本身耗时，**含子调用** = 加上它调用的函数 |
| 调用图（Graph） | 调用关系有向图，节点越大越耗时（依赖 Graphviz） |
| 火焰图（Flame Graph） | 横向越宽耗时越多，自上而下是调用栈 |
| 上下游（Peek） | 某函数的调用方与被调用方 |
| 源码（Source） | 带耗时标注的源码（要求 profile 里记录的源文件路径在本机存在，所以 profile 请在本机生成） |
| 反汇编（Disassemble） | 带采样标注的汇编，需要 objdump |
| **调用统计** | 本仓库新增：调用次数 / 耗时 / 内存 / N+1 告警（需先导出 `calls.json`） |

### 导航与操作

- 顶部工具条：`← 后退`、`⤢ 适应屏幕`（恢复调用图缩放）、`✕ 清除选中`（也可按 Esc）、当前筛选条件标签（点 ✕ 单独移除）、`重置全部筛选`。
- 下拉菜单：再点一次菜单标题、按 Esc、点空白处、鼠标移出 0.5 秒，都会收起（上游原版再点标题不会收起）。
- 调用图：滚轮缩放、拖拽平移、点节点选中，再用「筛选」菜单聚焦 / 忽略 / 隐藏 / 显示。

### 友好的错误提示

- 网页：4xx/5xx 的英文纯文本会换成中文错误页，并给出解决建议（缺少 Graphviz、正则无匹配、非本机访问被拒等）。
- 终端：常见提示（服务启动、dot 缺失、端口占用、文件格式错误）同样输出中文。
- 未收录的错误原样显示在页面「原始信息」里，不会丢失。

---

## 五、原理、定制与限制

### 原理

1. 通过 pprof 公开的 `driver.Options.HTTPServer` 钩子接管 Web 路由（保持上游 `/ui/` 前缀与“仅本机访问”的行为）。
2. 对每个 `text/html` 响应，在 `</body>` 前注入 `zh.css` 与 `zh.js`；`zh.js` 在浏览器端翻译文案，并用 `MutationObserver` 处理动态内容。
3. 4xx/5xx 纯文本错误在服务端换成中文错误页；`/calls` 由本仓库自己的处理器渲染。

**没有收录的文案保持英文**，不会导致页面报错；函数名、调用图节点、源码、`<pre>` 文本刻意不翻译。

### 补充或修改翻译 / 样式

- 翻译：编辑 [`cmd/pprof-zh/zh.js`](cmd/pprof-zh/zh.js) 的 `EXACT`（整句对照）和 `RULES`（带数值的正则替换）。
- 样式：编辑 [`cmd/pprof-zh/zh.css`](cmd/pprof-zh/zh.css)。
- 改完重新编译即可（二者通过 `go:embed` 打进二进制）。

### 已知限制

- 悬停提示（`title`）只翻了常用的几条；调用图（SVG）内的函数名、数值不翻译。
- Peek / Source 页的 `<pre>` 文本不翻译。
- 保存 / 删除配置的对话框动态文案尚未逐一验证。
- 上游升级后若新增或改动文案，需要手工补充翻译表。

## 许可证

沿用上游 [Apache-2.0](LICENSE)。
