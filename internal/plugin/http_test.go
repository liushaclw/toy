package plugin_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"toy/internal/plugin"
)

// echo answers with what it was sent: the method, a header and the body.
func echo(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/moved":
			http.Redirect(w, r, "/echo", http.StatusFound)
			return
		case "/missing":
			http.Error(w, "nothing here", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Seen-Type", r.Header.Get("Content-Type"))
		io.WriteString(w, `{"method":"`+r.Method+`","token":"`+r.Header.Get("X-Token")+`","body":`+jsonString(string(body))+`}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func TestHostHTTPInLuaAndJS(t *testing.T) {
	server := echo(t)
	for _, lang := range []struct{ runtime, entry, src string }{
		{"lua", "main.lua", `
function run(action, inputs)
  local r, err = host.http({method = inputs.method, url = inputs.url, headers = {["X-Token"] = "t1"}, body = inputs.body, redirects = inputs.follow})
  if not r then error(err) end
  return r.status .. " " .. r.body
end`},
		{"js", "main.js", `
function run(action, inputs) {
  const r = host.http({method: inputs.method, url: inputs.url, headers: {"X-Token": "t1"}, body: inputs.body, redirects: inputs.follow, timeout: 5})
  return r.status + " " + r.body
}`},
	} {
		t.Run(lang.runtime, func(t *testing.T) {
			h := newHarness(t)
			p := writePlugin(t, `
id = "h"
runtime = "`+lang.runtime+`"
entry = "`+lang.entry+`"
[[tools]]
id = "h"
`, map[string]string{lang.entry: lang.src})
			call := func(method, path, body string, follow bool) string {
				t.Helper()
				e := h.run(p, "run", map[string]any{"method": method, "url": server.URL + path, "body": body, "follow": follow})
				if e.Err != nil {
					t.Fatal(e.Err)
				}
				return e.Result.Texts["result"]
			}
			if got, want := call("POST", "/echo", "hi", true), `200 {"method":"POST","token":"t1","body":"hi"}`; got != want {
				t.Fatalf("POST: %s, want %s", got, want)
			}
			if got := call("GET", "/missing", "", true); !strings.HasPrefix(got, "404 nothing here") {
				t.Fatalf("a 404 is an answer, not an error: %q", got)
			}
			if got := call("GET", "/moved", "", true); !strings.HasPrefix(got, `200 {"method":"GET"`) {
				t.Fatalf("followed: %q", got)
			}
			if got := call("GET", "/moved", "", false); !strings.HasPrefix(got, "302") {
				t.Fatalf("not followed: %q", got)
			}
		})
	}
}

func TestHTTPExample(t *testing.T) {
	server := echo(t)
	h := newHarness(t)
	p := find(t, plugin.Discover("../../plugins"), "http")
	form := map[string]any{
		"saved": "", "method": "POST", "url": server.URL + "/echo", "headers": "X-Token: abc\n# a comment\n",
		"body": `{"a":1}`, "timeout": 10.0, "follow": true, "name": "",
	}

	e := h.run(p, "send", form)
	if e.Err != nil {
		t.Fatal(e.Err)
	}
	if !strings.HasPrefix(e.Result.Texts["status"], "200 OK · ") {
		t.Fatalf("status %q", e.Result.Texts["status"])
	}
	if want := "{\n  \"method\": \"POST\",\n  \"token\": \"abc\",\n  \"body\": \"{\\\"a\\\":1}\"\n}"; e.Result.Texts["body"] != want {
		t.Fatalf("body %q", e.Result.Texts["body"])
	}
	headers := map[string]string{}
	for _, row := range e.Result.Tables["headers"].Rows {
		headers[row[0]] = row[1]
	}
	if headers["X-Seen-Type"] != "application/json" {
		t.Fatalf("a JSON body is not sent as JSON: %v", headers)
	}

	if e := h.run(p, "list", form); len(e.Result.Options["saved"]) != 0 || !e.Result.KeepOutputs {
		t.Fatalf("list before saving: %+v", e.Result)
	}
	form["name"] = "echo"
	e = h.run(p, "save", form)
	if e.Err != nil || strings.Join(e.Result.Options["saved"], ",") != "echo" || e.Result.Inputs["saved"] != "echo" || e.Result.Status != "已保存「echo」" {
		t.Fatalf("save: %v %+v", e.Err, e.Result)
	}

	blank := map[string]any{"saved": "echo", "method": "GET", "url": "", "headers": "", "body": "", "timeout": 0.0, "follow": true, "name": ""}
	e = h.run(p, "load", blank)
	if e.Err != nil || e.Result.Inputs["url"] != server.URL+"/echo" || e.Result.Inputs["method"] != "POST" || e.Result.Inputs["body"] != `{"a":1}` {
		t.Fatalf("load: %v %+v", e.Err, e.Result)
	}

	e = h.run(p, "delete", blank)
	if e.Err != nil || len(e.Result.Options["saved"]) != 0 {
		t.Fatalf("delete: %v %+v", e.Err, e.Result)
	}
	if e := h.run(p, "delete", blank); e.Err == nil {
		t.Fatal("deleting what is not saved should say so")
	}
	if e := h.run(p, "send", map[string]any{"url": "", "headers": "no colon"}); e.Err == nil {
		t.Fatal("sending without a URL should fail")
	}
}

func TestReservedKeysChangeTheForm(t *testing.T) {
	tool := &plugin.Tool{Outputs: []plugin.Output{{ID: "result", Type: plugin.OutputText}}}
	r, err := plugin.Normalize(map[string]any{
		"$inputs": map[string]any{"url": "x"}, "$options": map[string]any{"saved": []any{"a", "b"}}, "$status": "ok",
	}, tool)
	if err != nil || r.Inputs["url"] != "x" || len(r.Options["saved"]) != 2 || r.Status != "ok" || !r.KeepOutputs {
		t.Fatalf("%v %+v", err, r)
	}
	r, _ = plugin.Normalize(map[string]any{"$status": "ok", "result": "y"}, tool)
	if r.KeepOutputs || r.Texts["result"] != "y" {
		t.Fatalf("a result with outputs replaces them: %+v", r)
	}
	if _, err := plugin.Normalize(map[string]any{"$options": map[string]any{"saved": "a"}}, tool); err == nil {
		t.Fatal("$options of a string should be an error")
	}
}
