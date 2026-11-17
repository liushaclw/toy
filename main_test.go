package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/ui"
)

// open runs the page with no window: the real painter, input and focus rules.
func open(t *testing.T, m *model) *app.Driver {
	t.Helper()
	page := ui.NewPage(ui.PageConfig{View: m.page})
	driver, err := app.NewDriver(page.Config(config(m)), nil)
	if err != nil {
		t.Fatal(err)
	}
	m.inv.Invalidate() // as runApp does once the window is open
	t.Cleanup(func() {
		driver.Close()
		m.mgr.Close()
	})
	return driver
}

func newTestModel(t *testing.T, dirs ...string) *model {
	t.Helper()
	if len(dirs) == 0 {
		dirs = []string{"plugins"}
	}
	return newModel(dirs, t.TempDir())
}

// click presses the middle of the control with the given name, as a user would.
func click(t *testing.T, driver *app.Driver, name string) {
	t.Helper()
	for _, hit := range driver.Snapshot().Hits {
		if hit.Label == name && !hit.IsLabel {
			b := hit.Bounds
			if err := driver.Click(b.X+b.Width/2, b.Y+b.Height/2); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no control named %q on screen", name)
}

// typeInto focuses the field with the given name and types lines into it,
// pressing Enter between them.
func typeInto(t *testing.T, driver *app.Driver, name string, lines ...string) {
	t.Helper()
	click(t, driver, name)
	for i, line := range lines {
		if i > 0 {
			if err := driver.Key(gfx.KeyEnter, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := driver.Type(line); err != nil {
			t.Fatal(err)
		}
	}
}

// shows says whether the window painted want, in a label or in a text box.
func shows(driver *app.Driver, want string) bool {
	snap := driver.Snapshot()
	for _, label := range snap.Labels {
		if strings.Contains(label.Value, want) {
			return true
		}
	}
	for _, text := range snap.Texts {
		if strings.Contains(text.Value, want) {
			return true
		}
	}
	return false
}

// until pumps the window's wakes (results from plugin workers) until it shows want.
func until(t *testing.T, driver *app.Driver, want string) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for !shows(driver, want) {
		select {
		case <-driver.Wakes():
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("the window never showed %q", want)
		}
		if err := driver.Pump(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheWindowHasTheLogo(t *testing.T) {
	icon := windowIcon()
	if icon.Size != 512 || len(icon.RGBA) != 512*512*4 {
		t.Fatalf("the window icon is %d px with %d bytes", icon.Size, len(icon.RGBA))
	}
}

func TestSidebarListsThePlugins(t *testing.T) {
	driver := open(t, newTestModel(t))
	for _, name := range []string{"文本行处理", "JSON 格式化", "哈希计算", "编解码"} {
		if !shows(driver, name) {
			t.Errorf("the sidebar does not list %s", name)
		}
	}
}

func TestLuaToolRunsFromTheForm(t *testing.T) {
	m := newTestModel(t)
	driver := open(t, m)
	click(t, driver, "文本行处理")
	typeInto(t, driver, "输入", "pear", "apple", "pear")
	click(t, driver, "去重")
	until(t, driver, "输入 3 行，输出 2 行")
	if got := m.tools[m.selected].outText[0]; got != "pear\napple" {
		t.Fatalf("result %q", got)
	}
	click(t, driver, "排序")
	until(t, driver, "完成")
	if got := m.tools[m.selected].outText[0]; got != "apple\npear\npear" {
		t.Fatalf("sorted %q", got)
	}
}

func TestJSToolShowsATable(t *testing.T) {
	m := newTestModel(t)
	driver := open(t, m)
	click(t, driver, "JSON 格式化")
	typeInto(t, driver, "JSON", `[{"host":"alpha","port":22},{"host":"beta","port":443,"note":{"tag":"x"}}]`)
	click(t, driver, "转为表格")
	until(t, driver, "2 行 × 3 列")
	// Each cell is its own label in the output column; the input only holds the
	// JSON as one line, so these can only be the table's.
	out := bounds(t, driver, "结果")
	for _, want := range []string{"host", "port", "note", "beta", "443", `{"tag":"x"}`} {
		found := false
		for _, l := range driver.Snapshot().Labels {
			if l.Value == want && l.Bounds.X >= out.X {
				found = true
			}
		}
		if !found {
			t.Errorf("the table does not show the cell %q", want)
		}
	}
	typeInto(t, driver, "JSON", "}")
	click(t, driver, "格式化")
	until(t, driver, "不是合法的 JSON")
}

func TestExecToolHashesText(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the Go plugin with go run")
	}
	m := newTestModel(t)
	driver := open(t, m)
	click(t, driver, "哈希计算")
	typeInto(t, driver, "文本", "abc")
	click(t, driver, "计算")
	until(t, driver, "完成")
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := m.tools[m.selected].outText[1]; got != want {
		t.Fatalf("sha256 %q", got)
	}
	if !shows(driver, want[:16]) {
		t.Fatal("the digest is not painted")
	}
}

func TestReloadPicksUpANewPluginAndShowsBrokenOnes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("broken/plugin.toml", `id = "broken"`)
	m := newTestModel(t, dir)
	driver := open(t, m)
	click(t, driver, "broken")
	until(t, driver, "插件无法加载")

	write("echo/plugin.toml", `
id = "echo"
name = "回声"
runtime = "js"
entry = "main.js"
[[tools]]
id = "echo"
name = "回声"
  [[tools.inputs]]
  id = "text"
  label = "内容"
`)
	write("echo/main.js", `function run(action, inputs) { return "echo: " + inputs.text }`)
	m.changed.Store(true) // what watch does when the files change
	m.inv.Invalidate()
	until(t, driver, "回声")
	click(t, driver, "回声")
	typeInto(t, driver, "内容", "hi")
	click(t, driver, "运行")
	until(t, driver, "echo: hi")
}

func bounds(t *testing.T, driver *app.Driver, name string) gfx.Rect {
	t.Helper()
	for _, hit := range driver.Snapshot().Hits {
		if hit.Label == name && !hit.IsLabel {
			return hit.Bounds
		}
	}
	t.Fatalf("no control named %q on screen", name)
	return gfx.Rect{}
}

func TestOutputsSitBesideTheInputsAndTheLogFolds(t *testing.T) {
	m := newTestModel(t)
	driver := open(t, m)
	click(t, driver, "文本行处理")
	typeInto(t, driver, "输入", "b", "a")
	click(t, driver, "排序")
	until(t, driver, "完成")

	in, out := bounds(t, driver, "输入"), bounds(t, driver, "结果")
	if out.X < in.X+in.Width {
		t.Fatalf("the result (%v) is not beside the input (%v)", out, in)
	}
	frame := driver.Frame()
	if out.Y+out.Height > frame.Y+frame.Height {
		t.Fatalf("the result (%v) runs off the window (%v)", out, frame)
	}

	if shows(driver, "收起") {
		t.Fatal("the log starts open")
	}
	click(t, driver, "日志")
	if !shows(driver, "已加载") {
		t.Fatal("the opened log does not show its lines")
	}
	click(t, driver, "收起")
	if shows(driver, "收起") {
		t.Fatal("the log did not fold")
	}
}

// finish pumps the window until the tool's job has ended.
func finish(t *testing.T, driver *app.Driver, tv *toolView) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	for tv.running {
		select {
		case <-driver.Wakes():
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("the job never ended")
		}
		if err := driver.Pump(); err != nil {
			t.Fatal(err)
		}
	}
	if tv.err != "" {
		t.Fatalf("the job failed: %s", tv.err)
	}
}

func TestTheMainResultTakesTheColumn(t *testing.T) {
	m := newTestModel(t)
	driver := open(t, m)
	click(t, driver, "编解码")
	tv := m.tools[m.selected]
	for _, input := range []string{
		strings.Repeat("一段需要编码的比较长的文本，", 12), // one long line of Base64: it wraps
		"hi", // a result of a few characters still gets the room
	} {
		tv.texts[0] = input
		click(t, driver, "编码")
		finish(t, driver, tv)
		result, info := bounds(t, driver, "结果"), bounds(t, driver, "信息")
		if result.Height < 160 || result.Height < 3*info.Height {
			t.Fatalf("for %d bytes of input the result is %v beside info %v", len(input), result, info)
		}
		tv.texts[0] = tv.outText[0]
		click(t, driver, "解码")
		finish(t, driver, tv)
		if tv.outText[0] != input {
			t.Fatalf("decoded %q", tv.outText[0])
		}
	}
}

// textAt is where the window painted the text value, if it did inside r.
func textAt(driver *app.Driver, value string, r gfx.Rect) (gfx.Rect, bool) {
	for _, text := range driver.Snapshot().Texts {
		b := text.Bounds
		if text.Value == value && b.Y >= r.Y && b.Y+b.Height <= r.Y+r.Height+1 && b.X >= r.X {
			return b, true
		}
	}
	return gfx.Rect{}, false
}

func TestTheWheelScrollsALongResult(t *testing.T) {
	m := newTestModel(t)
	driver := open(t, m)
	click(t, driver, "文本行处理")
	tv := m.tools[m.selected]
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %03d", i)
	}
	tv.texts[0] = strings.Join(lines, "\n")
	click(t, driver, "排序")
	finish(t, driver, tv)

	box := bounds(t, driver, "结果")
	// A new result is read from the top, not left scrolled to its end.
	if _, ok := textAt(driver, "line 000", box); !ok {
		t.Fatal("the result does not start at its first line")
	}
	for range 5 {
		if err := driver.Scroll(gfx.ScrollEvent{X: box.X + box.Width/2, Y: box.Y + box.Height/2, DeltaY: 400}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := textAt(driver, "line 000", box); ok {
		t.Fatal("the wheel did not scroll the result")
	}
	shown := false
	for _, text := range driver.Snapshot().Texts {
		if strings.HasPrefix(text.Value, "line ") && text.Value > "line 050" && text.Bounds.X >= box.X &&
			text.Bounds.Y >= box.Y && text.Bounds.Y+text.Bounds.Height <= box.Y+box.Height+1 {
			shown = true
		}
	}
	if !shown {
		t.Fatal("scrolling did not bring later lines into the box")
	}
}
