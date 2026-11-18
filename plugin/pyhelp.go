package fastapi

// 本文件是框架插件（django / fastapi / flask）共用的 Python 辅助代码，各插件各存一份（插件之间只通过协议通信，不共享代码）。

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// pythonModel 是 python 插件模型里用到的字段（见 python 插件 analyze.go 的 Model）。
type pythonModel struct {
	Manager      string   `json:"manager"`
	Dependencies []string `json:"dependencies"`
	Venv         string   `json:"venv"`
	VenvPython   string   `json:"venvPython"`
	VenvVersion  string   `json:"venvVersion"`
	SourceRoots  []string `json:"sourceRoots"`
}

// resolveEnvParams / resolveEnvResult 对应 python 插件服务 resolveEnv。
type resolveEnvParams struct {
	Module     sdk.ModuleInfo           `json:"module"`
	Toolchains map[string]sdk.Toolchain `json:"toolchains,omitempty"`
	Env        map[string]string        `json:"env,omitempty"`
	Console    string                   `json:"console,omitempty"`
	Ensure     bool                     `json:"ensure,omitempty"`
	EnvFile    *string                  `json:"envFile,omitempty"`
}

type resolveEnvResult struct {
	Python      string            `json:"python"`
	Env         map[string]string `json:"env"`
	PathPrepend []string          `json:"pathPrepend,omitempty"`
	Venv        string            `json:"venv,omitempty"`
	Version     string            `json:"version,omitempty"`
}

// resolvePython 经 python 插件拿解释器与环境（必要时它会建虚拟环境、装依赖，输出进 console）。
// python 插件不可用时退回模型里的虚拟环境或选定的工具链。
func resolvePython(ctx *sdk.Context, module sdk.ModuleInfo, tcs map[string]sdk.Toolchain, env map[string]string, console string, ensure bool, envFile string) (*resolveEnvResult, error) {
	var r resolveEnvResult
	err := ctx.Host.Call(ctx, "python", "resolveEnv", &resolveEnvParams{Module: module, Toolchains: tcs, Env: env, Console: console, Ensure: ensure, EnvFile: &envFile}, &r)
	if err == nil && r.Python != "" {
		if r.Env == nil {
			r.Env = map[string]string{}
		}
		return &r, nil
	}
	if sdk.IsCode(err, sdk.ErrCodeUserError) || sdk.IsCode(err, sdk.ErrCodeCancelled) {
		return nil, err
	}
	// 兜底
	var m pythonModel
	module.Model("python", &m)
	res := &resolveEnvResult{Env: map[string]string{"PYTHONUNBUFFERED": "1", "PYTHONIOENCODING": "utf-8"}}
	switch {
	case m.VenvPython != "" && sdk.FileExists(m.VenvPython):
		res.Python, res.Venv = m.VenvPython, m.Venv
		res.Env["VIRTUAL_ENV"] = m.Venv
		res.PathPrepend = []string{filepath.Dir(m.VenvPython)}
	case tcs["python"].Executable != "":
		res.Python = tcs["python"].Executable
	default:
		res.Python = "python3"
		if runtime.GOOS == "windows" {
			res.Python = "python"
		}
	}
	roots := m.SourceRoots
	if len(roots) == 0 {
		roots = []string{module.Dir}
	}
	res.Env["PYTHONPATH"] = strings.Join(roots, string(os.PathListSeparator))
	return res, nil
}

// installPythonPackages 经 python 插件往模块环境里装包。
func installPythonPackages(ctx *sdk.Context, module sdk.ModuleInfo, tcs map[string]sdk.Toolchain, env map[string]string, console string, pkgs ...string) error {
	var r map[string]any
	return ctx.Host.Call(ctx, "python", "installPackages", map[string]any{"module": module, "toolchains": tcs, "env": env, "console": console, "packages": pkgs}, &r)
}

// pythonFacetDeps 取 python facet 里的依赖名（python 插件 detect 时写入）。
func pythonFacetDeps(facets []sdk.Facet) map[string]bool {
	out := map[string]bool{}
	for _, f := range facets {
		if f.ID != "python" || f.Data == nil {
			continue
		}
		if list, ok := f.Data["deps"].([]any); ok {
			for _, x := range list {
				if s, ok := x.(string); ok {
					out[s] = true
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// .py 文件扫描

type pyFile struct {
	Rel  string // 相对模块目录，/ 分隔
	Text string
}

var pySkipDirs = map[string]bool{"tests": true, "test": true, "migrations": true, "docs": true, "site-packages": true, "node_modules": true}

// scanPy 读模块目录下（最多 depth 层）的 .py 文件，跳过虚拟环境、测试等目录；最多 limit 个。
func scanPy(dir string, depth, limit int) []pyFile {
	var out []pyFile
	var walk func(d, rel string, level int)
	walk = func(d, rel string, level int) {
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		var dirs []string
		for _, e := range ents {
			name := e.Name()
			if len(out) >= limit {
				return
			}
			if e.IsDir() {
				if level < depth && !sdk.DefaultSkipDirs[name] && !strings.HasPrefix(name, ".") && !pySkipDirs[name] && !sdk.FileExists(filepath.Join(d, name, "pyvenv.cfg")) {
					dirs = append(dirs, name)
				}
				continue
			}
			if !strings.HasSuffix(name, ".py") || strings.HasPrefix(name, "test_") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(d, name))
			if err != nil || len(b) > 1<<20 {
				continue
			}
			r := name
			if rel != "" {
				r = rel + "/" + name
			}
			out = append(out, pyFile{Rel: r, Text: string(b)})
		}
		for _, n := range dirs {
			r := n
			if rel != "" {
				r = rel + "/" + n
			}
			walk(filepath.Join(d, n), r, level+1)
		}
	}
	walk(dir, "", 0)
	return out
}

var (
	identRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	mainGuardRe = regexp.MustCompile(`(?m)^if\s+__name__\s*==\s*['"]__main__['"]\s*:`)
)

// importPath 把相对路径 "app/main.py" 变成可导入的模块名 "app.main"；src 布局去掉 src/ 前缀。
// 路径里有不能作为标识符的段时返回空。
func importPath(rel string) (mod string, base string) {
	rel = strings.TrimSuffix(rel, ".py")
	parts := strings.Split(rel, "/")
	if len(parts) > 1 && parts[0] == "src" {
		parts = parts[1:]
		base = "src"
	}
	if parts[len(parts)-1] == "__init__" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return "", base
	}
	for _, p := range parts {
		if !identRe.MatchString(p) {
			return "", base
		}
	}
	return strings.Join(parts, "."), base
}

// hasMainGuard 判断文件有 if __name__ == "__main__":
func hasMainGuard(text string) bool { return mainGuardRe.MatchString(text) }

// mainBlock 返回 __main__ 块的文本（到文件末尾）。
func mainBlock(text string) string {
	loc := mainGuardRe.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	return text[loc[0]:]
}

// ---------------------------------------------------------------------------
// 端口推断：port=8000、port=PORT、port=int(os.getenv("PORT", 8000))、port=settings.port

var (
	portLiteralRe = regexp.MustCompile(`\bport\s*=\s*(\d{2,5})\b`)
	portExprRe    = regexp.MustCompile(`\bport\s*=\s*`)
	getenvRe      = regexp.MustCompile(`(?:getenv|environ\.get)\(\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*(?:,\s*["']?(\d{2,5})["']?)?`)
	environIdxRe  = regexp.MustCompile(`environ\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\]`)
	digitsRe      = regexp.MustCompile(`^\(?\s*(?:int\()?\s*["']?(\d{2,5})["']?\s*\)?\s*\)?$`)
	fromImportRe  = regexp.MustCompile(`(?m)^from\s+([\w.]+)\s+import\s+([^\n(]+|\([^)]*\))`)
)

// guessPort 在一段代码里（如 uvicorn.run(...) 调用）找端口；name 引用会到同文件与 from-import 的文件里找定义。
// envFile 里的变量用于解析 os.getenv("PORT")。找不到返回 0。
func guessPort(moduleDir string, f pyFile, snippet string, dotenv map[string]string) int {
	if m := portLiteralRe.FindStringSubmatch(snippet); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	loc := portExprRe.FindStringIndex(snippet)
	if loc == nil {
		return 0
	}
	return evalPortExpr(moduleDir, f, argExpr(snippet[loc[1]:]), dotenv, 0)
}

// argExpr 取关键字参数的表达式：到深度 0 的逗号或右括号为止（括号、引号配对）。
func argExpr(s string) string {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				return strings.TrimSpace(s[:i])
			}
			depth--
		case c == ',' && depth == 0, c == '\n' && depth == 0:
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}

func evalPortExpr(moduleDir string, f pyFile, expr string, dotenv map[string]string, depth int) int {
	if depth > 3 {
		return 0
	}
	if m := digitsRe.FindStringSubmatch(expr); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := getenvRe.FindStringSubmatch(expr); m != nil {
		if v, ok := dotenv[m[1]]; ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
		if m[2] != "" {
			n, _ := strconv.Atoi(m[2])
			return n
		}
		return 0
	}
	if m := environIdxRe.FindStringSubmatch(expr); m != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(dotenv[m[1]])); err == nil {
			return n
		}
		return 0
	}
	// 名字引用：NAME 或 obj.attr（取最后一段）
	name := expr
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	if !identRe.MatchString(name) {
		return 0
	}
	defRe := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*(?::\s*[\w\[\]]+\s*)?=\s*(.+)$`)
	if d := defRe.FindStringSubmatch(f.Text); d != nil {
		return evalPortExpr(moduleDir, f, strings.TrimSpace(d[1]), dotenv, depth+1)
	}
	// from x.y import NAME
	for _, im := range fromImportRe.FindAllStringSubmatch(f.Text, -1) {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(im[2]) {
			continue
		}
		path := filepath.Join(moduleDir, filepath.FromSlash(strings.ReplaceAll(im[1], ".", "/")))
		for _, cand := range []string{path + ".py", filepath.Join(path, "__init__.py")} {
			if b, err := os.ReadFile(cand); err == nil {
				if d := defRe.FindStringSubmatch(string(b)); d != nil {
					return evalPortExpr(moduleDir, pyFile{Rel: cand, Text: string(b)}, strings.TrimSpace(d[1]), dotenv, depth+1)
				}
			}
		}
	}
	return 0
}

// readDotenv 简单读 .env（只用于推断端口，运行时由 python 插件完整加载）。
func readDotenv(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 1<<20 {
		return nil
	}
	out := map[string]string{}
	for _, line := range bytes.Split(b, []byte("\n")) {
		s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(line)), "export "))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if k, v, ok := strings.Cut(s, "="); ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 已安装版本

var distInfoRe = regexp.MustCompile(`^(.+?)-([0-9][^-]*?)(?:-py\d.*)?\.(?:dist-info|egg-info)$`)

// installedVersion 读虚拟环境 site-packages 里某个包的版本（不执行命令）。
func installedVersion(venv, pkg string) string {
	if venv == "" {
		return ""
	}
	var sps []string
	if runtime.GOOS == "windows" {
		sps = []string{filepath.Join(venv, "Lib", "site-packages")}
	} else {
		libs, _ := filepath.Glob(filepath.Join(venv, "lib", "python3*", "site-packages"))
		sps = libs
	}
	want := normName(pkg)
	for _, sp := range sps {
		ents, _ := os.ReadDir(sp)
		for _, e := range ents {
			if m := distInfoRe.FindStringSubmatch(e.Name()); m != nil && normName(m[1]) == want {
				return m[2]
			}
		}
	}
	return ""
}

var normNameRe = regexp.MustCompile(`[-_.]+`)

func normName(s string) string { return normNameRe.ReplaceAllString(strings.ToLower(s), "-") }

// shortVersion 把 "0.115.12" 取成 "0.115"，"5.1.4" 取成 "5.1"。
func shortVersion(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}

// ---------------------------------------------------------------------------
// 运行配置选项

func optInt(opts map[string]any, key string, def int) int {
	switch t := opts[key].(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	}
	return def
}

// optEnv 读运行配置选项里的 env（keyvalue）。
func optEnv(opts map[string]any) map[string]string {
	out := map[string]string{}
	switch t := opts["env"].(type) {
	case map[string]any:
		for k, v := range t {
			if s, ok := v.(string); ok {
				out[k] = s
			} else {
				b, _ := json.Marshal(v)
				out[k] = string(b)
			}
		}
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				if k, v, ok := strings.Cut(s, "="); ok {
					out[k] = v
				}
			}
		}
	}
	return out
}

// envFileOpt 读 envFile 选项：缺省 ".env"，显式空字符串表示不读。
func envFileOpt(opts map[string]any) string {
	if v, ok := opts["envFile"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ".env"
}

// workingDir 解析工作目录选项（相对模块目录）。
func workingDir(moduleDir string, opts map[string]any, def string) string {
	wd := sdk.OptString(opts, "workingDir", "")
	if wd == "" {
		return def
	}
	if filepath.IsAbs(wd) {
		return wd
	}
	return filepath.Join(moduleDir, wd)
}

// sortedKeys 返回 map 的有序键。
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// portDiagnoses：日志里有「端口被占用」时给「换一个端口」的修复（结束占用进程由内核按 LaunchSpec.Ports 检测并提供）。
func portDiagnoses(ctx *sdk.Context, in *sdk.DiagnoseParams, defPort int, patterns ...*regexp.Regexp) []sdk.Diagnosis {
	if in.Config == nil {
		return nil
	}
	hit := false
	for _, line := range in.LogTail {
		for _, re := range patterns {
			if re.MatchString(line) {
				hit = true
			}
		}
	}
	if !hit {
		return nil
	}
	port := optInt(in.Config.Options, "port", defPort)
	if port <= 0 {
		return nil
	}
	ps := strconv.Itoa(port)
	return []sdk.Diagnosis{{
		RuleID: pluginID + ".port-in-use", Severity: "error",
		Title: ctx.T("diag.portInUse.title", ps),
		Fixes: []sdk.Fix{
			{ID: "nextPort", Title: ctx.T("fix.usePort", strconv.Itoa(port+1)), Kind: sdk.FixSetRunOption, Params: map[string]string{"key": "port", "value": strconv.Itoa(port + 1)}, Recommended: true},
		},
	}}
}
