package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vivaui/viva/app"
)

func menuItem(t *testing.T, items []app.MenuItem, id string) app.MenuItem {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("the tray menu has no %q", id)
	return app.MenuItem{}
}

func TestTheTrayMenuOpensATool(t *testing.T) {
	m := newTestModel(t)
	driver := open(t, m)
	shown := false
	m.tray.show = func() { shown = true }

	tools := menuItem(t, m.trayMenu(), "tools").Submenu
	if len(tools) != len(m.tools) {
		t.Fatalf("the tray lists %d tools of %d", len(tools), len(m.tools))
	}
	menuItem(t, tools, "codec/codec").OnSelect()
	if err := driver.Pump(); err != nil {
		t.Fatal(err)
	}
	if !shown || m.tools[m.selected].title != "编解码" || !shows(driver, "Base64") {
		t.Fatalf("choosing 编解码 in the tray: shown %v, selected %q", shown, m.tools[m.selected].title)
	}
	if !menuItem(t, menuItem(t, m.trayMenu(), "tools").Submenu, "codec/codec").Checked {
		t.Fatal("the tray does not check the open tool")
	}
}

func TestClosingHidesToTheTrayWhileTheUserWantsIt(t *testing.T) {
	m := newTestModel(t)
	m.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	var hid, closed int
	hide, allow := func() { hid++ }, func() { closed++ }

	m.closeRequested(hide, allow) // no tray: the window closes
	if hid != 0 || closed != 1 {
		t.Fatalf("without a tray: hid %d, closed %d", hid, closed)
	}
	m.tray.item = &app.StatusItem{}
	m.closeRequested(hide, allow)
	if hid != 1 || closed != 1 {
		t.Fatalf("with a tray: hid %d, closed %d", hid, closed)
	}

	keep := menuItem(t, m.trayMenu(), "keep")
	if !keep.Checked {
		t.Fatal("keeping to the tray is not the default")
	}
	keep.OnSelect()
	m.closeRequested(hide, allow)
	if hid != 1 || closed != 2 {
		t.Fatalf("after turning it off: hid %d, closed %d", hid, closed)
	}
	if loadSettings(m.settingsPath).KeepInTray {
		t.Fatal("the choice was not saved")
	}
}

func TestTheTrayTooltipCountsRunningJobs(t *testing.T) {
	m := newTestModel(t)
	for _, tv := range m.tools {
		tv.running = false // the init of the tool shown first
	}
	if got := m.trayTooltip(); got != appTitle {
		t.Fatalf("idle tooltip %q", got)
	}
	m.tools[0].running, m.tools[1].running = true, true
	if got, want := m.trayTooltip(), appTitle+" · 2 个任务运行中"; got != want {
		t.Fatalf("tooltip %q, want %q", got, want)
	}
}

// What the single-instance claim does when the program is launched again.
func TestASecondLaunchShowsTheWindow(t *testing.T) {
	m := newTestModel(t)
	driver := open(t, m)
	shown := make(chan struct{}, 1)
	m.tray.show = func() { shown <- struct{}{} }

	m.reopen.Store(true)
	m.inv.Invalidate()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-shown:
			return
		case <-driver.Wakes():
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatal("a second launch did not bring the window back")
		}
		if err := driver.Pump(); err != nil {
			t.Fatal(err)
		}
	}
}
