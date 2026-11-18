// Package fastapi 是 Lemon Paperplane 的 FastAPI 插件：识别 FastAPI 应用（FastAPI() 实例或工厂函数），
// 用 uvicorn 运行（或直接运行自己调用 uvicorn.run 的入口脚本），提供打开 /docs 的动作与常见报错诊断。
// 解释器与虚拟环境由 python 插件负责（服务 resolveEnv）。
package fastapi

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

const defaultPort = 8000

// Plugin 实现 Detector、Analyzer、RunPreparer、Diagnoser、FixApplier、ActionInvoker。
type Plugin struct{}

// pluginID 是本插件 id。
const pluginID = "fastapi"

// New 创建插件实例。
func New() any { return &Plugin{} }

var (
	importFastAPIRe = regexp.MustCompile(`(?m)^\s*(?:from\s+fastapi[\s.]|import\s+fastapi\b)`)
	appDefRe        = regexp.MustCompile(`(?m)^([A-Za-z_]\w*)\s*(?::\s*[\w.\[\]]+\s*)?=\s*(?:fastapi\.)?FastAPI\(`)
	factoryRe       = regexp.MustCompile(`(?m)^def\s+(create_app|make_app|get_app|get_application|create_application|app_factory)\s*\(`)
	uvicornRunRe    = regexp.MustCompile(`uvicorn\.(?:run|Server|Config)\s*\(`)
	docsURLRe       = regexp.MustCompile(`docs_url\s*=\s*(None|["']([^"']*)["'])`)
)

// Detect：依赖里有 fastapi，或根目录 .py 导入了 fastapi。
func (p *Plugin) Detect(ctx *sdk.Context, d *sdk.DetectParams) (*sdk.DetectResult, error) {
	deps := pythonFacetDeps(d.Facets)
	declared := deps["fastapi"] || deps["fastapi-slim"]
	rootUse := false
	var scripts []any
	for _, f := range scanPy(d.Dir, 0, 40) {
		if importFastAPIRe.MatchString(f.Text) || appDefRe.MatchString(f.Text) {
			rootUse = true
		}
		if selfRunsUvicorn(f.Text) {
			scripts = append(scripts, f.Rel)
		}
	}
	// 常见布局 app/main.py：根目录没有 .py 时看一层子包
	if !rootUse && declared && !hasRootPy(d.Files) {
		rootUse = true
	}
	if !declared && !rootUse {
		return &sdk.DetectResult{}, nil
	}
	data := map[string]any{}
	// 根目录就在用 FastAPI（或没有别的入口）时，它是工程的主框架，默认运行项归 fastapi；
	// 只是依赖里带了 fastapi（如桌面程序里一个健康检查端点）时不抢默认。
	if rootUse || len(scripts) > 0 {
		data["category"] = "framework"
	}
	if len(scripts) > 0 {
		data["scripts"] = scripts
	}
	return &sdk.DetectResult{Facets: []sdk.Facet{{ID: "fastapi", Data: data}}}, nil
}

// selfRunsUvicorn：脚本有 __main__ 入口，并且自己调用 uvicorn.run / uvicorn.Server 启动。
func selfRunsUvicorn(text string) bool {
	return hasMainGuard(text) && uvicornRunRe.MatchString(text)
}

func hasRootPy(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, ".py") {
			return true
		}
	}
	return false
}

// isMainFramework：本模块的 fastapi facet 是否被标成主框架；或者 python 插件没有生成任何入口。
func isMainFramework(module *sdk.ModuleInfo) bool {
	if f := module.Facet("fastapi"); f != nil && f.Data != nil {
		if c, _ := f.Data["category"].(string); c == "framework" {
			return true
		}
	}
	var pm struct {
		Entries int `json:"entries"`
	}
	if module.Model("python", &pm) && pm.Entries == 0 {
		return true
	}
	return false
}

// app 是一个识别到的 FastAPI 应用。
type app struct {
	Target  string `json:"target"`  // uvicorn 目标，如 main:app、app.main:create_app
	File    string `json:"file"`    // 相对模块目录
	Factory bool   `json:"factory"` // 工厂函数（uvicorn --factory）
	DocsURL string `json:"docsUrl"` // "" 表示禁用
}

// script 是自己调用 uvicorn 启动的入口脚本。
type script struct {
	File string `json:"file"`
	Port int    `json:"port,omitempty"`
}

// Model 是 fastapi 插件模型。
type Model struct {
	Apps    []app    `json:"apps,omitempty"`
	Scripts []script `json:"scripts,omitempty"`
	Version string   `json:"version,omitempty"`
}

// analyzeModule 扫描应用与入口脚本；env 是工程级环境变量，与 .env 一起用于推断端口（工程设置优先）。
func analyzeModule(dir string, env map[string]string) *Model {
	m := &Model{}
	dotenv := sdk.MergeEnv(readDotenv(filepath.Join(dir, ".env")), env)
	for _, f := range scanPy(dir, 4, 600) {
		if !strings.Contains(f.Text, "FastAPI") && !strings.Contains(f.Text, "uvicorn") {
			continue
		}
		mod, _ := importPath(f.Rel)
		docs := "/docs"
		if dm := docsURLRe.FindStringSubmatch(f.Text); dm != nil {
			if dm[1] == "None" {
				docs = ""
			} else {
				docs = dm[2]
			}
		}
		if mod != "" {
			if am := appDefRe.FindStringSubmatch(f.Text); am != nil {
				m.Apps = append(m.Apps, app{Target: mod + ":" + am[1], File: f.Rel, DocsURL: docs})
			} else if fm := factoryRe.FindStringSubmatch(f.Text); fm != nil && strings.Contains(f.Text, "FastAPI(") {
				m.Apps = append(m.Apps, app{Target: mod + ":" + fm[1], File: f.Rel, Factory: true, DocsURL: docs})
			}
		}
		if selfRunsUvicorn(f.Text) {
			loc := uvicornRunRe.FindStringIndex(f.Text)
			call := f.Text[loc[0]:]
			if len(call) > 600 {
				call = call[:600]
			}
			m.Scripts = append(m.Scripts, script{File: f.Rel, Port: guessPortIn(dir, f, call, mainBlock(f.Text), dotenv)})
		}
	}
	// 入口优先级：根目录 main.py / app.py 在前，其次层级浅的
	rank := func(file string) int {
		base := filepath.Base(file)
		r := strings.Count(file, "/") * 10
		switch base {
		case "main.py":
		case "app.py", "server.py", "asgi.py":
			r++
		default:
			r += 5
		}
		return r
	}
	sort.SliceStable(m.Apps, func(i, j int) bool { return rank(m.Apps[i].File) < rank(m.Apps[j].File) })
	sort.SliceStable(m.Scripts, func(i, j int) bool { return rank(m.Scripts[i].File) < rank(m.Scripts[j].File) })
	return m
}

// guessPortIn：uvicorn.run(..., port=...) 或 uvicorn.Config(..., port=...)。
func guessPortIn(dir string, f pyFile, call, block string, dotenv map[string]string) int {
	if p := guessPort(dir, f, call, dotenv); p > 0 {
		return p
	}
	return guessPort(dir, f, block, dotenv)
}

// Analyze 生成运行配置。
func (p *Plugin) Analyze(ctx *sdk.Context, in *sdk.AnalyzeParams) (*sdk.AnalyzeResult, error) {
	if !in.Module.HasFacet("fastapi") {
		return &sdk.AnalyzeResult{}, nil // 插件因 python facet 被拉起，但本模块不是 fastapi 工程
	}
	m := analyzeModule(in.Module.Dir, in.Env)
	var pm pythonModel
	in.Module.Model("python", &pm)
	m.Version = installedVersion(pm.Venv, "fastapi")
	res := &sdk.AnalyzeResult{}
	res.Model, _ = json.Marshal(m)
	if m.Version != "" {
		res.Info = append(res.Info, sdk.InfoItem{Label: "FastAPI", Value: shortVersion(m.Version), Icon: "icons/fastapi.svg"})
	} else {
		res.Info = append(res.Info, sdk.InfoItem{Label: ctx.T("info.framework"), Value: "FastAPI", Icon: "icons/fastapi.svg"})
	}
	opts := []sdk.Option{} // 没有选项也输出 []，不能是 null（界面按数组解析）
	for _, a := range m.Apps {
		opts = append(opts, sdk.Option{Value: a.Target, Label: a.Target})
	}
	res.OptionValues = map[string][]sdk.Option{"fastapi.apps": opts}

	defaultSet := !isMainFramework(&in.Module)
	// 1. 自己调用 uvicorn 的入口脚本（如 main.py 里 uvicorn.run）：优先直接运行，它可能在 main() 里注册路由、建表
	for _, s := range m.Scripts {
		port := s.Port
		cfg := sdk.RunConfiguration{
			ID:       sdk.StableID("fastapi-app", "script", s.File),
			Name:     strings.TrimSuffix(filepath.Base(s.File), ".py"),
			Type:     "fastapi-app",
			Options:  map[string]any{"mode": "script", "script": s.File},
			Priority: 110,
			Source:   "auto",
		}
		if port > 0 {
			cfg.Options["port"] = port
		}
		if !defaultSet {
			cfg.Default = true
			defaultSet = true
		}
		res.RunConfigurations = append(res.RunConfigurations, cfg)
	}
	// 2. uvicorn 直接加载应用
	for i, a := range m.Apps {
		cfg := sdk.RunConfiguration{
			ID:       sdk.StableID("fastapi-app", "uvicorn", a.Target),
			Name:     "uvicorn " + a.Target,
			Type:     "fastapi-app",
			Options:  map[string]any{"mode": "uvicorn", "app": a.Target, "port": defaultPort, "host": "127.0.0.1", "reload": true},
			Priority: 100 - i,
			Source:   "auto",
		}
		if a.Factory {
			cfg.Options["factory"] = true
		}
		if !defaultSet {
			cfg.Default = true
			defaultSet = true
		}
		res.RunConfigurations = append(res.RunConfigurations, cfg)
		if i >= 4 {
			break
		}
	}
	for _, c := range ideConfigs(in) {
		res.RunConfigurations = append(res.RunConfigurations, c)
	}
	return res, nil
}

// ideConfigs 转换 PyCharm 的 FastAPI 运行配置（Python.FastAPI）。
func ideConfigs(in *sdk.AnalyzeParams) []sdk.RunConfiguration {
	var out []sdk.RunConfiguration
	for _, c := range in.IdeConfigs {
		if c.Source != "idea" || c.Type != "Python.FastAPI" {
			continue
		}
		file := strings.ReplaceAll(c.Options["file"], "$PROJECT_DIR$", in.Project.Root)
		if file == "" {
			continue
		}
		rel, err := filepath.Rel(in.Module.Dir, file)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		mod, _ := importPath(filepath.ToSlash(rel))
		if mod == "" {
			continue
		}
		target := mod + ":app"
		if am := appDefRe.FindStringSubmatch(readFile(file)); am != nil {
			target = mod + ":" + am[1]
		}
		opts := map[string]any{"mode": "uvicorn", "app": target, "port": defaultPort, "host": "127.0.0.1", "reload": true}
		if a := c.Options["uvicornArgs"]; a != "" {
			opts["args"] = a
		}
		out = append(out, sdk.RunConfiguration{ID: sdk.StableID("idea", c.Type, c.Name), Name: c.Name, Type: "fastapi-app", Options: opts, Env: c.Env, Source: "idea", Priority: 120})
	}
	return out
}

// PrepareRun 准备 fastapi-app。
func (p *Plugin) PrepareRun(ctx *sdk.Context, in *sdk.PrepareRunParams) (*sdk.LaunchSpec, error) {
	opts := in.Config.Options
	r, err := resolvePython(ctx, in.Module, in.Toolchains, in.Env, in.RunID, true, envFileOpt(opts))
	if err != nil {
		return nil, err
	}
	env := r.Env
	for k, v := range optEnv(opts) {
		env[k] = v
	}
	cwd := workingDir(in.Module.Dir, opts, in.Module.Dir)
	spec := &sdk.LaunchSpec{Command: r.Python, Cwd: cwd, Env: env, PathPrepend: r.PathPrepend}
	extra := sdk.SplitArgs(sdk.OptString(opts, "args", ""))
	switch sdk.OptString(opts, "mode", "uvicorn") {
	case "script":
		s := sdk.OptString(opts, "script", "")
		if s == "" {
			return nil, sdk.UserError("%s", ctx.T("err.noScript"))
		}
		if !filepath.IsAbs(s) {
			s = filepath.Join(in.Module.Dir, s)
		}
		spec.Args = append([]string{s}, extra...)
		if port := optInt(opts, "port", 0); port > 0 {
			spec.Ports = []int{port}
		}
	default:
		target := sdk.OptString(opts, "app", "")
		if target == "" {
			if m := analyzeModule(in.Module.Dir, nil); len(m.Apps) > 0 {
				target = m.Apps[0].Target
			} else {
				return nil, sdk.UserError("%s", ctx.T("err.noApp"))
			}
		}
		// 环境里没有 uvicorn 时先补装（FastAPI 只声明了 fastapi 的工程常见）
		if r.Venv != "" && installedVersion(r.Venv, "uvicorn") == "" {
			ctx.Host.Console(in.RunID, ctx.T("step.installUvicorn"))
			if err := installPythonPackages(ctx, in.Module, in.Toolchains, in.Env, in.RunID, "uvicorn[standard]"); err != nil {
				return nil, err
			}
		}
		host := sdk.OptString(opts, "host", "127.0.0.1")
		port := optInt(opts, "port", defaultPort)
		args := []string{"-m", "uvicorn", target, "--host", host, "--port", strconv.Itoa(port)}
		if sdk.OptBool(opts, "factory", false) {
			args = append(args, "--factory")
		}
		if sdk.OptBool(opts, "reload", true) {
			args = append(args, "--reload")
		}
		spec.Args = append(args, extra...)
		spec.Ports = []int{port}
	}
	return spec, nil
}

// Diagnose：端口被占用时补上运行配置的端口。
func (p *Plugin) Diagnose(ctx *sdk.Context, in *sdk.DiagnoseParams) (*sdk.DiagnoseResult, error) {
	if !in.Module.HasFacet("fastapi") {
		return &sdk.DiagnoseResult{}, nil
	}
	for _, d := range in.Matched {
		if strings.Contains(d.RuleID, "address-in-use") && len(d.Fixes) > 0 {
			return &sdk.DiagnoseResult{}, nil
		}
	}
	return &sdk.DiagnoseResult{Diagnoses: portDiagnoses(ctx, in, defaultPort, regexp.MustCompile(`\[Errno (?:48|98)\] Address already in use|\[WinError 10048\]`))}, nil
}

// ApplyFix：安装 FastAPI 运行时需要的可选依赖（python-multipart、email-validator 等）。
func (p *Plugin) ApplyFix(ctx *sdk.Context, in *sdk.ApplyFixParams) (*sdk.ApplyFixResult, error) {
	if in.Fix.ID != "installPackage" {
		return nil, sdk.ErrNotSupported
	}
	pkg := in.Fix.Params["package"]
	if pkg == "" {
		return nil, sdk.ErrNotSupported
	}
	if err := installPythonPackages(ctx, in.Module, in.Toolchains, in.Env, in.TaskID, pkg); err != nil {
		return nil, err
	}
	return &sdk.ApplyFixResult{Rerun: true, Message: ctx.T("msg.installed", pkg)}, nil
}

// InvokeAction：打开接口文档。从运行工具栏触发时用这次运行的实际地址，否则按分析结果推断端口。
func (p *Plugin) InvokeAction(ctx *sdk.Context, in *sdk.InvokeActionParams) (*sdk.InvokeActionResult, error) {
	if in.Action != "openDocs" {
		return nil, sdk.ErrNotSupported
	}
	var m Model
	if !in.Module.Model("fastapi", &m) {
		m = *analyzeModule(in.Module.Dir, in.Env)
	}
	docs := "/docs"
	if len(m.Apps) > 0 {
		docs = m.Apps[0].DocsURL
	}
	if docs == "" {
		return &sdk.InvokeActionResult{Message: ctx.T("msg.docsDisabled")}, nil
	}
	return &sdk.InvokeActionResult{OpenURL: docsBase(in, &m) + docs}, nil
}

// docsBase 决定应用地址：运行的 URL > 运行的端口 > 参数 url/port > 入口脚本推断的端口 > 8000。
func docsBase(in *sdk.InvokeActionParams, m *Model) string {
	if r := in.Run; r != nil {
		for _, u := range r.URLs {
			if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
				return baseURL(u)
			}
		}
		if len(r.Ports) > 0 && r.Ports[0] > 0 {
			return "http://127.0.0.1:" + strconv.Itoa(r.Ports[0])
		}
		if r.Config != nil {
			if port := optInt(r.Config.Options, "port", 0); port > 0 {
				return "http://127.0.0.1:" + strconv.Itoa(port)
			}
		}
	}
	if u, ok := in.Args["url"].(string); ok && u != "" {
		return baseURL(u)
	}
	port := optInt(in.Args, "port", 0)
	if port == 0 && len(m.Scripts) > 0 {
		port = m.Scripts[0].Port
	}
	if port == 0 {
		port = defaultPort
	}
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// baseURL 取 scheme://host:port 部分。
func baseURL(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		if j := strings.Index(u[i+3:], "/"); j >= 0 {
			return u[:i+3+j]
		}
	}
	return strings.TrimRight(u, "/")
}

func readFile(path string) string { return sdk.ReadText(path, 1<<20) }
