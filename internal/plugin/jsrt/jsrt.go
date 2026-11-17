// Package jsrt runs JavaScript (ES2015+) plugins in the process with goja.
//
// The entry script declares a global function run(action, inputs, tool), which
// returns a string or an object keyed by output id. The global host object
// offers log, progress, clipboard, get, set, readFile, writeFile, http and httpGet;
// console.log goes to the log. Host calls are synchronous: there is no event loop.
package jsrt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dop251/goja"

	"toy/internal/plugin"
)

// Runtime is the "js" runtime.
type Runtime struct{}

func (Runtime) Start(p *plugin.Plugin) (plugin.Instance, error) {
	src, err := os.ReadFile(p.Path(p.Manifest.Entry))
	if err != nil {
		return nil, err
	}
	in := &instance{vm: goja.New(), plugin: p, host: plugin.NoHost, ctx: context.Background()}
	in.install()
	if _, err := in.vm.RunScript(p.Manifest.Entry, string(src)); err != nil {
		return nil, err
	}
	if _, ok := goja.AssertFunction(in.vm.Get("run")); !ok {
		return nil, fmt.Errorf("%s declares no function run(action, inputs, tool)", p.Manifest.Entry)
	}
	return in, nil
}

type instance struct {
	vm     *goja.Runtime
	plugin *plugin.Plugin
	ctx    context.Context
	host   plugin.Host
}

func (in *instance) Run(ctx context.Context, call plugin.Call) (any, error) {
	in.ctx, in.host = ctx, call.Host
	// The watcher interrupts the script when ctx ends during the run. It must be
	// gone, and its interrupt cleared, before Run returns: ctx is cancelled as the
	// job completes, and an interrupt landing after this run would stop the next.
	stop, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			in.vm.Interrupt(ctx.Err())
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		<-watched
		in.vm.ClearInterrupt()
	}()
	run, _ := goja.AssertFunction(in.vm.Get("run"))
	ret, err := run(goja.Undefined(), in.vm.ToValue(call.Action), in.vm.ToValue(call.Inputs), in.vm.ToValue(call.Tool))
	if err != nil {
		var interrupted *goja.InterruptedError
		if errors.As(err, &interrupted) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if p, ok := ret.Export().(*goja.Promise); ok {
		switch p.State() {
		case goja.PromiseStateFulfilled:
			ret = p.Result()
		case goja.PromiseStateRejected:
			return nil, fmt.Errorf("%v", p.Result())
		default:
			return nil, fmt.Errorf("run returned a promise that never settles (there is no event loop)")
		}
	}
	return ret.Export(), nil
}

func (in *instance) Close() error {
	in.vm.Interrupt("closed")
	return nil
}

func (in *instance) install() {
	vm := in.vm
	throw := func(err error) {
		if err != nil {
			panic(vm.NewGoError(err))
		}
	}
	log := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			parts[i] = format(a)
		}
		in.host.Log(strings.Join(parts, " "))
		return goja.Undefined()
	}
	host := vm.NewObject()
	host.Set("log", log)
	host.Set("progress", func(f float64) { in.host.Progress(f) })
	host.Set("clipboard", func(s string) { in.host.Clipboard(s) })
	host.Set("get", func(key string) any {
		v, err := in.host.Get(key)
		throw(err)
		return v
	})
	host.Set("set", func(key string, v goja.Value) {
		var value any
		if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
			value = v.Export()
		}
		throw(in.host.Set(key, value))
	})
	host.Set("readFile", func(name string) string {
		s, err := in.plugin.ReadFile(name)
		throw(err)
		return s
	})
	host.Set("writeFile", func(name, content string) { throw(in.plugin.WriteFile(name, content)) })
	host.Set("http", func(request map[string]any) map[string]any {
		resp, err := plugin.HTTP(in.ctx, request)
		throw(err)
		return resp
	})
	host.Set("httpGet", func(url string) string {
		s, err := plugin.HTTPGet(in.ctx, url)
		throw(err)
		return s
	})
	vm.Set("host", host)
	console := vm.NewObject()
	for _, name := range []string{"log", "info", "warn", "error", "debug"} {
		console.Set(name, log)
	}
	vm.Set("console", console)
}

// format shows a value as console.log would: objects as JSON.
func format(v goja.Value) string {
	if obj, ok := v.(*goja.Object); ok && obj.ClassName() != "Function" && obj.ClassName() != "Error" {
		if data, err := obj.MarshalJSON(); err == nil {
			return string(data)
		}
	}
	return v.String()
}
