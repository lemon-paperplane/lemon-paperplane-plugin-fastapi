package fastapi

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// loadManifest 读插件根目录的 plugin.json，并检查所有正则可编译、每条规则有 hint、%key% 在两份 l10n 里都有。
func loadManifest(t *testing.T) *sdk.Manifest {
	t.Helper()
	b, err := os.ReadFile("../plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m sdk.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Contributes.Diagnostics {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			t.Fatalf("%s: %v", r.ID, err)
		}
		if r.Hint == "" {
			t.Errorf("%s: no hint", r.ID)
		}
	}
	for _, u := range m.Contributes.URLPatterns {
		if _, err := regexp.Compile(u.Pattern); err != nil {
			t.Fatalf("url %s: %v", u.Pattern, err)
		}
	}
	for _, loc := range []string{"zh-Hans", "en"} {
		var l10n map[string]string
		data, _ := os.ReadFile("../l10n/" + loc + ".json")
		if err := json.Unmarshal(data, &l10n); err != nil {
			t.Fatal(err)
		}
		for _, k := range regexp.MustCompile(`%([\w.]+)%`).FindAllStringSubmatch(string(b), -1) {
			if _, ok := l10n[k[1]]; !ok {
				t.Errorf("%s: missing l10n key %s", loc, k[1])
			}
		}
	}
	return &m
}

// matchRule 像内核一样匹配一行：先 hint 再正则，返回捕获组。
func matchRule(t *testing.T, m *sdk.Manifest, id, line string) []string {
	t.Helper()
	for _, r := range m.Contributes.Diagnostics {
		if r.ID != id {
			continue
		}
		if !strings.Contains(line, r.Hint) {
			t.Errorf("%s: hint %q not in line", id, r.Hint)
			return nil
		}
		mm := regexp.MustCompile(r.Pattern).FindStringSubmatch(line)
		if mm == nil {
			t.Errorf("%s: no match for %q", id, line)
		}
		return mm
	}
	t.Errorf("rule %s not found", id)
	return nil
}

// matchURL 用 urlPatterns 从一行里得到地址（模板 {n} 引用捕获组，空模板取第 1 组）。
func matchURL(m *sdk.Manifest, line string) string {
	for _, u := range m.Contributes.URLPatterns {
		if u.Hint != "" && !strings.Contains(line, u.Hint) {
			continue
		}
		mm := regexp.MustCompile(u.Pattern).FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		if u.URL == "" {
			return mm[1]
		}
		out := u.URL
		for i := len(mm) - 1; i >= 1; i-- {
			out = strings.ReplaceAll(out, "{"+string(rune('0'+i))+"}", mm[i])
		}
		return out
	}
	return ""
}
