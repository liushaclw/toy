package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vivaui/viva/components"
	"github.com/vivaui/viva/text"
	"github.com/vivaui/viva/ui"

	"toy/internal/plugin"
)

// toolView is one tool of the sidebar and its form: the values typed in, the
// job that runs, and what the last run produced.
type toolView struct {
	key    string // plugin id / tool id
	plugin *plugin.Plugin
	tool   *plugin.Tool // nil for a plugin whose manifest failed
	title  string
	detail string // plugin name, version and runtime, kept to avoid formatting per paint
	broken string // why the plugin cannot be used

	texts   []string   // per input: text, password, textarea, number, file
	bools   []bool     // per input: switch
	choices []string   // per input: select
	options [][]string // per input: a select's options, which a result's $options replaces

	running  bool
	job      int
	quiet    bool    // the job is the tool's init: it reports no status of its own
	initDone bool    // the init action has been started
	progress float64 // 0..1, or negative while unknown

	err     string
	status  string
	outText []string       // per output (text): what the plugin returned
	outBox  []ui.Field     // per output (text): the box showing it, which the user may edit
	outEdit []*text.Editor // the editors of outBox, to put the caret back at the start
	tables  []*tableView   // per output (table)
}

type tableView struct {
	headers []string
	columns []components.TableColumn // ui.Table paints nothing without them
	rows    ui.TableRows
	state   ui.TableState
}

// columnWidths sizes every column but the last to its widest cell (estimated:
// a wide character counts twice), within bounds; the last takes the rest.
func columnWidths(headers []string, rows [][]string) []components.TableColumn {
	columns := make([]components.TableColumn, len(headers))
	for c := range columns[:max(len(columns)-1, 0)] {
		widest := textWidth(headers[c])
		for _, row := range rows {
			if c < len(row) {
				widest = max(widest, textWidth(row[c]))
			}
		}
		columns[c].Width = min(max(float32(widest)*7.5+24, 80), 320)
	}
	return columns
}

// textWidth counts a string in narrow-character widths.
func textWidth(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115F || (r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE30 && r <= 0xFE4F) || (r >= 0xFF00 && r <= 0xFF60) || (r >= 0xFFE0 && r <= 0xFFE6)) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func newToolView(p *plugin.Plugin, t *plugin.Tool) *toolView {
	man := p.Manifest
	tv := &toolView{
		key: man.ID + "/" + t.ID, plugin: p, tool: t, title: t.Name,
		detail: fmt.Sprintf("%s %s · %s", man.Name, man.Version, man.Runtime),
		texts:  make([]string, len(t.Inputs)), bools: make([]bool, len(t.Inputs)),
		choices: make([]string, len(t.Inputs)), options: make([][]string, len(t.Inputs)),
		outText: make([]string, len(t.Outputs)), tables: make([]*tableView, len(t.Outputs)),
		outBox: make([]ui.Field, len(t.Outputs)), outEdit: make([]*text.Editor, len(t.Outputs)),
	}
	for i := range t.Outputs {
		tv.outEdit[i] = text.NewEditor("")
		tv.outBox[i].Bind(tv.outEdit[i])
	}
	for i, in := range t.Inputs {
		switch in.Type {
		case plugin.InputSwitch:
			tv.bools[i], _ = in.Default.(bool)
		case plugin.InputSelect:
			tv.options[i] = in.Options
			if len(in.Options) > 0 {
				tv.choices[i] = in.Options[0]
			}
			if d, ok := in.Default.(string); ok && contains(in.Options, d) {
				tv.choices[i] = d
			}
		default:
			if in.Default != nil {
				tv.texts[i] = plugin.Stringify(in.Default)
			}
		}
	}
	for i, out := range t.Outputs {
		if out.Type == plugin.OutputTable {
			tv.tables[i] = &tableView{}
		}
	}
	return tv
}

func newBrokenView(p *plugin.Plugin) *toolView {
	return &toolView{
		key: p.Manifest.ID + "/", plugin: p, title: p.Manifest.Name,
		detail: p.Dir, broken: p.Err.Error(),
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// keepValues takes what was typed into the same tool before a reload.
func (tv *toolView) keepValues(old *toolView) {
	if old == nil || old.tool == nil || tv.tool == nil {
		return
	}
	for i, in := range tv.tool.Inputs {
		for j, was := range old.tool.Inputs {
			if was.ID != in.ID || was.Type != in.Type {
				continue
			}
			tv.texts[i], tv.bools[i] = old.texts[j], old.bools[j]
			if in.Type == plugin.InputSelect && len(in.Options) == 0 {
				tv.options[i] = old.options[j] // filled by results: keep them until init runs again
			}
			if in.Type != plugin.InputSelect || contains(tv.options[i], old.choices[j]) {
				tv.choices[i] = old.choices[j]
			}
		}
	}
}

// inputs are the form's values as the plugin receives them. check enforces
// required fields and numbers, for a button; an init or a change sends the form
// as it is. need, when not nil, are the inputs required in place of those marked so.
func (tv *toolView) inputs(check bool, need []string) (map[string]any, error) {
	values := make(map[string]any, len(tv.tool.Inputs))
	for i, in := range tv.tool.Inputs {
		if need != nil {
			in.Required = contains(need, in.ID) // in is a copy
		}
		switch in.Type {
		case plugin.InputSwitch:
			values[in.ID] = tv.bools[i]
		case plugin.InputSelect:
			if check && in.Required && tv.choices[i] == "" {
				return nil, fmt.Errorf("请选择「%s」", in.Label)
			}
			values[in.ID] = tv.choices[i]
		case plugin.InputNumber:
			if check && in.Required && strings.TrimSpace(tv.texts[i]) == "" {
				return nil, fmt.Errorf("请填写「%s」", in.Label)
			}
			n, err := plugin.ParseNumber(tv.texts[i])
			if err != nil && check {
				return nil, fmt.Errorf("「%s」不是数字", in.Label)
			}
			values[in.ID] = n
		default:
			if check && in.Required && strings.TrimSpace(tv.texts[i]) == "" {
				return nil, fmt.Errorf("请填写「%s」", in.Label)
			}
			values[in.ID] = tv.texts[i]
		}
	}
	return values, nil
}

// fill replaces each {id} in s by the value of that input, as the form shows it.
func (tv *toolView) fill(s string) string {
	if !strings.Contains(s, "{") {
		return s
	}
	pairs := make([]string, 0, 2*len(tv.tool.Inputs))
	for i, in := range tv.tool.Inputs {
		v := tv.texts[i]
		switch in.Type {
		case plugin.InputSwitch:
			v = strconv.FormatBool(tv.bools[i])
		case plugin.InputSelect:
			v = tv.choices[i]
		}
		pairs = append(pairs, "{"+in.ID+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// namesEmpty says s names, as {id}, an input with no value.
func (tv *toolView) namesEmpty(s string) bool {
	for i, in := range tv.tool.Inputs {
		if in.Type == plugin.InputSwitch || !strings.Contains(s, "{"+in.ID+"}") {
			continue
		}
		v := tv.texts[i]
		if in.Type == plugin.InputSelect {
			v = tv.choices[i]
		}
		if strings.TrimSpace(v) == "" {
			return true
		}
	}
	return false
}

// finish shows what a job ended with.
func (tv *toolView) finish(e plugin.Event) {
	tv.running = false
	elapsed := e.Elapsed.Round(time.Millisecond)
	if e.Err != nil {
		tv.err, tv.status = e.Err.Error(), "失败，用时 "+elapsed.String()
		return
	}
	tv.err, tv.status = "", "完成，用时 "+elapsed.String()
	if tv.quiet {
		tv.status = ""
	}
	if e.Result.Status != "" {
		tv.status = e.Result.Status
	}
	tv.applyOptions(e.Result.Options)
	tv.applyInputs(e.Result.Inputs)
	if e.Result.KeepOutputs {
		return
	}
	for i, out := range tv.tool.Outputs {
		if out.Type == plugin.OutputTable {
			t := e.Result.Tables[out.ID]
			tv.tables[i].headers = t.Columns
			tv.tables[i].columns = columnWidths(t.Columns, t.Rows)
			tv.tables[i].rows = ui.TableRows(t.Rows)
			tv.tables[i].state = ui.TableState{}
			continue
		}
		tv.setOutput(i, e.Result.Texts[out.ID])
	}
}

// setOutput shows text in output i from its first line: a new result is read
// from the top, where setting a box's text would leave it scrolled to the end.
func (tv *toolView) setOutput(i int, s string) {
	tv.outText[i] = s
	tv.outBox[i].SetText(s)
	tv.outEdit[i].MoveTo(0, false)
}

// applyOptions replaces the options of selects; a choice no longer offered is
// cleared.
func (tv *toolView) applyOptions(options map[string][]string) {
	for i, in := range tv.tool.Inputs {
		list, ok := options[in.ID]
		if !ok || in.Type != plugin.InputSelect {
			continue
		}
		tv.options[i] = list
		if !contains(list, tv.choices[i]) {
			tv.choices[i] = ""
		}
	}
}

// applyInputs fills the form with values a plugin returned.
func (tv *toolView) applyInputs(values map[string]any) {
	for i, in := range tv.tool.Inputs {
		v, ok := values[in.ID]
		if !ok {
			continue
		}
		switch in.Type {
		case plugin.InputSwitch:
			b, _ := v.(bool)
			tv.bools[i] = b
		case plugin.InputSelect:
			if s := plugin.Stringify(v); s == "" || contains(tv.options[i], s) {
				tv.choices[i] = s
			}
		default:
			tv.texts[i] = plugin.Stringify(v)
		}
	}
}

// hasOutput says whether output i has something to show.
func (tv *toolView) hasOutput(i int) bool {
	if t := tv.tables[i]; t != nil {
		return len(t.rows) > 0
	}
	return tv.outText[i] != ""
}

// main is the output that takes the column: the first long one with something
// to show (a status line above a response body leaves it to the body), else the
// first with something to show, or -1.
func (tv *toolView) main() int {
	first := -1
	for i := range tv.tool.Outputs {
		if !tv.hasOutput(i) {
			continue
		}
		if !tv.short(i) {
			return i
		}
		if first < 0 {
			first = i
		}
	}
	return first
}

// What ui.Table and ui.TextArea paint: the heights of a table's header and
// rows, and the least height of a text area.
const (
	tableHeadHeight = 40
	tableRowHeight  = 36
	areaMinHeight   = 64
)

// besideMain is the room the outputs other than main take below or above it in
// the output column, so the main one leaves it when the window has it: a table
// counts up to tableRowsBeside rows, and one longer than that is a scroll away.
func (tv *toolView) besideMain(main int) float32 {
	const tableRowsBeside = 8
	var room float32
	for i := range tv.tool.Outputs {
		if i == main || !tv.hasOutput(i) {
			continue
		}
		room += outputHead + outputGap
		switch {
		case tv.tables[i] != nil:
			room += tableHeadHeight + float32(min(len(tv.tables[i].rows), tableRowsBeside))*tableRowHeight + 2
		case tv.compact(i):
			room += max(areaMinHeight, rowsHeight(lineCount(tv.outText[i])))
		default:
			room += sideHeight
		}
	}
	return room
}

// compact says output i is a side note (a count, a status) short enough to show
// at its own height: not the main output, a few lines, none long enough to wrap.
func (tv *toolView) compact(i int) bool {
	return i != tv.main() && tv.short(i)
}

// short says output i is text of a few lines, none long enough to wrap.
func (tv *toolView) short(i int) bool {
	if tv.tables[i] != nil {
		return false
	}
	return shortText(tv.outText[i], 3, 60)
}

// shortText says s has at most lines lines of at most width narrow characters.
// It stops at the first that is not, so a long response costs nothing per paint.
func shortText(s string, lines, width int) bool {
	if len(s) > lines*(width*4+1) { // more bytes than such text can have
		return false
	}
	for n := 1; ; n++ {
		if n > lines {
			return false
		}
		line, rest, more := strings.Cut(s, "\n")
		if textWidth(line) > width {
			return false
		}
		if !more {
			return true
		}
		s = rest
	}
}

func (tv *toolView) matches(query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(strings.TrimSpace(query))
	return strings.Contains(strings.ToLower(tv.title), q) || strings.Contains(strings.ToLower(tv.detail), q)
}

// rowsHeight is the height of a text box showing n lines.
func rowsHeight(n int) float32 { return float32(n)*20 + 16 }

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

func itoa(n int) string { return strconv.Itoa(n) }
