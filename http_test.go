package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
)

func inputIndex(t *testing.T, tv *toolView, id string) int {
	t.Helper()
	for i, in := range tv.tool.Inputs {
		if in.ID == id {
			return i
		}
	}
	t.Fatalf("%s has no input %q", tv.title, id)
	return -1
}

func TestHTTPToolSendsAndKeepsRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"path":"`+r.URL.Path+`"}`)
	}))
	defer server.Close()
	data := t.TempDir()
	m := newModel([]string{"plugins"}, data)
	driver := open(t, m)
	click(t, driver, "HTTP 请求")
	tv := m.tools[m.selected]
	finish(t, driver, tv) // the init that lists what is saved
	if !shows(driver, "（暂无）") {
		t.Fatal("nothing saved yet, and the form does not say so")
	}

	typeInto(t, driver, "URL", server.URL+"/a")
	click(t, driver, "发送")
	finish(t, driver, tv)
	if !shows(driver, "200 OK") || !strings.Contains(tv.outText[inputIndexOut(tv, "body")], `"path": "/a"`) {
		t.Fatalf("send: status shown %v, body %q", shows(driver, "200 OK"), tv.outText[inputIndexOut(tv, "body")])
	}

	url, name, saved := inputIndex(t, tv, "url"), inputIndex(t, tv, "name"), inputIndex(t, tv, "saved")
	tv.texts[name] = "first"
	click(t, driver, "保存")
	finish(t, driver, tv)
	tv.texts[url], tv.texts[name] = server.URL+"/b", "second"
	click(t, driver, "保存")
	finish(t, driver, tv)
	if got := strings.Join(tv.options[saved], ","); got != "first,second" || tv.choices[saved] != "second" || !shows(driver, "已保存「second」") {
		t.Fatalf("saved %q, chosen %q", got, tv.choices[saved])
	}
	if !shows(driver, "200 OK") {
		t.Fatal("saving cleared the response")
	}

	// Choosing a saved request loads it into the form.
	click(t, driver, "已保存的请求")
	click(t, driver, "first")
	finish(t, driver, tv)
	if tv.texts[url] != server.URL+"/a" || tv.texts[name] != "first" {
		t.Fatalf("loaded url %q name %q", tv.texts[url], tv.texts[name])
	}

	// They outlive the program.
	again := newModel([]string{"plugins"}, data)
	driver2 := open(t, again)
	click(t, driver2, "HTTP 请求")
	tv2 := again.tools[again.selected]
	finish(t, driver2, tv2)
	if got := strings.Join(tv2.options[saved], ","); got != "first,second" {
		t.Fatalf("after a restart: %q", got)
	}
}

func inputIndexOut(tv *toolView, id string) int {
	for i, out := range tv.tool.Outputs {
		if out.ID == id {
			return i
		}
	}
	return -1
}

// sendLong opens the HTTP tool and sends a request whose answer is 400 lines.
func sendLong(t *testing.T) (*model, *app.Driver) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		for i := range 400 {
			fmt.Fprintf(&b, "row %d\n", i)
		}
		w.Header().Set("X-Last", "the last header")
		io.WriteString(w, b.String())
	}))
	t.Cleanup(server.Close)
	m := newTestModel(t)
	driver := open(t, m)
	click(t, driver, "HTTP 请求")
	tv := m.tools[m.selected]
	finish(t, driver, tv)
	typeInto(t, driver, "URL", server.URL)
	click(t, driver, "发送")
	finish(t, driver, tv)
	return m, driver
}

// visible says the window painted value above the log bar.
func visible(t *testing.T, driver *app.Driver, value string) bool {
	t.Helper()
	bottom := bounds(t, driver, "日志").Y
	for _, text := range driver.Snapshot().Texts {
		if text.Value == value && text.Bounds.Y >= 0 && text.Bounds.Y+text.Bounds.Height <= bottom {
			return true
		}
	}
	return false
}

func TestALongBodyLeavesTheHeadersInView(t *testing.T) {
	_, driver := sendLong(t)
	for _, want := range []string{"200 OK", "row 0", "X-Last", "the last header"} {
		if !visible(t, driver, want) && !shows(driver, want) {
			t.Errorf("%q is not shown", want)
		}
	}
	if !visible(t, driver, "X-Last") {
		t.Fatal("the response headers are below the window")
	}
	if body := bounds(t, driver, "响应体"); body.Height < mainMinHeight-outputHead {
		t.Fatalf("the body is squeezed to %v", body)
	}
}

func TestTheOutputColumnScrolls(t *testing.T) {
	_, driver := sendLong(t)
	if err := driver.Resize(900, 480); err != nil { // the smallest the window goes
		t.Fatal(err)
	}
	if err := driver.Pump(); err != nil {
		t.Fatal(err)
	}
	if visible(t, driver, "X-Last") {
		t.Skip("the headers fit even at the smallest window; nothing to scroll to")
	}
	status := bounds(t, driver, "状态") // a short box: the wheel goes on to the column
	// One long turn: after a short one the pointer is over the body, which takes
	// the wheel itself until it is at its end.
	if err := driver.Scroll(gfx.ScrollEvent{X: status.X + status.Width/2, Y: status.Y + status.Height/2, DeltaY: 1000}); err != nil {
		t.Fatal(err)
	}
	if !visible(t, driver, "X-Last") {
		t.Fatal("scrolling the output column does not bring the headers into view")
	}
}

// clickLast presses the last control named name: the one in a dialog over the page.
func clickLast(t *testing.T, driver *app.Driver, name string) {
	t.Helper()
	hits := driver.Snapshot().Hits
	for i := len(hits) - 1; i >= 0; i-- {
		if hits[i].Label == name && !hits[i].IsLabel {
			b := hits[i].Bounds
			if err := driver.Click(b.X+b.Width/2, b.Y+b.Height/2); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no control named %q on screen", name)
}

func TestDeletingASavedRequestAsksFirst(t *testing.T) {
	m := newTestModel(t)
	driver := open(t, m)
	click(t, driver, "HTTP 请求")
	tv := m.tools[m.selected]
	finish(t, driver, tv)

	// With nothing chosen there is nothing to ask about: the plugin says so.
	click(t, driver, "删除已保存")
	if m.confirm.open {
		t.Fatal("asked to delete nothing")
	}
	if tv.running || !shows(driver, "请选择「已保存的请求」") {
		t.Fatalf("running %v, error %q", tv.running, tv.err)
	}

	tv.texts[inputIndex(t, tv, "url")], tv.texts[inputIndex(t, tv, "name")] = "http://127.0.0.1:1/", "keep me"
	click(t, driver, "保存")
	finish(t, driver, tv)
	saved := inputIndex(t, tv, "saved")

	click(t, driver, "删除已保存")
	if tv.running || !shows(driver, "确定删除已保存的请求「keep me」吗？") {
		t.Fatalf("the delete ran (%v) or asked nothing", tv.running)
	}
	clickLast(t, driver, "取消")
	if m.confirm.open || tv.running || len(tv.options[saved]) != 1 {
		t.Fatalf("cancel: open %v, running %v, saved %v", m.confirm.open, tv.running, tv.options[saved])
	}

	click(t, driver, "删除已保存")
	clickLast(t, driver, "删除已保存") // the dialog's button
	finish(t, driver, tv)
	if m.confirm.open || len(tv.options[saved]) != 0 || !shows(driver, "已删除「keep me」") {
		t.Fatalf("confirm: open %v, saved %v", m.confirm.open, tv.options[saved])
	}
}
