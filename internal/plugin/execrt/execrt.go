// Package execrt runs a plugin as a separate process that speaks JSON-RPC 2.0,
// one JSON message per line, over its standard input and output. Any language
// can implement it; sdk/toyplugin is the Go side, and PLUGINS.md describes it.
//
// The host sends the requests initialize {protocol, plugin, dir} and run
// {tool, action, inputs} (whose result is what the action answers), and the
// notification $/cancel {id}. The plugin may send the notifications log {text},
// progress {fraction} and clipboard {text}, and the requests get {key} and set
// {key, value}. Lines on standard error are logged. The process is started on
// the first action, kept for the next ones, and started again if it exits.
package execrt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"toy/internal/plugin"
)

// Protocol is the version the host speaks.
const Protocol = 1

// Runtime is the "exec" runtime.
type Runtime struct {
	// StartTimeout bounds the answer to initialize (default 30s: `go run` compiles).
	StartTimeout time.Duration
	// CancelGrace is how long a cancelled run may take to answer before its
	// process is killed (default 2s).
	CancelGrace time.Duration
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

type process struct {
	cmd    *exec.Cmd
	plugin *plugin.Plugin
	grace  time.Duration

	writeMu sync.Mutex
	stdin   io.WriteCloser
	outputs []io.Closer

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan message
	host    plugin.Host

	exited    chan struct{}
	exitErr   error
	closeOnce sync.Once
}

func (r Runtime) Start(p *plugin.Plugin) (plugin.Instance, error) {
	args := append([]string(nil), p.Manifest.Command...)
	args[0] = resolve(p.Dir, args[0])
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(), "TOY_PLUGIN_DIR="+p.Dir, "TOY_PLUGIN_ID="+p.Manifest.ID)
	hideWindow(cmd)
	// The pipes are the host's own files rather than the Cmd's: a process the
	// plugin starts in turn (`go run` does) may hold them after the plugin's own
	// process is gone, and exited must not wait for that.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW
	err = cmd.Start()
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()
	if err != nil {
		stdinW.Close()
		stdoutR.Close()
		stderrR.Close()
		return nil, err
	}
	pr := &process{
		cmd: cmd, plugin: p, stdin: stdinW, outputs: []io.Closer{stdoutR, stderrR}, grace: r.CancelGrace,
		pending: map[int64]chan message{}, host: plugin.NoHost, exited: make(chan struct{}),
	}
	if pr.grace == 0 {
		pr.grace = 2 * time.Second
	}
	go pr.read(stdoutR)
	go pr.readErrors(stderrR)
	go func() {
		err := cmd.Wait()
		pr.mu.Lock()
		pr.exitErr = err
		pr.mu.Unlock()
		close(pr.exited)
	}()

	timeout := r.StartTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	init := map[string]any{"protocol": Protocol, "plugin": p.Manifest.ID, "dir": p.Dir}
	if _, err := pr.call(ctx, "initialize", init, false); err != nil {
		pr.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return pr, nil
}

// resolve runs a command named by a file of the plugin's directory from there;
// any other name is looked up on PATH.
func resolve(dir, name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	candidates := []string{name}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		candidates = append(candidates, name+".exe", name+".cmd", name+".bat")
	}
	for _, c := range candidates {
		path := filepath.Join(dir, c)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	return name
}

func (pr *process) Run(ctx context.Context, call plugin.Call) (any, error) {
	pr.mu.Lock()
	pr.host = call.Host
	pr.mu.Unlock()
	defer func() {
		pr.mu.Lock()
		pr.host = plugin.NoHost
		pr.mu.Unlock()
	}()
	raw, err := pr.call(ctx, "run", map[string]any{"tool": call.Tool, "action": call.Action, "inputs": call.Inputs}, true)
	if err != nil {
		return nil, err
	}
	var v any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("result: %w", err)
		}
	}
	return v, nil
}

// KeepsAfterCancel tells the worker a cancelled run leaves the process usable:
// when it does not answer in time, Run returns plugin.ErrStopped instead.
func (pr *process) KeepsAfterCancel() bool { return true }

// call sends a request and waits for its answer. With cancelable, ctx being done
// sends $/cancel and waits the grace period for the answer, then kills.
func (pr *process) call(ctx context.Context, method string, params any, cancelable bool) (json.RawMessage, error) {
	pr.mu.Lock()
	pr.nextID++
	id := pr.nextID
	reply := make(chan message, 1)
	pr.pending[id] = reply
	pr.mu.Unlock()
	defer func() {
		pr.mu.Lock()
		delete(pr.pending, id)
		pr.mu.Unlock()
	}()
	if err := pr.send(message{ID: &id, Method: method, Params: mustJSON(params)}); err != nil {
		return nil, pr.stopped(err)
	}
	answer := func(m message) (json.RawMessage, error) {
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	}
	select {
	case m := <-reply:
		return answer(m)
	case <-pr.exited:
		return nil, pr.stopped(nil)
	case <-ctx.Done():
	}
	if cancelable {
		pr.send(message{Method: "$/cancel", Params: mustJSON(map[string]any{"id": id})})
		select {
		case <-reply:
			return nil, ctx.Err()
		case <-pr.exited:
		case <-time.After(pr.grace):
		}
	}
	pr.Close()
	return nil, fmt.Errorf("%w: %w (killed)", ctx.Err(), plugin.ErrStopped)
}

func (pr *process) stopped(cause error) error {
	pr.mu.Lock()
	exitErr := pr.exitErr
	pr.mu.Unlock()
	switch {
	case exitErr != nil:
		return fmt.Errorf("%w: process exited: %v", plugin.ErrStopped, exitErr)
	case cause != nil:
		return fmt.Errorf("%w: %v", plugin.ErrStopped, cause)
	}
	return fmt.Errorf("%w: process exited", plugin.ErrStopped)
}

func (pr *process) send(m message) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	pr.writeMu.Lock()
	defer pr.writeMu.Unlock()
	_, err = pr.stdin.Write(append(data, '\n'))
	return err
}

func (pr *process) currentHost() plugin.Host {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	return pr.host
}

func (pr *process) read(r io.Reader) {
	reader := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			pr.handle(line)
		}
		if err != nil {
			return
		}
	}
}

func (pr *process) handle(line []byte) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		// Something printed to stdout that is not the protocol: show it rather than lose it.
		pr.currentHost().Log(string(line))
		return
	}
	if m.Method == "" {
		if m.ID == nil {
			return
		}
		pr.mu.Lock()
		reply := pr.pending[*m.ID]
		pr.mu.Unlock()
		if reply != nil {
			reply <- m
		}
		return
	}
	host := pr.currentHost()
	var p struct {
		Text     string  `json:"text"`
		Fraction float64 `json:"fraction"`
		Key      string  `json:"key"`
		Value    any     `json:"value"`
	}
	json.Unmarshal(m.Params, &p)
	var result any
	var err error
	switch m.Method {
	case "log":
		host.Log(p.Text)
	case "progress":
		host.Progress(p.Fraction)
	case "clipboard":
		host.Clipboard(p.Text)
	case "get":
		result, err = host.Get(p.Key)
	case "set":
		err = host.Set(p.Key, p.Value)
	default:
		err = &rpcError{Code: -32601, Message: "no method " + m.Method}
	}
	if m.ID == nil {
		return
	}
	reply := message{ID: m.ID, Result: mustJSON(result)}
	if err != nil {
		var re *rpcError
		if !errors.As(err, &re) {
			re = &rpcError{Code: -32000, Message: err.Error()}
		}
		reply.Result, reply.Error = nil, re
	}
	pr.send(reply)
}

func (pr *process) readErrors(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		pr.currentHost().Log(scanner.Text())
	}
}

// Close ends the process: its input is closed, which a plugin takes as the end,
// and it is killed if it has not exited a moment later.
func (pr *process) Close() error {
	pr.closeOnce.Do(func() {
		pr.stdin.Close()
		select {
		case <-pr.exited:
		case <-time.After(time.Second):
			pr.cmd.Process.Kill()
			<-pr.exited
		}
		for _, c := range pr.outputs {
			c.Close()
		}
	})
	return nil
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(fmt.Sprint(v))
	}
	return data
}
