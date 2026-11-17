package plugin_test

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"toy/internal/plugin"
	"toy/internal/plugin/execrt"
	"toy/internal/plugin/jsrt"
	"toy/internal/plugin/luart"
)

func runtimes() map[string]plugin.Runtime {
	return map[string]plugin.Runtime{"lua": luart.Runtime{}, "js": jsrt.Runtime{}, "exec": execrt.Runtime{}}
}

// harness runs actions through a Manager and waits for them, as the window does.
type harness struct {
	t    *testing.T
	m    *plugin.Manager
	wake chan struct{}
	logs []string
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, wake: make(chan struct{}, 1)}
	h.m = plugin.NewManager(runtimes(), t.TempDir(), func() {
		select {
		case h.wake <- struct{}{}:
		default:
		}
	})
	t.Cleanup(h.m.Close)
	return h
}

func (h *harness) wait(job int, timeout time.Duration) plugin.Event {
	h.t.Helper()
	deadline := time.After(timeout)
	for {
		for _, e := range h.m.Drain() {
			if e.Kind == plugin.EventLog {
				h.logs = append(h.logs, e.Text)
			}
			if e.Kind == plugin.EventDone && e.Job == job {
				return e
			}
		}
		select {
		case <-h.wake:
		case <-deadline:
			h.t.Fatalf("job %d did not finish in %v", job, timeout)
		}
	}
}

func (h *harness) run(p *plugin.Plugin, action string, inputs map[string]any) plugin.Event {
	h.t.Helper()
	return h.wait(h.m.Run(p, &p.Manifest.Tools[0], action, inputs), 60*time.Second)
}

func find(t *testing.T, plugins []*plugin.Plugin, id string) *plugin.Plugin {
	t.Helper()
	for _, p := range plugins {
		if p.Manifest.ID == id {
			if p.Err != nil {
				t.Fatalf("%s: %v", id, p.Err)
			}
			return p
		}
	}
	t.Fatalf("no plugin %q", id)
	return nil
}

// writePlugin makes a one-tool plugin in a temporary directory.
func writePlugin(t *testing.T, manifest string, files map[string]string) *plugin.Plugin {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "p")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, plugin.ManifestFile), []byte(manifest), 0o644)
	for name, content := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	plugins := plugin.Discover(root)
	if len(plugins) != 1 || plugins[0].Err != nil {
		t.Fatalf("plugin not loaded: %+v", plugins)
	}
	return plugins[0]
}

func TestExamplesLoad(t *testing.T) {
	plugins := plugin.Discover("../../plugins")
	if len(plugins) != 5 {
		t.Fatalf("want 5 example plugins, got %d", len(plugins))
	}
	for _, p := range plugins {
		if p.Err != nil {
			t.Errorf("%s: %v", p.Dir, p.Err)
		}
	}
}

func TestLuaExample(t *testing.T) {
	h := newHarness(t)
	p := find(t, plugin.Discover("../../plugins"), "lines")
	in := map[string]any{"text": "pear\n Apple\napple\n\nbanana", "trim": true, "ignore_case": true, "drop_empty": true}
	e := h.run(p, "sort", in)
	if e.Err != nil {
		t.Fatal(e.Err)
	}
	if got := e.Result.Texts["result"]; got != "Apple\napple\nbanana\npear" {
		t.Fatalf("sort: %q", got)
	}
	e = h.run(p, "unique", in)
	if got := e.Result.Texts["result"]; got != "pear\nApple\nbanana" {
		t.Fatalf("unique: %q", got)
	}
	if e.Result.Texts["summary"] != "输入 5 行，输出 3 行" {
		t.Fatalf("summary: %q", e.Result.Texts["summary"])
	}
	if len(h.logs) == 0 {
		t.Fatal("host.log did not reach the log")
	}
}

func TestJSExample(t *testing.T) {
	h := newHarness(t)
	p := find(t, plugin.Discover("../../plugins"), "json-tool")
	e := h.run(p, "format", map[string]any{"text": `{"b":1,"a":[1,2]}`, "indent": "2 空格", "sort_keys": true})
	if e.Err != nil {
		t.Fatal(e.Err)
	}
	if want := "{\n  \"a\": [\n    1,\n    2\n  ],\n  \"b\": 1\n}"; e.Result.Texts["result"] != want {
		t.Fatalf("format: %q", e.Result.Texts["result"])
	}
	e = h.run(p, "table", map[string]any{"text": `[{"name":"a","n":1},{"name":"b","x":true}]`})
	table := e.Result.Tables["table"]
	if e.Err != nil || strings.Join(table.Columns, ",") != "name,n,x" || len(table.Rows) != 2 || table.Rows[1][2] != "true" {
		t.Fatalf("table: %+v %v", table, e.Err)
	}
	e = h.run(p, "format", map[string]any{"text": "{oops"})
	if e.Err == nil || !strings.Contains(e.Err.Error(), "不是合法的 JSON") {
		t.Fatalf("want a parse error, got %v", e.Err)
	}
	// The instance survives a thrown error.
	if e = h.run(p, "minify", map[string]any{"text": `{ "a" : 1 }`}); e.Result.Texts["result"] != `{"a":1}` {
		t.Fatalf("minify after error: %+v", e)
	}
}

// buildHash compiles the exec example so the test does not depend on `go run` timing.
func buildHash(t *testing.T) *plugin.Plugin {
	t.Helper()
	src := find(t, plugin.Discover("../../plugins"), "hash")
	exe := filepath.Join(t.TempDir(), "hash")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", exe, src.Dir).CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	src.Manifest.Command = []string{exe}
	return src
}

func TestExecExample(t *testing.T) {
	h := newHarness(t)
	p := buildHash(t)
	e := h.run(p, "hash", map[string]any{"text": "abc", "upper": false})
	if e.Err != nil {
		t.Fatal(e.Err)
	}
	if want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"; e.Result.Texts["sha256"] != want {
		t.Fatalf("sha256: %q", e.Result.Texts["sha256"])
	}
	if rows := e.Result.Tables["digests"].Rows; len(rows) != 4 || rows[0][0] != "MD5" {
		t.Fatalf("digests: %+v", rows)
	}
	// A file, with progress, then the same process again.
	file := filepath.Join(t.TempDir(), "f.txt")
	os.WriteFile(file, []byte("abc"), 0o644)
	e = h.run(p, "hash", map[string]any{"file": file, "upper": true})
	if e.Err != nil || !strings.HasPrefix(e.Result.Texts["sha256"], "BA7816BF") {
		t.Fatalf("file: %+v", e)
	}
}

func TestStorageAndErrors(t *testing.T) {
	h := newHarness(t)
	p := writePlugin(t, `
id = "counter"
runtime = "lua"
entry = "main.lua"
[[tools]]
id = "c"
`, map[string]string{"main.lua": `
function run(action, inputs)
  if action == "fail" then error("boom") end
  local n = (host.get("n") or 0) + 1
  host.set("n", n)
  return n
end`})
	for want := 1; want <= 2; want++ {
		if e := h.run(p, "run", nil); e.Result.Texts["result"] != strconv.Itoa(want) {
			t.Fatalf("run %d: %+v", want, e)
		}
	}
	if e := h.run(p, "fail", nil); e.Err == nil || !strings.Contains(e.Err.Error(), "boom") {
		t.Fatalf("want boom, got %v", e.Err)
	}
}

func TestCancelAndTimeout(t *testing.T) {
	for _, lang := range []struct{ runtime, entry, src string }{
		{"lua", "main.lua", `function run(action) if action == "spin" then while true do end end return "ok" end`},
		{"js", "main.js", `function run(action) { if (action === "spin") for (;;) {} return "ok" }`},
	} {
		t.Run(lang.runtime, func(t *testing.T) {
			h := newHarness(t)
			p := writePlugin(t, `
id = "spin"
runtime = "`+lang.runtime+`"
entry = "`+lang.entry+`"
[[tools]]
id = "t"
timeout = 1
`, map[string]string{lang.entry: lang.src})
			job := h.m.Run(p, &p.Manifest.Tools[0], "spin", nil)
			time.Sleep(100 * time.Millisecond)
			h.m.Cancel(job)
			if e := h.wait(job, 10*time.Second); !errors.Is(e.Err, context.Canceled) {
				t.Fatalf("cancel: %v", e.Err)
			}
			e := h.wait(h.m.Run(p, &p.Manifest.Tools[0], "spin", nil), 10*time.Second)
			if e.Err == nil || !strings.Contains(e.Err.Error(), "timed out") {
				t.Fatalf("timeout: %v", e.Err)
			}
			if e := h.run(p, "ok", nil); e.Err != nil || e.Result.Texts["result"] != "ok" {
				t.Fatalf("after timeout: %+v", e)
			}
		})
	}
}

func TestBadPluginIsListedWithItsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, plugin.ManifestFile), []byte(`id = "bad"`+"\n"+`runtime = "lua"`), 0o644)
	plugins := plugin.Discover(root)
	if len(plugins) != 1 || plugins[0].Err == nil {
		t.Fatalf("want one plugin with an error, got %+v", plugins)
	}
}

func TestCodecExample(t *testing.T) {
	h := newHarness(t)
	p := find(t, plugin.Discover("../../plugins"), "codec")
	run := func(action, codec, text string, all bool) plugin.Event {
		t.Helper()
		return h.run(p, action, map[string]any{"text": text, "codec": codec, "all": all})
	}
	const text = "Hello, 世界 🌍 <a href=\"x?q=1&r=2\">'</a>"

	wants := map[string]string{
		"Base64":     base64.StdEncoding.EncodeToString([]byte(text)),
		"Base64 URL": base64.RawURLEncoding.EncodeToString([]byte(text)),
		"Hex":        hex.EncodeToString([]byte(text)),
	}
	for _, codec := range []string{"Base64", "Base64 URL", "URL", "Hex", "HTML 实体", "Unicode 转义"} {
		for _, all := range []bool{false, true} {
			enc := run("encode", codec, text, all)
			if enc.Err != nil {
				t.Fatalf("%s encode: %v", codec, enc.Err)
			}
			encoded := enc.Result.Texts["result"]
			if want, ok := wants[codec]; ok && encoded != want {
				t.Errorf("%s encode: %q, want %q", codec, encoded, want)
			}
			dec := run("decode", codec, encoded, false)
			if dec.Err != nil || dec.Result.Texts["result"] != text {
				t.Errorf("%s (all=%v) round trip: %q -> %q (%v)", codec, all, encoded, dec.Result.Texts["result"], dec.Err)
			}
		}
	}

	if got := run("encode", "Unicode 转义", "a中", false).Result.Texts["result"]; got != "a"+"\\"+"u4e2d" {
		t.Errorf("unicode: %q", got)
	}
	if got := run("encode", "HTML 实体", "<中>", true).Result.Texts["result"]; got != "&lt;&#x4E2D;&gt;" {
		t.Errorf("html all: %q", got)
	}
	// Bytes that are not UTF-8 come back as hex, and say so.
	bin := run("decode", "Base64", base64.StdEncoding.EncodeToString([]byte{0xff, 0x00, 0x80}), false)
	if bin.Err != nil || bin.Result.Texts["result"] != "ff0080" || !strings.Contains(bin.Result.Texts["info"], "十六进制") {
		t.Errorf("binary: %+v", bin)
	}
	if e := run("decode", "Base64", "ab$c", false); e.Err == nil || !strings.Contains(e.Err.Error(), "不是 Base64 字符") {
		t.Errorf("bad base64: %v", e.Err)
	}
	if e := run("decode", "Hex", "abc", false); e.Err == nil {
		t.Error("odd hex decoded")
	}

	seg := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	token := seg(`{"alg":"HS256","typ":"JWT"}`) + "." + seg(`{"sub":"张三","exp":1700000000}`) + ".sig"
	jwt := run("decode", "JWT", token, false)
	if jwt.Err != nil || !strings.Contains(jwt.Result.Texts["result"], `"sub": "张三"`) ||
		!strings.Contains(jwt.Result.Texts["info"], "HS256") || !strings.Contains(jwt.Result.Texts["info"], "已过期") {
		t.Errorf("jwt: %+v", jwt)
	}
	if e := run("encode", "JWT", "x", false); e.Err == nil {
		t.Error("JWT encode should refuse")
	}
}

// A job's context is cancelled as it completes; that must never interrupt the
// next run of the same script (it once did, returning an empty result).
func TestJSBackToBackRunsAllAnswer(t *testing.T) {
	h := newHarness(t)
	p := writePlugin(t, `
id = "quick"
runtime = "js"
entry = "main.js"
[[tools]]
id = "t"
`, map[string]string{"main.js": `function run(action, inputs) { return "n" + inputs.n }`})
	for i := 0; i < 300; i++ {
		want := "n" + strconv.Itoa(i)
		if e := h.run(p, "run", map[string]any{"n": i}); e.Err != nil || e.Result.Texts["result"] != want {
			t.Fatalf("run %d: %q, %v", i, e.Result.Texts["result"], e.Err)
		}
	}
}

func TestActionStyles(t *testing.T) {
	write := func(actions string) (*plugin.Manifest, error) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "main.lua"), []byte("function run() return '' end"), 0o644)
		os.WriteFile(filepath.Join(dir, plugin.ManifestFile), []byte(`id = "s"
runtime = "lua"
entry = "main.lua"
[[tools]]
id = "t"
`+actions), 0o644)
		return plugin.LoadManifest(dir)
	}
	man, err := write(`
  [[tools.actions]]
  id = "a"
  [[tools.actions]]
  id = "b"
  [[tools.actions]]
  id = "c"
  style = "danger"
`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range man.Tools[0].Actions {
		got = append(got, a.Style)
	}
	if strings.Join(got, ",") != "primary,outline,danger" {
		t.Fatalf("styles %v", got)
	}
	if _, err := write("  [[tools.actions]]\n  id = \"a\"\n  style = \"blue\"\n"); err == nil || !strings.Contains(err.Error(), "blue") {
		t.Fatalf("an unknown style gave %v", err)
	}
	if _, err := write("  [[tools.actions]]\n  id = \"a\"\n  requires = [\"nope\"]\n"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("requiring an unknown input gave %v", err)
	}
}
