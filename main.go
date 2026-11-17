package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/app/singleinstance"
	"github.com/vivaui/viva/ui"

	"toy/internal/plugin"
	"toy/internal/plugin/execrt"
	"toy/internal/plugin/jsrt"
	"toy/internal/plugin/luart"
)

// model is everything the window shows. page only reads it; clicks, handled
// where their controls are declared, and wake (events from plugin workers,
// on the window's thread) are what change it.
type model struct {
	dirs    []string // where plugins are found, later ones overriding earlier
	dataDir string   // plugins' stored values
	inv     *app.Invalidator
	mgr     *plugin.Manager

	tools    []*toolView
	selected int
	query    string

	logs    []string
	showLog bool
	split   ui.SplitterState // sidebar | tool
	ioSplit ui.SplitterState // inputs | outputs
	logPane ui.SplitterState // tool over log, while the log is open
	toaster ui.Toaster
	confirm confirmation // an action waiting for the user's yes

	changed atomic.Bool  // the plugin directories changed on disk
	reopen  *atomic.Bool // another launch asked for the window

	settings     settings
	settingsPath string // "" keeps the settings in memory only (tests)
	tray         tray
}

const maxLogLines = 500

func newModel(dirs []string, dataDir string) *model {
	return newModelWith(dirs, dataDir, &app.Invalidator{}, &atomic.Bool{})
}

// newModelWith takes the invalidator and reopen flag the single-instance claim
// was given before the model existed.
func newModelWith(dirs []string, dataDir string, inv *app.Invalidator, reopen *atomic.Bool) *model {
	m := &model{
		dirs: dirs, dataDir: dataDir, inv: inv, reopen: reopen,
		split:    ui.SplitterState{Pos: 240},
		ioSplit:  ui.SplitterState{Pos: 400, Ratio: 0.45},
		logPane:  ui.SplitterState{Pos: 520, Ratio: 0.75},
		settings: defaultSettings(),
	}
	m.load()
	return m
}

func runtimes() map[string]plugin.Runtime {
	return map[string]plugin.Runtime{"lua": luart.Runtime{}, "js": jsrt.Runtime{}, "exec": execrt.Runtime{}}
}

// load finds the plugins and builds a view per tool. A reload keeps what was
// typed into a tool that still exists, and the selection.
func (m *model) load() {
	if old := m.mgr; old != nil {
		go old.Close() // cancels what runs; its late events are dropped with it
	}
	m.mgr = plugin.NewManager(runtimes(), m.dataDir, m.inv.Invalidate)
	previous := map[string]*toolView{}
	for _, tv := range m.tools {
		previous[tv.key] = tv
	}
	selectedKey := ""
	if m.selected < len(m.tools) {
		selectedKey = m.tools[m.selected].key
	}
	m.tools = m.tools[:0:0]
	for _, p := range plugin.Discover(m.dirs...) {
		if p.Err != nil {
			m.tools = append(m.tools, newBrokenView(p))
			continue
		}
		for i := range p.Manifest.Tools {
			tv := newToolView(p, &p.Manifest.Tools[i])
			tv.keepValues(previous[tv.key])
			m.tools = append(m.tools, tv)
		}
	}
	m.selected = 0
	for i, tv := range m.tools {
		if tv.key == selectedKey {
			m.selected = i
		}
	}
	m.logf("", "已加载 %d 个工具（%s）", len(m.tools), strings.Join(m.dirs, "; "))
	if m.selected < len(m.tools) {
		m.initTool(m.tools[m.selected])
	}
}

func (m *model) logf(source, format string, args ...any) {
	line := time.Now().Format("15:04:05") + "  "
	if source != "" {
		line += "[" + source + "] "
	}
	m.logs = append(m.logs, line+fmt.Sprintf(format, args...))
	if over := len(m.logs) - maxLogLines; over > 0 {
		m.logs = append(m.logs[:0], m.logs[over:]...)
	}
}

// wake runs on the window's thread when a worker has reported something.
func (m *model) wake() {
	if m.reopen.Swap(false) && m.tray.show != nil {
		m.tray.show()
	}
	if m.changed.Swap(false) {
		m.load()
	}
	for _, e := range m.mgr.Drain() {
		switch e.Kind {
		case plugin.EventLog:
			m.logf(e.Plugin, "%s", e.Text)
		case plugin.EventProgress:
			if tv := m.byJob(e.Job); tv != nil {
				tv.progress = e.Fraction
			}
		case plugin.EventClipboard:
			if err := app.SetClipboardText(e.Text); err != nil {
				m.logf(e.Plugin, "剪贴板: %v", err)
			}
		case plugin.EventDone:
			tv := m.byJob(e.Job)
			if tv == nil {
				continue
			}
			tv.finish(e)
			if e.Err != nil {
				m.logf(e.Plugin, "%s 失败: %v", tv.tool.Name, e.Err)
			}
		}
	}
	m.updateTray()
}

func (m *model) byJob(job int) *toolView {
	for _, tv := range m.tools {
		if tv.running && tv.job == job {
			return tv
		}
	}
	return nil
}

// start runs an action of a button.
func (m *model) start(tv *toolView, a *plugin.Action) { m.run(tv, a.ID, a.Requires, true, false) }

// confirmation is the dialog an action with Confirm opens before it runs.
type confirmation struct {
	open   bool
	tool   *toolView
	action *plugin.Action
	text   string // the question, its {id}s filled in
}

// press runs a button's action, or first asks when the action says to.
func (m *model) press(tv *toolView, a *plugin.Action) {
	// A question about an input left empty has nothing to ask about: the action
	// runs at once and says what is missing.
	if a.Confirm == "" || tv.namesEmpty(a.Confirm) {
		m.start(tv, a)
		return
	}
	if _, err := tv.inputs(true, a.Requires); err != nil { // nothing to ask about yet
		tv.err, tv.status = err.Error(), ""
		return
	}
	m.confirm = confirmation{open: true, tool: tv, action: a, text: tv.fill(a.Confirm)}
}

// initTool runs the tool's init action the first time it is shown.
func (m *model) initTool(tv *toolView) {
	if tv.tool == nil || tv.tool.Init == "" || tv.initDone || tv.running {
		return
	}
	tv.initDone = true
	m.run(tv, tv.tool.Init, nil, false, true)
}

// changedInput runs the action a select or switch names for its changes.
func (m *model) changedInput(tv *toolView, in *plugin.Input) {
	if in.OnChange != "" && !tv.running {
		m.run(tv, in.OnChange, nil, false, false)
	}
}

// run starts action; check enforces the form's rules (need, when not nil, the
// inputs it requires), quiet leaves the status line alone.
func (m *model) run(tv *toolView, action string, need []string, check, quiet bool) {
	inputs, err := tv.inputs(check, need)
	if err != nil {
		tv.err, tv.status = err.Error(), ""
		return
	}
	if !quiet {
		tv.err, tv.status = "", ""
	}
	tv.quiet = quiet
	tv.running, tv.progress = true, -1
	tv.job = m.mgr.Run(tv.plugin, tv.tool, action, inputs)
	m.updateTray()
}

// watch notices edits under the plugin directories (by polling: a handful of
// small files) and asks the window to reload.
func (m *model) watch(every time.Duration) {
	last := signature(m.dirs)
	for range time.Tick(every) {
		if sig := signature(m.dirs); sig != last {
			last = sig
			m.changed.Store(true)
			m.inv.Invalidate()
		}
	}
}

func signature(dirs []string) string {
	var b strings.Builder
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if info, err := d.Info(); err == nil && !d.IsDir() {
				fmt.Fprintf(&b, "%s|%d|%d\n", path, info.Size(), info.ModTime().UnixNano())
			}
			return nil
		})
	}
	return b.String()
}

func config(m *model) app.Config {
	return app.Config{
		Title: appTitle, Width: 1100, Height: 720, MinWidth: 720, MinHeight: 480, Icon: windowIcon(),
		Invalidator: m.inv, OnWake: m.wake,
		OnClosed: func() { m.mgr.Close() },
	}
}

// pluginDirs are the bundled plugins beside the program, those of the working
// directory (for `viva dev .`), and the user's own, in that order.
func pluginDirs() []string {
	var dirs []string
	add := func(dir string) {
		if abs, err := filepath.Abs(dir); err == nil {
			for _, d := range dirs {
				if strings.EqualFold(d, abs) {
					return
				}
			}
			dirs = append(dirs, abs)
		}
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(exe), "plugins"))
	}
	add("plugins")
	if cfg, err := os.UserConfigDir(); err == nil {
		user := filepath.Join(cfg, "toy", "plugins")
		os.MkdirAll(user, 0o755)
		add(user)
	}
	return dirs
}

func dataDir() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = os.TempDir()
	}
	return filepath.Join(cfg, "toy", "data")
}

// appID names the application to its other copies; it is viva.toml's id.
const appID = "dev.example.toy"

func main() {
	// One copy per user: a second launch wakes the first and ends. The claim
	// is made before anything else, so a second copy starts no plugin.
	reopen := &atomic.Bool{}
	inv := &app.Invalidator{}
	claim, primary, err := singleinstance.SingleInstance(appID, os.Args[1:], func([]string) {
		reopen.Store(true)
		inv.Invalidate()
	})
	if err == nil && !primary {
		return
	}
	if err != nil {
		log.Printf("单实例检查失败，照常启动: %v", err)
	}
	defer claim.Close() // nil-safe

	data := dataDir()
	m := newModelWith(pluginDirs(), data, inv, reopen)
	m.settingsPath = filepath.Join(filepath.Dir(data), "settings.json")
	m.settings = loadSettings(m.settingsPath)
	go m.watch(time.Second)
	if err := runApp(m); err != nil {
		log.Fatal(err)
	}
}
