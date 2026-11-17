// Package toyplugin writes an exec plugin for toy in Go. It depends on nothing
// but the standard library, so it can be copied into a module of its own.
//
//	func main() {
//		toyplugin.Handle("run", func(c *toyplugin.Call) (any, error) {
//			c.Log("working")
//			return map[string]any{"result": strings.ToUpper(c.String("text"))}, nil
//		})
//		toyplugin.Serve()
//	}
//
// Serve reads requests from standard input until it closes. Handlers run one at a
// time; Call.Context is cancelled when the user presses Cancel. Write logs with
// Call.Log (or to standard error), never to standard output: it is the protocol.
package toyplugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
)

// Handler answers one action: a string, or a map from output id to value (a
// string, a number, a list of lists or of maps for a table).
type Handler func(c *Call) (any, error)

var (
	handlers = map[string]Handler{}
	fallback Handler
)

// Handle registers h for the action id ("tool/action" or just "action").
// The id "*" answers every action no other handler takes.
func Handle(id string, h Handler) {
	if id == "*" {
		fallback = h
		return
	}
	handlers[id] = h
}

// Call is one action the user started.
type Call struct {
	Context context.Context
	Tool    string
	Action  string
	Inputs  map[string]any
	conn    *conn
}

// String is a text input (or any input, as text).
func (c *Call) String(id string) string {
	switch v := c.Inputs[id].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// Number is a number input.
func (c *Call) Number(id string) float64 {
	f, _ := c.Inputs[id].(float64)
	return f
}

// Bool is a switch input.
func (c *Call) Bool(id string) bool {
	b, _ := c.Inputs[id].(bool)
	return b
}

// Log writes a line to the window's log.
func (c *Call) Log(format string, args ...any) {
	c.conn.notify("log", map[string]any{"text": fmt.Sprintf(format, args...)})
}

// Progress shows how far the action is, 0..1.
func (c *Call) Progress(fraction float64) {
	c.conn.notify("progress", map[string]any{"fraction": fraction})
}

// Clipboard puts text on the clipboard.
func (c *Call) Clipboard(text string) { c.conn.notify("clipboard", map[string]any{"text": text}) }

// Get reads a value the plugin stored with Set.
func (c *Call) Get(key string) (any, error) {
	raw, err := c.conn.request("get", map[string]any{"key": key})
	if err != nil {
		return nil, err
	}
	var v any
	err = json.Unmarshal(raw, &v)
	return v, err
}

// Set stores a value under key for later runs; nil deletes it.
func (c *Call) Set(key string, value any) error {
	_, err := c.conn.request("set", map[string]any{"key": key, "value": value})
	return err
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

type conn struct {
	out     io.Writer
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan message
	cancels map[int64]context.CancelFunc
	runs    map[int64]context.Context
}

// Serve answers the host on standard input and output until input ends.
func Serve() {
	if err := ServeIO(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ServeIO is Serve over other streams (for tests).
func ServeIO(in io.Reader, out io.Writer) error {
	c := &conn{out: out, pending: map[int64]chan message{}, cancels: map[int64]context.CancelFunc{}, runs: map[int64]context.Context{}}
	jobs := make(chan message, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for m := range jobs {
			c.serve(m)
		}
	}()
	reader := bufio.NewReaderSize(in, 64<<10)
	var err error
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var m message
			if jerr := json.Unmarshal(line, &m); jerr == nil {
				c.dispatch(m, jobs)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				err = rerr
			}
			break
		}
	}
	c.mu.Lock()
	for _, cancel := range c.cancels {
		cancel()
	}
	for id, reply := range c.pending { // the host is gone: nothing will answer
		reply <- message{ID: &id, Error: &rpcError{Code: -32000, Message: "host closed the connection"}}
	}
	c.mu.Unlock()
	close(jobs)
	<-done
	return err
}

// dispatch handles a message on the reading goroutine: answers to our requests
// and cancellations act at once, requests queue for the serving goroutine.
func (c *conn) dispatch(m message, jobs chan<- message) {
	switch {
	case m.Method == "" && m.ID != nil:
		c.mu.Lock()
		reply := c.pending[*m.ID]
		c.mu.Unlock()
		if reply != nil {
			reply <- m
		}
	case m.Method == "$/cancel":
		var p struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(m.Params, &p)
		c.mu.Lock()
		cancel := c.cancels[p.ID]
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case m.Method == "initialize":
		c.reply(m.ID, map[string]any{"protocol": 1}, nil)
	case m.Method == "run":
		ctx, cancel := context.WithCancel(context.Background())
		c.mu.Lock()
		c.cancels[*m.ID], c.runs[*m.ID] = cancel, ctx
		c.mu.Unlock()
		jobs <- m
	case m.ID != nil:
		c.reply(m.ID, nil, &rpcError{Code: -32601, Message: "no method " + m.Method})
	}
}

func (c *conn) serve(m message) {
	c.mu.Lock()
	ctx, cancel := c.runs[*m.ID], c.cancels[*m.ID]
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		delete(c.cancels, *m.ID)
		delete(c.runs, *m.ID)
		c.mu.Unlock()
	}()
	var p struct {
		Tool   string         `json:"tool"`
		Action string         `json:"action"`
		Inputs map[string]any `json:"inputs"`
	}
	json.Unmarshal(m.Params, &p)
	call := &Call{Context: ctx, Tool: p.Tool, Action: p.Action, Inputs: p.Inputs, conn: c}
	h := handlers[p.Tool+"/"+p.Action]
	if h == nil {
		h = handlers[p.Action]
	}
	if h == nil {
		h = fallback
	}
	if h == nil {
		c.reply(m.ID, nil, &rpcError{Code: -32601, Message: "no handler for " + p.Tool + "/" + p.Action})
		return
	}
	result, err := safely(h, call)
	if err != nil {
		c.reply(m.ID, nil, &rpcError{Code: -32000, Message: err.Error()})
		return
	}
	c.reply(m.ID, result, nil)
}

func safely(h Handler, call *Call) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return h(call)
}

func (c *conn) reply(id *int64, result any, e *rpcError) {
	m := message{ID: id, Error: e}
	if e == nil {
		data, err := json.Marshal(result)
		if err != nil {
			m.Error = &rpcError{Code: -32000, Message: "result: " + err.Error()}
		} else {
			m.Result = data
		}
	}
	c.write(m)
}

func (c *conn) notify(method string, params any) {
	data, _ := json.Marshal(params)
	c.write(message{Method: method, Params: data})
}

func (c *conn) request(method string, params any) (json.RawMessage, error) {
	data, _ := json.Marshal(params)
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	reply := make(chan message, 1)
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	c.write(message{ID: &id, Method: method, Params: data})
	m := <-reply
	if m.Error != nil {
		return nil, errors.New(m.Error.Message)
	}
	return m.Result, nil
}

func (c *conn) write(m message) {
	m.JSONRPC = "2.0"
	data, _ := json.Marshal(m)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.out.Write(append(data, '\n'))
}
