package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/gfx"
	"github.com/vivaui/viva/ui"
)

const appTitle = "toy 工具箱"

// settings are the application's own choices, kept in settings.json beside the
// plugins' data.
type settings struct {
	// KeepInTray makes closing the window hide it to the tray; quitting is
	// then the tray menu's 退出.
	KeepInTray bool `json:"keep_in_tray"`
}

func defaultSettings() settings { return settings{KeepInTray: true} }

func loadSettings(path string) settings {
	s := defaultSettings()
	if path == "" {
		return s
	}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &s)
	}
	return s
}

func (m *model) saveSettings() {
	if m.settingsPath == "" {
		return
	}
	data, _ := json.MarshalIndent(m.settings, "", "  ")
	os.MkdirAll(filepath.Dir(m.settingsPath), 0o755)
	if err := os.WriteFile(m.settingsPath, data, 0o644); err != nil {
		m.logf("", "保存设置失败: %v", err)
	}
}

// tray is what the tray icon acts on: the window to show, how to end the
// program. Both are funcs so tests can stand in for them.
type tray struct {
	show func()
	quit func()
	item *app.StatusItem // nil without a status area, or in tests

	tooltip string // last set, to skip setting the same one
}

// trayMenu is built each time the tray's menu opens, so it shows the tools and
// the setting as they are then. It runs on the event-loop thread, the window's.
func (m *model) trayMenu() []app.MenuItem {
	tools := make([]app.MenuItem, 0, len(m.tools))
	for i, tv := range m.tools {
		title := tv.title
		if tv.running {
			title += "（运行中）"
		}
		tools = append(tools, app.MenuItem{
			ID: tv.key, Title: title, Disabled: tv.broken != "", Checked: i == m.selected,
			OnSelect: func() { m.openTool(tv.key) },
		})
	}
	if len(tools) == 0 {
		tools = append(tools, app.MenuItem{Title: "没有工具", Disabled: true})
	}
	return []app.MenuItem{
		{ID: "show", Title: "显示窗口", OnSelect: m.tray.show},
		{ID: "tools", Title: "工具", Submenu: tools},
		{Separator: true},
		{ID: "keep", Title: "关闭窗口时留在托盘", Checked: m.settings.KeepInTray, OnSelect: func() {
			m.settings.KeepInTray = !m.settings.KeepInTray
			m.saveSettings()
		}},
		{Separator: true},
		{ID: "quit", Title: "退出", OnSelect: m.tray.quit},
	}
}

// openTool selects a tool and brings the window forward.
func (m *model) openTool(key string) {
	for i, tv := range m.tools {
		if tv.key == key {
			m.selected, m.query = i, ""
			m.initTool(tv)
		}
	}
	m.inv.Invalidate()
	if m.tray.show != nil {
		m.tray.show()
	}
}

// closeRequested hides the window to the tray, unless there is no tray or the
// user chose to quit on close.
func (m *model) closeRequested(hide, allow func()) {
	if m.tray.item != nil && m.settings.KeepInTray {
		hide()
		return
	}
	allow()
}

// trayTooltip says how many jobs run, so a hidden window's work shows.
func (m *model) trayTooltip() string {
	running := 0
	for _, tv := range m.tools {
		if tv.running {
			running++
		}
	}
	if running == 0 {
		return appTitle
	}
	return appTitle + " · " + itoa(running) + " 个任务运行中"
}

func (m *model) updateTray() {
	if tip := m.trayTooltip(); tip != m.tray.tooltip {
		m.tray.tooltip = tip
		m.tray.item.SetTooltip(tip) // nil-safe
	}
}

// runApp opens the window and puts the icon in the tray. A desktop with no
// status area gets a window that closes as usual.
func runApp(m *model) error {
	return app.RunApplication(func(a *app.Application) error {
		var window *app.Window
		m.tray.show = func() { window.Show() }
		m.tray.quit = a.Quit

		config := pageConfig(m)
		config.OnCloseRequest = func(allow func()) { m.closeRequested(window.Hide, allow) }
		closed := config.OnClosed
		config.OnClosed = func() {
			m.tray.item.Remove()
			closed()
		}
		var err error
		if window, err = a.OpenWindow(config, new(gfx.Canvas)); err != nil {
			return err
		}
		m.inv.Invalidate() // what plugins reported before the window could hear it (an init)
		icon := windowIcon()
		m.tray.tooltip = appTitle
		item, err := a.AddStatusItem(app.StatusItemConfig{
			Tooltip: appTitle,
			Icon:    icon.StatusIcon(32),
			OnClick: func(app.StatusClick) { window.Show() }, // the menu is the other button's
			Menu:    m.trayMenu,
		})
		if err != nil {
			m.logf("", "没有系统托盘，关闭窗口即退出: %v", err)
			return nil
		}
		m.tray.item = item
		return nil
	})
}

// pageConfig is the window's config with the page wired to it.
func pageConfig(m *model) app.Config {
	return ui.NewPage(ui.PageConfig{View: m.page}).Config(config(m))
}
