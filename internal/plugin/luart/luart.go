// Package luart runs Lua 5.1 plugins in the process with gopher-lua.
//
// The entry script defines a global function run(action, inputs, tool) and
// returns nothing; run returns a string or a table keyed by output id. The
// global host table offers log, progress, clipboard, get, set, read_file,
// write_file, http and http_get; print goes to the log. A plugin's own modules load
// with require from its directory.
package luart

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	lua "github.com/yuin/gopher-lua"

	"toy/internal/plugin"
)

// Runtime is the "lua" runtime.
type Runtime struct{}

func (Runtime) Start(p *plugin.Plugin) (plugin.Instance, error) {
	L := lua.NewState()
	in := &instance{L: L, plugin: p, host: plugin.NoHost, ctx: context.Background()}
	in.install()
	pkg := L.GetGlobal("package").(*lua.LTable)
	dir := filepath.ToSlash(p.Dir)
	pkg.RawSetString("path", lua.LString(dir+"/?.lua;"+dir+"/?/init.lua;"+lua.LVAsString(pkg.RawGetString("path"))))
	if err := L.DoFile(p.Path(p.Manifest.Entry)); err != nil {
		L.Close()
		return nil, err
	}
	if _, ok := L.GetGlobal("run").(*lua.LFunction); !ok {
		L.Close()
		return nil, fmt.Errorf("%s defines no function run(action, inputs, tool)", p.Manifest.Entry)
	}
	return in, nil
}

type instance struct {
	L      *lua.LState
	plugin *plugin.Plugin
	ctx    context.Context
	host   plugin.Host
}

func (in *instance) Run(ctx context.Context, call plugin.Call) (any, error) {
	in.ctx, in.host = ctx, call.Host
	L := in.L
	L.SetContext(ctx)
	defer L.RemoveContext()
	err := L.CallByParam(lua.P{Fn: L.GetGlobal("run"), NRet: 1, Protect: true},
		lua.LString(call.Action), ToLua(L, call.Inputs), lua.LString(call.Tool))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	ret := L.Get(-1)
	L.Pop(1)
	return FromLua(ret), nil
}

func (in *instance) Close() error {
	in.L.Close()
	return nil
}

func (in *instance) install() {
	L := in.L
	host := L.NewTable()
	fns := map[string]lua.LGFunction{
		"log": func(L *lua.LState) int {
			in.host.Log(joinArgs(L))
			return 0
		},
		"progress": func(L *lua.LState) int {
			in.host.Progress(float64(L.CheckNumber(1)))
			return 0
		},
		"clipboard": func(L *lua.LState) int {
			in.host.Clipboard(L.CheckString(1))
			return 0
		},
		"get": func(L *lua.LState) int {
			v, err := in.host.Get(L.CheckString(1))
			if err != nil {
				L.RaiseError("%v", err)
			}
			L.Push(ToLua(L, v))
			return 1
		},
		"set": func(L *lua.LState) int {
			if err := in.host.Set(L.CheckString(1), FromLua(L.Get(2))); err != nil {
				L.RaiseError("%v", err)
			}
			return 0
		},
		"read_file": func(L *lua.LState) int {
			s, err := in.plugin.ReadFile(L.CheckString(1))
			return pushResult(L, s, err)
		},
		"write_file": func(L *lua.LState) int {
			err := in.plugin.WriteFile(L.CheckString(1), L.CheckString(2))
			return pushResult(L, "", err)
		},
		"http": func(L *lua.LState) int {
			request, _ := FromLua(L.CheckTable(1)).(map[string]any)
			resp, err := plugin.HTTP(in.ctx, request)
			if err != nil {
				L.Push(lua.LNil)
				L.Push(lua.LString(err.Error()))
				return 2
			}
			L.Push(ToLua(L, resp))
			return 1
		},
		"http_get": func(L *lua.LState) int {
			s, err := plugin.HTTPGet(in.ctx, L.CheckString(1))
			return pushResult(L, s, err)
		},
	}
	for name, fn := range fns {
		host.RawSetString(name, L.NewFunction(fn))
	}
	L.SetGlobal("host", host)
	L.SetGlobal("print", L.NewFunction(func(L *lua.LState) int {
		if in.host != nil {
			in.host.Log(joinArgs(L))
		}
		return 0
	}))
}

// pushResult returns value, nil or nil, message: Lua's convention for failures.
func pushResult(L *lua.LState, value string, err error) int {
	if err != nil {
		L.Push(lua.LNil)
		L.Push(lua.LString(err.Error()))
		return 2
	}
	if value == "" {
		L.Push(lua.LTrue)
	} else {
		L.Push(lua.LString(value))
	}
	return 1
}

func joinArgs(L *lua.LState) string {
	parts := make([]string, L.GetTop())
	for i := range parts {
		parts[i] = L.ToStringMeta(L.Get(i + 1)).String()
	}
	return strings.Join(parts, "\t")
}

// ToLua converts a Go value from JSON-like data to Lua.
func ToLua(L *lua.LState, v any) lua.LValue {
	switch v := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(v)
	case bool:
		return lua.LBool(v)
	case float64:
		return lua.LNumber(v)
	case int:
		return lua.LNumber(v)
	case int64:
		return lua.LNumber(v)
	case []any:
		t := L.NewTable()
		for _, e := range v {
			t.Append(ToLua(L, e))
		}
		return t
	case map[string]any:
		t := L.NewTable()
		for k, e := range v {
			t.RawSetString(k, ToLua(L, e))
		}
		return t
	}
	return lua.LString(fmt.Sprint(v))
}

// FromLua converts a Lua value to JSON-like Go data: a table with only the keys
// 1..n is a list, any other table a map with string keys.
func FromLua(v lua.LValue) any {
	switch v := v.(type) {
	case lua.LString:
		return string(v)
	case lua.LNumber:
		return float64(v)
	case lua.LBool:
		return bool(v)
	case *lua.LTable:
		n := v.Len()
		count := 0
		v.ForEach(func(lua.LValue, lua.LValue) { count++ })
		if n > 0 && count == n {
			list := make([]any, n)
			for i := range list {
				list[i] = FromLua(v.RawGetInt(i + 1))
			}
			return list
		}
		m := map[string]any{}
		v.ForEach(func(k, e lua.LValue) { m[k.String()] = FromLua(e) })
		return m
	}
	return nil
}
