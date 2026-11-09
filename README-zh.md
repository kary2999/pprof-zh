# pprof-zh：pprof Web UI 中文版

本仓库 fork 自 [google/pprof](https://github.com/google/pprof)，在不修改上游任何代码的前提下，新增一个入口 `cmd/pprof-zh`，让 pprof 的 Web 界面显示中文。

> 上游 `README.md` 保持原样；本文件只说明中文化部分。

## 效果

- 顶部菜单：视图（列表 / 调用图 / 火焰图 / 上下游 / 源码 / 反汇编）、采样类型、筛选、配置、下载、搜索框
- Top 列表表头、鼠标悬停提示、profile 详情（类型、时间、时长、采样总量）
- 保存 / 删除配置的对话框

## 原理

1. 通过 pprof 公开的 `driver.Options.HTTPServer` 钩子接管 Web 路由（保持上游 `/ui/` 前缀与仅本机访问的行为）。
2. 对每个 `text/html` 响应，在 `</body>` 前注入 `zh.js`。
3. `zh.js` 在浏览器端把界面文案翻成中文，并用 `MutationObserver` 处理动态生成的内容。

**没有收录的文案会保持英文**，不会导致页面报错。函数名、调用图节点、源码、`<pre>` 文本刻意不翻译。

## 使用说明

需要 Go 1.26+（与 `go.mod` 一致）。Graph（调用图）页面需要本机安装 graphviz：`brew install graphviz`（上游同样要求）。

### 1. 获取并编译

```bash
git clone https://github.com/kary2999/pprof-zh.git
cd pprof-zh
go build -o pprof-zh ./cmd/pprof-zh
```

> 模块路径仍是 `github.com/google/pprof`，所以不能用 `go install github.com/kary2999/pprof-zh/...@latest`，请克隆后编译。

### 2. 打开本地 profile 文件

参数与 `go tool pprof` 完全一致：

```bash
./pprof-zh -http=127.0.0.1:8080 -no_browser cmd/pprof-zh/sample/cpu.pprof
```

浏览器访问 <http://127.0.0.1:8080>。不想先编译，也可以直接运行：

```bash
go run ./cmd/pprof-zh -http=127.0.0.1:8080 -no_browser cmd/pprof-zh/sample/cpu.pprof
```

### 3. 直接分析线上服务

服务端需引入 `net/http/pprof`，然后：

```bash
# CPU，采样 30 秒
./pprof-zh -http=127.0.0.1:8080 -no_browser http://<host>:<port>/debug/pprof/profile?seconds=30

# 堆内存
./pprof-zh -http=127.0.0.1:8080 -no_browser http://<host>:<port>/debug/pprof/heap
```

### 4. 页面说明

| 页面 | 作用 |
|---|---|
| 列表（Top） | 按耗时排序的函数表：自身 = 函数本身耗时，含子调用 = 加上它调用的函数 |
| 调用图（Graph） | 调用关系有向图，节点越大越耗时（依赖 graphviz） |
| 火焰图（Flame Graph） | 横向越宽耗时越多，自上而下是调用栈 |
| 上下游（Peek） | 某函数的调用方与被调用方 |
| 源码（Source） | 带耗时标注的源码 |
| 筛选（Refine） | 聚焦 / 忽略 / 隐藏 / 显示指定函数 |

## 补充或修改翻译

编辑 [`cmd/pprof-zh/zh.js`](cmd/pprof-zh/zh.js)：

- `EXACT`：整句精确对照，如 `'Flat': '自身'`
- `RULES`：带数值的动态文案，用正则替换

改完重新编译即可（`zh.js` 通过 `go:embed` 打进二进制）。

## 已知限制

- 提示文案（鼠标悬停 `title`）目前只翻了常用的几条。
- Peek / Source 页的 `<pre>` 文本不翻译。
- 保存 / 删除配置的对话框动态文案尚未逐一验证。
- 上游升级后若新增或改动文案，需要手工补充翻译表。

## 许可证

沿用上游 [Apache-2.0](LICENSE)。
