// pprof-zh：pprof Web UI 漢化原型。
//
// 做法：通過 driver.Options.HTTPServer 接管路由，對 HTML 響應注入 zh.js，
// 由前端腳本把介面文案翻成中文（含 MutationObserver 處理動態內容）。
// 用法與 `go tool pprof` 完全一致，例如：
//
//	go run ./cmd/pprof-zh -http=127.0.0.1:8080 -no_browser cpu.pprof
package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/google/pprof/driver"
)

//go:embed zh.js
var zhJS []byte

func main() {
	if err := driver.PProf(&driver.Options{HTTPServer: serve}); err != nil {
		fmt.Fprintln(os.Stderr, "pprof-zh:", err)
		os.Exit(1)
	}
}

// serve 仿照 pprof 默認 web 服務（/ui/ 前綴 + 僅本機訪問），並給每個 handler 包上漢化層。
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
				http.Error(w, "permission denied", http.StatusForbidden)
				return
			}
		}
		next := args.Handlers[r.URL.Path]
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

// localize 緩衝 handler 輸出，若是 HTML 則在 </body> 前注入翻譯腳本。
func localize(next http.Handler) http.Handler {
	inject := []byte("<script>" + string(zhJS) + "</script></body>")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &capture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(c, r)
		body := c.buf.Bytes()
		if strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			if i := bytes.LastIndex(body, []byte("</body>")); i >= 0 {
				body = append(append(append([]byte{}, body[:i]...), inject...), body[i+len("</body>"):]...)
			}
			w.Header().Del("Content-Length")
		}
		w.WriteHeader(c.status)
		w.Write(body)
	})
}
