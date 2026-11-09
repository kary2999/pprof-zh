// pprof-zh：pprof Web UI 中文化。
//
// 做法：通过 driver.Options.HTTPServer 接管路由，对 HTML 响应注入 zh.css / zh.js，
// 由前端脚本把界面文案翻成中文（含 MutationObserver 处理动态内容）；
// 对错误响应（4xx/5xx 纯文本）渲染为带解决建议的中文错误页；
// 终端里的提示也通过自定义 UI 翻译为中文。
// 用法与 `go tool pprof` 完全一致，例如：
//
//	go run ./cmd/pprof-zh -http=127.0.0.1:8078 -no_browser cpu.pprof
package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/google/pprof/driver"
)

//go:embed zh.js
var zhJS []byte

//go:embed zh.css
var zhCSS []byte

var callsPath string

func main() {
	if len(os.Args) > 1 && os.Args[1] == "instrument" {
		if err := runInstrument(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "错误：", err)
			os.Exit(1)
		}
		return
	}
	callsPath = callsFile()
	opts := &driver.Options{HTTPServer: serve, UI: &zhUI{r: bufio.NewReader(os.Stdin)}}
	if err := driver.PProf(opts); err != nil {
		fmt.Fprintln(os.Stderr, "pprof-zh 出错：", translateTerminal(err.Error()))
		os.Exit(1)
	}
}

// ---------- 终端提示中文化 ----------

type zhUI struct{ r *bufio.Reader }

func (u *zhUI) ReadLine(prompt string) (string, error) {
	os.Stdout.WriteString(prompt)
	return u.r.ReadString('\n')
}
func (u *zhUI) Print(a ...interface{})              { u.out(a) }
func (u *zhUI) PrintErr(a ...interface{})           { u.out(a) }
func (u *zhUI) IsTerminal() bool                    { return false }
func (u *zhUI) WantBrowser() bool                   { return true }
func (u *zhUI) SetAutoComplete(func(string) string) {}
func (u *zhUI) out(a []interface{}) {
	t := translateTerminal(fmt.Sprint(a...))
	if !strings.HasSuffix(t, "\n") {
		t += "\n"
	}
	os.Stderr.WriteString(t)
}

var termRules = []struct {
	re *regexp.Regexp
	to string
}{
	{regexp.MustCompile(`Serving web UI on (\S+)`), "Web 界面已启动：$1（Ctrl+C 退出）"},
	{regexp.MustCompile(`Failed to execute dot\. Is Graphviz installed\?`), "无法执行 dot：请确认已安装 Graphviz（macOS: brew install graphviz）"},
	{regexp.MustCompile(`exec: "dot": executable file not found in \$PATH`), "在 PATH 中找不到 dot 命令"},
	{regexp.MustCompile(`no matches found for regexp:? (.+)`), "没有匹配的函数（正则：$1）"},
	{regexp.MustCompile(`Fetched (\d+) source profiles out of (\d+)`), "已读取 $1/$2 个 profile 文件"},
	{regexp.MustCompile(`parsing profile: unrecognized profile format`), "解析 profile 失败：文件格式无法识别（请传入 .pprof 文件，不要传二进制）"},
	{regexp.MustCompile(`no such file or directory`), "文件或目录不存在"},
	{regexp.MustCompile(`could not split http address`), "监听地址格式不对（应为 host:port，如 127.0.0.1:8078）"},
	{regexp.MustCompile(`address already in use`), "端口已被占用，请换一个端口（-http=127.0.0.1:端口）"},
}

func translateTerminal(s string) string {
	for _, r := range termRules {
		s = r.re.ReplaceAllString(s, r.to)
	}
	return s
}

// ---------- Web 服务 ----------

// serve 仿照 pprof 默认 web 服务（/ui/ 前缀 + 仅本机访问），并给每个 handler 包上中文化层。
func serve(args *driver.HTTPServerArgs) error {
	ln, err := net.Listen("tcp", args.Hostport)
	if err != nil {
		return err
	}
	local := args.Host == "localhost" || args.Host == "127.0.0.1" || args.Host == "::1"
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil || (host != "127.0.0.1" && host != "::1") {
				localize(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "permission denied", http.StatusForbidden)
				})).ServeHTTP(w, r)
				return
			}
		}
		next := args.Handlers[r.URL.Path]
		if r.URL.Path == "/calls.json" && callsPath != "" {
			callsJSONHandler(callsPath).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/calls" && callsPath != "" {
			callsHandler(callsPath).ServeHTTP(w, r)
			return
		}
		if next == nil {
			next = http.DefaultServeMux
		}
		localize(next).ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	mux.Handle("/ui/", http.StripPrefix("/ui", h))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u := "/ui" + r.URL.Path
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, u, http.StatusTemporaryRedirect)
	})
	return (&http.Server{Handler: mux}).Serve(ln)
}

type capture struct {
	http.ResponseWriter
	buf    bytes.Buffer
	status int
}

func (c *capture) WriteHeader(code int) { c.status = code }
func (c *capture) Write(b []byte) (int, error) {
	return c.buf.Write(b)
}

// localize 缓冲 handler 输出：HTML 注入样式与翻译脚本；错误响应换成中文错误页。
func localize(next http.Handler) http.Handler {
	flag := ""
	if callsPath != "" {
		flag = "window.PZ_CALLS=true;"
	}
	if os.Getenv("PPROF_ZH_FULL") == "1" {
		flag += "window.PZ_FULL=true;"
	}
	inject := []byte("<style>" + string(zhCSS) + "</style><script>" + flag + string(zhJS) + "</script></body>")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &capture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(c, r)
		body := c.buf.Bytes()
		ct := w.Header().Get("Content-Type")
		switch {
		case c.status >= 400 && strings.HasPrefix(ct, "text/plain"):
			body = errorPage(c.status, strings.TrimSpace(string(body)))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Del("Content-Length")
		case strings.HasPrefix(ct, "text/html"):
			if i := bytes.LastIndex(body, []byte("</body>")); i >= 0 {
				body = append(append(append([]byte{}, body[:i]...), inject...), body[i+len("</body>"):]...)
			}
			w.Header().Del("Content-Length")
		}
		w.WriteHeader(c.status)
		w.Write(body)
	})
}

var errRules = []struct{ match, title, hint string }{
	{"Could not execute dot", "无法生成调用图：未检测到 Graphviz",
		"调用图依赖 Graphviz 提供的 dot 命令。\n\nmacOS:          brew install graphviz\nUbuntu/Debian:  sudo apt install graphviz\nCentOS/RHEL:    sudo yum install graphviz\n\n安装后重启 pprof-zh 即可。\n不想装的话，火焰图和列表视图不需要 Graphviz。"},
	{"no matches found for regexp", "没有匹配的函数或源码",
		"可能原因：\n1. 正则写错了，或该函数不在当前 profile / 筛选范围内；\n2. 源码视图需要能在本机按 profile 记录的路径找到源文件（编译机与当前机器路径不同时，可加 -source_path / -trim_path 参数）；\n3. 标准库、runtime 的源码在没有 GOROOT 时也找不到。"},
	{"permission denied", "仅允许本机访问",
		"为安全起见，监听 localhost / 127.0.0.1 时只接受本机请求。\n如需远程访问，请改用 -http=0.0.0.0:端口（注意网络暴露风险）。"},
	{"internal template error", "页面渲染失败", "这通常是 pprof 内部问题，终端里有详细错误，请带上它反馈。"},
	{"objdump", "反汇编失败：找不到 objdump",
		"反汇编视图依赖 objdump 工具和对应的可执行文件。\nmacOS 可 brew install binutils；Linux 通常安装 binutils 即可。"},
}

func errorPage(code int, raw string) []byte {
	title, hint := fmt.Sprintf("请求失败（HTTP %d）", code), "请检查参数是否正确；原始错误信息见下方。"
	for _, r := range errRules {
		if strings.Contains(raw, r.match) {
			title, hint = r.title, r.hint
			break
		}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>出错了 - pprof 中文版</title>
<style>body{margin:0;font-family:-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;color:#1f2430;background:#fff}%s</style></head><body>
<div class="pz-err"><h1>%s</h1><div class="code">HTTP %d</div><pre>%s</pre>
<div class="nav"><a href="./flamegraph">火焰图</a><a href="./top">列表</a><a href="javascript:history.back()">返回上一页</a></div>
<div class="raw">原始信息：%s</div></div></body></html>`,
		zhCSS, html.EscapeString(title), code, html.EscapeString(hint), html.EscapeString(raw))
	return b.Bytes()
}
