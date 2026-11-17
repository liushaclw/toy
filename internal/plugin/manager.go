package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Plugin is a directory with a plugin.toml. Err is set when the manifest could
// not be read; such a plugin is listed so the problem is visible, but never runs.
type Plugin struct {
	Dir      string
	Manifest *Manifest
	Err      error
}

// Path is a file of the plugin's directory.
func (p *Plugin) Path(name string) string { return filepath.Join(p.Dir, name) }

// Discover lists the plugins in the subdirectories of dirs. A plugin id found
// again in a later directory replaces the earlier one, so a user's directory can
// override a bundled plugin.
func Discover(dirs ...string) []*Plugin {
	var found []*Plugin
	index := map[string]int{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			pdir := filepath.Join(dir, e.Name())
			if _, err := os.Stat(filepath.Join(pdir, ManifestFile)); err != nil {
				continue
			}
			m, err := LoadManifest(pdir)
			if m == nil {
				m = &Manifest{ID: e.Name(), Name: e.Name()}
			}
			p := &Plugin{Dir: pdir, Manifest: m, Err: err}
			if i, ok := index[m.ID]; ok && m.ID != "" {
				found[i] = p
				continue
			}
			index[m.ID] = len(found)
			found = append(found, p)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Manifest.Name < found[j].Manifest.Name })
	return found
}

// EventKind says what an Event carries.
type EventKind int

const (
	EventLog EventKind = iota
	EventProgress
	EventDone
	EventClipboard
)

// Event is something a worker reports to the window. Events are collected by the
// Manager and read on the window's thread with Drain.
type Event struct {
	Kind     EventKind
	Job      int
	Plugin   string
	Text     string  // log line, clipboard text
	Fraction float64 // progress
	Result   Result  // done
	Err      error   // done
	Elapsed  time.Duration
}

// Manager runs the actions of a set of plugins.
type Manager struct {
	runtimes map[string]Runtime
	dataDir  string
	notify   func()

	mu      sync.Mutex
	events  []Event
	nextJob int
	cancels map[int]context.CancelFunc
	workers map[*Plugin]*worker
}

// NewManager makes a manager that starts plugins with runtimes (by manifest
// runtime name), keeps their stored values in dataDir and calls notify, from any
// goroutine, when events are waiting.
func NewManager(runtimes map[string]Runtime, dataDir string, notify func()) *Manager {
	return &Manager{
		runtimes: runtimes, dataDir: dataDir, notify: notify,
		cancels: map[int]context.CancelFunc{}, workers: map[*Plugin]*worker{},
	}
}

// Run queues action of tool with the given inputs and returns the job's number.
// The job's Events carry that number; the last is EventDone.
func (m *Manager) Run(p *Plugin, tool *Tool, action string, inputs map[string]any) int {
	m.mu.Lock()
	m.nextJob++
	id := m.nextJob
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if d := tool.Deadline(); d > 0 {
		ctx, cancel = context.WithTimeout(ctx, d)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	m.cancels[id] = cancel
	w := m.workers[p]
	if w == nil {
		w = &worker{m: m, plugin: p, jobs: make(chan job, 16)}
		m.workers[p] = w
		go w.loop()
	}
	m.mu.Unlock()
	w.jobs <- job{id: id, ctx: ctx, call: Call{Tool: tool.ID, Action: action, Inputs: inputs}, tool: tool}
	return id
}

// Cancel stops a job; it still ends with EventDone, carrying context.Canceled.
func (m *Manager) Cancel(job int) {
	m.mu.Lock()
	cancel := m.cancels[job]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Drain takes the events reported since the last call.
func (m *Manager) Drain() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := m.events
	m.events = nil
	return events
}

// Close stops every worker, cancelling what runs, and closes the instances.
// Close waits for the workers to finish.
func (m *Manager) Close() {
	m.mu.Lock()
	for _, cancel := range m.cancels {
		cancel()
	}
	workers := m.workers
	m.workers = map[*Plugin]*worker{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		w.done = &wg
		close(w.jobs)
	}
	wg.Wait()
}

func (m *Manager) emit(e Event) {
	m.mu.Lock()
	m.events = append(m.events, e)
	if e.Kind == EventDone {
		if cancel := m.cancels[e.Job]; cancel != nil {
			cancel()
			delete(m.cancels, e.Job)
		}
	}
	m.mu.Unlock()
	if m.notify != nil {
		m.notify()
	}
}

type job struct {
	id   int
	ctx  context.Context
	call Call
	tool *Tool
}

// worker runs one plugin's jobs in order on its own goroutine: script engines
// are not safe for concurrent use, and a plugin's actions see one another's
// effects in the order they were asked for.
type worker struct {
	m        *Manager
	plugin   *Plugin
	jobs     chan job
	instance Instance
	done     *sync.WaitGroup
}

func (w *worker) loop() {
	for j := range w.jobs {
		w.run(j)
	}
	if w.instance != nil {
		w.instance.Close()
	}
	if w.done != nil {
		w.done.Done()
	}
}

func (w *worker) run(j job) {
	began := time.Now()
	done := Event{Kind: EventDone, Job: j.id, Plugin: w.plugin.Manifest.ID}
	defer func() {
		if r := recover(); r != nil {
			done.Err = fmt.Errorf("plugin panicked: %v", r)
			w.reset()
		}
		done.Elapsed = time.Since(began)
		w.m.emit(done)
	}()
	if err := j.ctx.Err(); err != nil {
		done.Err = err
		return
	}
	if w.instance == nil {
		if w.plugin.Err != nil {
			done.Err = w.plugin.Err
			return
		}
		rt := w.m.runtimes[w.plugin.Manifest.Runtime]
		if rt == nil {
			done.Err = fmt.Errorf("no runtime %q", w.plugin.Manifest.Runtime)
			return
		}
		instance, err := rt.Start(w.plugin)
		if err != nil {
			done.Err = fmt.Errorf("start: %w", err)
			return
		}
		w.instance = instance
	}
	j.call.Host = &host{m: w.m, job: j.id, plugin: w.plugin}
	raw, err := w.instance.Run(j.ctx, j.call)
	if err == nil {
		err = j.ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %v", j.tool.Deadline())
		}
		// A script stopped half way, or a process that is gone, starts afresh next time.
		keeps, _ := w.instance.(interface{ KeepsAfterCancel() bool })
		if errors.Is(err, ErrStopped) || j.ctx.Err() != nil && (keeps == nil || !keeps.KeepsAfterCancel()) {
			w.reset()
		}
		done.Err = err
		return
	}
	done.Result, done.Err = Normalize(raw, j.tool)
}

func (w *worker) reset() {
	if w.instance != nil {
		w.instance.Close()
		w.instance = nil
	}
}

// ErrStopped is returned (wrapped) by an instance that cannot run again, such as
// a process that exited; the worker starts a new one for the next action.
var ErrStopped = errors.New("plugin stopped")

// host is Host for one job.
type host struct {
	m      *Manager
	job    int
	plugin *Plugin
}

func (h *host) Log(text string) {
	h.m.emit(Event{Kind: EventLog, Job: h.job, Plugin: h.plugin.Manifest.ID, Text: text})
}

func (h *host) Progress(f float64) {
	h.m.emit(Event{Kind: EventProgress, Job: h.job, Plugin: h.plugin.Manifest.ID, Fraction: f})
}

func (h *host) Clipboard(text string) {
	h.m.emit(Event{Kind: EventClipboard, Job: h.job, Plugin: h.plugin.Manifest.ID, Text: text})
}

func (h *host) Get(key string) (any, error) {
	values, err := h.m.load(h.plugin.Manifest.ID)
	return values[key], err
}

func (h *host) Set(key string, value any) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	values, err := h.m.loadLocked(h.plugin.Manifest.ID)
	if err != nil {
		return err
	}
	if value == nil {
		delete(values, key)
	} else {
		values[key] = value
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(h.m.dataDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(h.m.storePath(h.plugin.Manifest.ID), data, 0o644)
}

// storeMu serializes the stores' files.
var storeMu sync.Mutex

func (m *Manager) storePath(id string) string { return filepath.Join(m.dataDir, id+".json") }

func (m *Manager) load(id string) (map[string]any, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	return m.loadLocked(id)
}

func (m *Manager) loadLocked(id string) (map[string]any, error) {
	values := map[string]any{}
	data, err := os.ReadFile(m.storePath(id))
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return values, err
	}
	return values, json.Unmarshal(data, &values)
}
