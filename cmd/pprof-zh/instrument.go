package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed tracer/tracer.go
var tracerSrc string

// runInstrument 实现 `pprof-zh instrument`：
// 为指定目录下的所有函数生成“带埋点的副本”和 go build -overlay 用的 overlay.json，原始源码不会被修改。
func runInstrument(args []string) error {
	fs := flag.NewFlagSet("instrument", flag.ExitOnError)
	dir := fs.String("dir", ".", "要插桩的目录（递归）；必须位于某个 go module 内")
	entry := fs.String("entry", "", "额外的入口函数名（逗号分隔，如 job.Daily,consumer.Handle）；HTTP handler 会按签名自动识别，一般不用填")
	auto := fs.Bool("auto-entry", true, "按签名自动识别 HTTP 入口：func(http.ResponseWriter, *http.Request) 和带 *gin.Context 参数的函数")
	out := fs.String("out", ".pprof-zh-overlay", "输出目录（存放插桩后的文件和 overlay.json）")
	skip := fs.String("skip", "", "额外要跳过的路径片段（逗号分隔）")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法：pprof-zh instrument [-dir ./internal] [-entry pkg.Func,...] [-out 目录]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	absDir, err := filepath.Abs(*dir)
	must1(err)
	root, modPath, err := findModule(absDir)
	if err != nil {
		return err
	}
	absOut, err := filepath.Abs(*out)
	must1(err)
	must1(os.MkdirAll(filepath.Join(absOut, "files"), 0o755))
	skips := splitCSV(*skip)

	replace := map[string]string{}
	nFiles, nFuncs := 0, 0
	entryNames := splitCSV(*entry)
	rtImport := modPath + "/internal/pzprof"

	err = filepath.WalkDir(absDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != absDir && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			if p == absOut || p == filepath.Join(root, "internal", "pzprof") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasSuffix(name, ".pb.go") || strings.HasSuffix(name, "_gen.go") {
			return nil
		}
		for _, s := range skips {
			if strings.Contains(p, s) {
				return nil
			}
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(src[:min(len(src), 600)]), "Code generated") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.ParseComments)
		if err != nil {
			fmt.Fprintf(os.Stderr, "跳过（解析失败）%s: %v\n", p, err)
			return nil
		}
		for _, im := range f.Imports {
			if im.Path.Value == `"C"` {
				return nil // cgo 文件不处理
			}
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		impPath := modPath
		if rel != "." {
			impPath = modPath + "/" + filepath.ToSlash(rel)
		}
		symPkg := impPath
		if f.Name.Name == "main" {
			symPkg = "main" // pprof 里 main 包的符号前缀就是 main
		}
		type edit struct {
			off  int
			text string
		}
		var edits []edit
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || len(fn.Body.List) == 0 || fn.Name.Name == "init" || fn.Name.Name == "_" {
				continue
			}
			recv, ptr := recvName(fn)
			disp, sym := f.Name.Name+"."+fn.Name.Name, symPkg+"."+fn.Name.Name
			if recv != "" {
				disp = f.Name.Name + "." + recv + "." + fn.Name.Name
				if ptr {
					sym = symPkg + ".(*" + recv + ")." + fn.Name.Name
				} else {
					sym = symPkg + "." + recv + "." + fn.Name.Name
				}
			}
			if *auto && isHTTPEntry(fn) {
				entryNames = append(entryNames, disp)
			}
			text := "defer _pzprof.Enter(" + strconv.Quote(disp) + "," + strconv.Quote(sym) + ")();"
			if f.Name.Name == "main" && recv == "" && fn.Name.Name == "main" {
				text = "defer _pzprof.Flush();" + text
			}
			edits = append(edits, edit{fset.Position(fn.Body.Lbrace).Offset + 1, text})
			nFuncs++
		}
		if len(edits) == 0 {
			return nil
		}
		// import 与插入的语句都放在原有行内，保证源码行号不变
		edits = append(edits, edit{fset.Position(f.Name.End()).Offset, `; import _pzprof "` + rtImport + `"`})
		sort.Slice(edits, func(i, j int) bool { return edits[i].off > edits[j].off })
		b := []byte(string(src))
		for _, e := range edits {
			b = append(b[:e.off], append([]byte(e.text), b[e.off:]...)...)
		}
		dst := filepath.Join(absOut, "files", strconv.Itoa(nFiles)+".go")
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
		replace[p] = dst
		nFiles++
		return nil
	})
	if err != nil {
		return err
	}

	// 运行时包：放在 <module>/internal/pzprof（仅存在于 overlay 中）
	rt := strings.Replace(tracerSrc, "package tracer", "package pzprof", 1)
	must1(os.WriteFile(filepath.Join(absOut, "pzprof.go"), []byte(rt), 0o644))
	var sb strings.Builder
	sb.WriteString("package pzprof\n\nfunc init() {\n\tentries = map[string]bool{\n")
	for _, e := range entryNames {
		sb.WriteString("\t\t" + strconv.Quote(e) + ": true,\n")
	}
	sb.WriteString("\t}\n}\n")
	must1(os.WriteFile(filepath.Join(absOut, "zz_entries.go"), []byte(sb.String()), 0o644))
	replace[filepath.Join(root, "internal", "pzprof", "pzprof.go")] = filepath.Join(absOut, "pzprof.go")
	replace[filepath.Join(root, "internal", "pzprof", "zz_entries.go")] = filepath.Join(absOut, "zz_entries.go")

	ov, _ := json.MarshalIndent(map[string]any{"Replace": replace}, "", "  ")
	ovPath := filepath.Join(absOut, "overlay.json")
	must1(os.WriteFile(ovPath, ov, 0o644))

	fmt.Printf("已生成 %d 个文件、%d 个函数的插桩副本（原始源码未改动）\n", nFiles, nFuncs)
	fmt.Printf("识别到 %d 个入口：%s\n", len(entryNames), strings.Join(entryNames, ", "))
	if len(entryNames) == 0 {
		fmt.Println("提示：没有识别到入口，将把每个 goroutine 最外层的函数都当作入口（开销大）；可用 -entry 指定。")
	}
	fmt.Printf("overlay: %s\n\n编译：\n  go build -overlay=%s -o app <你的包>\n", ovPath, ovPath)
	fmt.Println("运行后统计写入 calls.json（可用环境变量 PPROF_ZH_CALLS_OUT 指定路径），再用 pprof-zh 打开。")
	return nil
}

// isHTTPEntry 判断函数签名是否像 HTTP 入口：参数里有 *http.Request 或 *gin.Context。
func isHTTPEntry(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, p := range fn.Type.Params.List {
		st, ok := p.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := st.X.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		if x, ok := sel.X.(*ast.Ident); ok &&
			((x.Name == "http" && sel.Sel.Name == "Request") || (x.Name == "gin" && sel.Sel.Name == "Context")) {
			return true
		}
	}
	return false
}

func recvName(fn *ast.FuncDecl) (name string, ptr bool) {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "", false
	}
	t := fn.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		ptr = true
		t = s.X
	}
	switch x := t.(type) {
	case *ast.Ident:
		return x.Name, ptr
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name, ptr
		}
	case *ast.IndexListExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name, ptr
		}
	}
	return "", false
}

var modRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

func findModule(dir string) (root, mod string, err error) {
	for d := dir; ; d = filepath.Dir(d) {
		if b, e := os.ReadFile(filepath.Join(d, "go.mod")); e == nil {
			m := modRe.FindSubmatch(b)
			if m == nil {
				return "", "", fmt.Errorf("%s/go.mod 中找不到 module 声明", d)
			}
			return d, strings.Trim(string(m[1]), `"`), nil
		}
		if filepath.Dir(d) == d {
			return "", "", fmt.Errorf("%s 不在任何 go module 内", dir)
		}
	}
}

func splitCSV(s string) (r []string) {
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			r = append(r, x)
		}
	}
	return
}

func must1(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}
