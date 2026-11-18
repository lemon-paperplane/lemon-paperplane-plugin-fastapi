package fastapi

import (
	"testing"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

func TestAnalyzeApps(t *testing.T) {
	m := analyzeModule("testdata/fa1", nil)
	if len(m.Apps) != 1 || m.Apps[0].Target != "app.main:app" || m.Apps[0].DocsURL != "/api/docs" {
		t.Fatalf("fa1 apps = %+v", m.Apps)
	}
	m = analyzeModule("testdata/fa2", nil)
	if len(m.Scripts) != 1 || m.Scripts[0].File != "main.py" || m.Scripts[0].Port != 9100 {
		t.Fatalf("fa2 scripts = %+v（端口应从 .env 的 PORT 解析出 9100）", m.Scripts)
	}
	if len(m.Apps) != 1 || m.Apps[0].Target != "main:app" {
		t.Fatalf("fa2 apps = %+v", m.Apps)
	}
	m = analyzeModule("testdata/fa3", nil)
	if len(m.Apps) != 1 || m.Apps[0].Target != "svc.web:create_app" || !m.Apps[0].Factory {
		t.Fatalf("fa3 apps = %+v", m.Apps)
	}
}

func TestImportPath(t *testing.T) {
	cases := map[string]string{"main.py": "main", "app/main.py": "app.main", "src/pkg/api.py": "pkg.api", "pkg/__init__.py": "pkg", "my-dir/x.py": ""}
	for in, want := range cases {
		if got, _ := importPath(in); got != want {
			t.Errorf("importPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessPort(t *testing.T) {
	f := pyFile{Rel: "x.py", Text: "P = 8123\n"}
	for snippet, want := range map[string]int{
		`uvicorn.run(app, port=8000)`:                               8000,
		`uvicorn.run(app, port=P)`:                                  8123,
		`uvicorn.run(app, port=int(os.getenv("APP_PORT", "7001")))`: 7001,
		`uvicorn.run(app, port=int(os.environ["X"]))`:               0,
	} {
		if got := guessPort(".", f, snippet, nil); got != want {
			t.Errorf("guessPort(%q) = %d, want %d", snippet, got, want)
		}
	}
}

func TestManifest(t *testing.T) {
	m := loadManifest(t)
	if got := matchURL(m, "INFO:     Uvicorn running on http://0.0.0.0:8000 (Press CTRL+C to quit)"); got != "http://127.0.0.1:8000" {
		t.Errorf("url = %q", got)
	}
	if got := matchURL(m, "INFO:     Uvicorn running on http://127.0.0.1:8765 (Press CTRL+C to quit)"); got != "http://127.0.0.1:8765" {
		t.Errorf("url = %q", got)
	}
	if mm := matchRule(t, m, "fastapi.import-module", `ERROR:    Error loading ASGI app. Could not import module "main".`); mm != nil && mm[1] != "main" {
		t.Errorf("group = %q", mm[1])
	}
	if mm := matchRule(t, m, "fastapi.attribute", `ERROR:    Error loading ASGI app. Attribute "app" not found in module "main".`); mm != nil && (mm[1] != "app" || mm[2] != "main") {
		t.Errorf("groups = %v", mm)
	}
	matchRule(t, m, "fastapi.multipart", `RuntimeError: Form data requires "python-multipart" to be installed.`)
	matchRule(t, m, "fastapi.email-validator", `ImportError: email-validator is not installed, run `+"`pip install pydantic[email]`")
	matchRule(t, m, "fastapi.no-cli", `/x/.venv/bin/python: No module named uvicorn`)
}

func TestDocsBase(t *testing.T) {
	m := &Model{Scripts: []script{{File: "main.py", Port: 9100}}}
	in := &sdk.InvokeActionParams{Run: &sdk.RunInfo{URLs: []string{"http://127.0.0.1:8765/"}, Ports: []int{8765}}}
	if got := docsBase(in, m); got != "http://127.0.0.1:8765" {
		t.Errorf("run url: %q", got)
	}
	in = &sdk.InvokeActionParams{Run: &sdk.RunInfo{Ports: []int{8123}}}
	if got := docsBase(in, m); got != "http://127.0.0.1:8123" {
		t.Errorf("run port: %q", got)
	}
	if got := docsBase(&sdk.InvokeActionParams{}, m); got != "http://127.0.0.1:9100" {
		t.Errorf("no run: %q", got)
	}
	// 工程环境变量优先于 .env
	if mm := analyzeModule("testdata/fa2", map[string]string{"PORT": "9200"}); len(mm.Scripts) != 1 || mm.Scripts[0].Port != 9200 {
		t.Errorf("project env port: %+v", mm.Scripts)
	}
}
