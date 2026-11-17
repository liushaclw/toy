package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Runtime runs plugins of one language.
type Runtime interface {
	// Start prepares a plugin: it loads the script or starts the process. It is
	// called on the plugin's worker goroutine, the first time an action runs.
	Start(p *Plugin) (Instance, error)
}

// Instance is a started plugin. Its methods are called from one goroutine at a
// time (the plugin's worker), so it needs no lock of its own.
type Instance interface {
	// Run performs action of tool with the form's values and returns what the
	// plugin answered: a string, or a map from output id to value. It returns
	// when ctx is done at the latest, with ctx's error.
	Run(ctx context.Context, call Call) (any, error)
	Close() error
}

// Call is one press of an action button.
type Call struct {
	Tool   string
	Action string
	Inputs map[string]any
	Host   Host
}

// Host is what a running action may ask of the application.
type Host interface {
	Log(text string)
	// Progress reports how far the action is, 0..1; a negative value means unknown.
	Progress(fraction float64)
	// Clipboard puts text on the clipboard (on the window's thread, after the event).
	Clipboard(text string)
	// Get and Set keep small values per plugin, across runs of the application.
	Get(key string) (any, error)
	Set(key string, value any) error
}

// Table is a table output.
type Table struct {
	Columns []string
	Rows    [][]string
}

// Result is what an action produced, sorted into the tool's outputs.
type Result struct {
	Texts  map[string]string
	Tables map[string]Table

	// What the reserved keys of a returned map ask of the form.
	Inputs  map[string]any      // $inputs: new values of inputs, by id
	Options map[string][]string // $options: new options of selects, by id
	Status  string              // $status: the line under the buttons
	// KeepOutputs is true when the map had reserved keys and no output: an
	// action that only fills the form leaves the last results shown.
	KeepOutputs bool
}

// The keys of a returned map that speak to the form rather than an output.
const (
	KeyInputs  = "$inputs"
	KeyOptions = "$options"
	KeyStatus  = "$status"
)

// Normalize turns what a plugin answered into a Result for tool. A plain value
// goes to the first output; a map is keyed by output id. A table is a map with
// "columns" and "rows", a list of lists, or a list of objects (their keys become
// the columns). The keys $inputs, $options and $status change the form.
func Normalize(raw any, tool *Tool) (Result, error) {
	r := Result{Texts: map[string]string{}, Tables: map[string]Table{}}
	if raw == nil {
		return r, nil
	}
	values, ok := raw.(map[string]any)
	if !ok || isTable(values) {
		values = map[string]any{tool.Outputs[0].ID: raw}
	}
	reserved := false
	if v, ok := values[KeyInputs]; ok {
		reserved = true
		m, ok := v.(map[string]any)
		if !ok && v != nil {
			return r, fmt.Errorf("%s must be an object of input values, not %T", KeyInputs, v)
		}
		r.Inputs = m
	}
	if v, ok := values[KeyOptions]; ok {
		reserved = true
		m, ok := v.(map[string]any)
		if !ok && v != nil {
			return r, fmt.Errorf("%s must be an object of option lists, not %T", KeyOptions, v)
		}
		r.Options = map[string][]string{}
		for id, list := range m {
			items, ok := list.([]any)
			if !ok && list != nil {
				return r, fmt.Errorf("%s.%s must be a list, not %T", KeyOptions, id, list)
			}
			options := make([]string, 0, len(items))
			for _, item := range items {
				options = append(options, Stringify(item))
			}
			r.Options[id] = options
		}
	}
	if v, ok := values[KeyStatus]; ok {
		reserved = true
		r.Status = Stringify(v)
	}
	outputs := false
	for _, out := range tool.Outputs {
		if _, ok := values[out.ID]; ok {
			outputs = true
		}
	}
	r.KeepOutputs = reserved && !outputs
	for _, out := range tool.Outputs {
		v, ok := values[out.ID]
		if !ok || v == nil {
			continue
		}
		if out.Type == OutputTable {
			t, err := toTable(v)
			if err != nil {
				return r, fmt.Errorf("output %q: %w", out.ID, err)
			}
			r.Tables[out.ID] = t
			continue
		}
		r.Texts[out.ID] = Stringify(v)
	}
	return r, nil
}

func isTable(m map[string]any) bool {
	_, rows := m["rows"]
	return rows && len(m) <= 2
}

func toTable(v any) (Table, error) {
	var t Table
	switch v := v.(type) {
	case map[string]any:
		if cols, ok := v["columns"].([]any); ok {
			for _, c := range cols {
				t.Columns = append(t.Columns, Stringify(c))
			}
		}
		rows, _ := v["rows"].([]any)
		return rowsInto(t, rows)
	case []any:
		return rowsInto(t, v)
	}
	return t, fmt.Errorf("a table is {columns, rows} or a list, not %T", v)
}

func rowsInto(t Table, rows []any) (Table, error) {
	for _, row := range rows {
		switch row := row.(type) {
		case []any:
			cells := make([]string, len(row))
			for i, c := range row {
				cells[i] = Stringify(c)
			}
			t.Rows = append(t.Rows, cells)
		case map[string]any:
			if t.Columns == nil {
				for k := range row {
					t.Columns = append(t.Columns, k)
				}
				slices.Sort(t.Columns)
			}
			cells := make([]string, len(t.Columns))
			for i, k := range t.Columns {
				cells[i] = Stringify(row[k])
			}
			t.Rows = append(t.Rows, cells)
		default:
			t.Rows = append(t.Rows, []string{Stringify(row)})
		}
	}
	width := len(t.Columns)
	for _, r := range t.Rows {
		width = max(width, len(r))
	}
	for len(t.Columns) < width {
		t.Columns = append(t.Columns, strconv.Itoa(len(t.Columns)+1))
	}
	for i, r := range t.Rows {
		for len(r) < width {
			r = append(r, "")
		}
		t.Rows[i] = r
	}
	return t, nil
}

// Stringify writes a value a plugin returned as the text a field shows.
func Stringify(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case bool:
		return strconv.FormatBool(v)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// ParseNumber reads a number field; an empty field is 0.
func ParseNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}

// NoHost is the Host of code that runs outside an action, such as a script's
// top level while it loads: logs and progress go nowhere, storage works.
var NoHost Host = noHost{}

type noHost struct{}

func (noHost) Log(string)              {}
func (noHost) Progress(float64)        {}
func (noHost) Clipboard(string)        {}
func (noHost) Get(string) (any, error) { return nil, nil }
func (noHost) Set(string, any) error   { return nil }
