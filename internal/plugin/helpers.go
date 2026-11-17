package plugin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The helpers below back the host functions the embedded runtimes offer scripts
// (Lua and JavaScript have no files or network of their own in a sandbox, and
// these behave the same in both).

// ReadFile reads a file; a relative path is relative to the plugin's directory.
func (p *Plugin) ReadFile(name string) (string, error) {
	data, err := os.ReadFile(p.resolve(name))
	return string(data), err
}

// WriteFile writes a file, making its directory; relative as in ReadFile.
func (p *Plugin) WriteFile(name, content string) error {
	path := p.resolve(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (p *Plugin) resolve(name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(p.Dir, name)
}

// HTTP sends one request described by a script and describes the response.
//
// The request: method (GET), url, headers (an object of name to value, or to a
// list of values), body (text), timeout (seconds, 30; 0 for none but the
// action's own), redirects (true: follow them). Any status is an answer; only a
// failure to get one (DNS, refused, timed out) is an error.
//
// The response: status (200), status_text ("200 OK"), proto, url (after
// redirects), headers (name to value, repeated ones joined by ", "), body
// (text, at most 32 MB), size (bytes of body), truncated, elapsed_ms.
func HTTP(ctx context.Context, request map[string]any) (map[string]any, error) {
	method := strings.ToUpper(strings.TrimSpace(str(request["method"])))
	if method == "" {
		method = http.MethodGet
	}
	url := strings.TrimSpace(str(request["url"]))
	if url == "" {
		return nil, fmt.Errorf("http: url is empty")
	}
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	timeout := 30 * time.Second
	if t, ok := number(request["timeout"]); ok {
		timeout = time.Duration(t * float64(time.Second))
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var body io.Reader
	if b := str(request["body"]); b != "" {
		body = strings.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if headers, ok := request["headers"].(map[string]any); ok {
		for name, v := range headers {
			switch v := v.(type) {
			case []any:
				for _, e := range v {
					req.Header.Add(name, str(e))
				}
			default:
				req.Header.Set(name, str(v))
			}
		}
	}
	if host := req.Header.Get("Host"); host != "" {
		req.Host = host
	}
	client := &http.Client{}
	if follow, ok := request["redirects"].(bool); ok && !follow {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	began := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const limit = 32 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(began)
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
	}
	headers := make(map[string]any, len(resp.Header))
	for name, values := range resp.Header {
		headers[name] = strings.Join(values, ", ")
	}
	return map[string]any{
		"status": float64(resp.StatusCode), "status_text": resp.Status, "proto": resp.Proto,
		"url": resp.Request.URL.String(), "headers": headers, "body": string(data),
		"size": float64(len(data)), "truncated": truncated,
		"elapsed_ms": float64(elapsed.Microseconds()) / 1000,
	}, nil
}

// number reads a number a script passed: JavaScript gives integers as int64.
func number(v any) (float64, bool) {
	switch v := v.(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	}
	return 0, false
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return Stringify(v)
}

// HTTPGet fetches url and returns its body; a status other than 2xx is an error.
func HTTPGet(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return string(body), fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return string(body), nil
}
