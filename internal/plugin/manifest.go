// Package plugin finds plugins on disk, describes them (manifest) and runs their
// actions on worker goroutines, one per plugin, through a Runtime of their
// language. It knows nothing about the window: the UI reads Events from a
// Manager and renders tools from their Manifest.
package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ManifestFile is the name of the file that makes a directory a plugin.
const ManifestFile = "plugin.toml"

// Input types a tool form can declare.
const (
	InputText     = "text"
	InputPassword = "password"
	InputTextarea = "textarea"
	InputNumber   = "number"
	InputSwitch   = "switch"
	InputSelect   = "select"
	InputFile     = "file"
)

// Output types a tool can show.
const (
	OutputText  = "text"
	OutputTable = "table"
)

// Manifest is plugin.toml.
type Manifest struct {
	ID          string `toml:"id"`
	Name        string `toml:"name"`
	Version     string `toml:"version"`
	Description string `toml:"description"`
	// Runtime is the name of the runtime that runs the plugin: lua, js or exec.
	Runtime string `toml:"runtime"`
	// Entry is the script a lua or js plugin starts from, relative to its directory.
	Entry string `toml:"entry"`
	// Command starts an exec plugin; a first element that names a file in the
	// plugin's directory is run from there.
	Command []string `toml:"command"`
	Tools   []Tool   `toml:"tools"`
}

// Tool is one form in the window. A plugin may offer several.
type Tool struct {
	ID          string `toml:"id"`
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Timeout     int    `toml:"timeout"` // seconds; 0 means DefaultTimeout, negative means none
	// Init is an action run when the tool is first shown, to fill the form (the
	// options of a select from stored values, say). It need not be a button.
	Init    string   `toml:"init"`
	Inputs  []Input  `toml:"inputs"`
	Actions []Action `toml:"actions"`
	Outputs []Output `toml:"outputs"`
}

// DefaultTimeout bounds an action whose tool sets no timeout.
const DefaultTimeout = 2 * time.Minute

// Deadline is how long one action of the tool may run, or 0 for no limit.
func (t *Tool) Deadline() time.Duration {
	switch {
	case t.Timeout < 0:
		return 0
	case t.Timeout == 0:
		return DefaultTimeout
	}
	return time.Duration(t.Timeout) * time.Second
}

// Input is one field of a tool's form.
type Input struct {
	ID          string   `toml:"id"`
	Type        string   `toml:"type"`
	Label       string   `toml:"label"`
	Placeholder string   `toml:"placeholder"`
	Default     any      `toml:"default"`
	Options     []string `toml:"options"` // select
	Rows        int      `toml:"rows"`    // textarea
	Required    bool     `toml:"required"`
	// OnChange is an action run when the user changes a select or a switch.
	OnChange string `toml:"on_change"`
}

// Action is a button; its ID is what the plugin receives.
type Action struct {
	ID    string `toml:"id"`
	Label string `toml:"label"`
	// Style is the button's look, one of the Style constants; empty is primary
	// for the first action and outline for the others.
	Style string `toml:"style"`
	// Confirm, when set, is asked in a dialog before the action runs; {id}
	// in it is replaced by the value of that input.
	Confirm string `toml:"confirm"`
	// Requires, when set, are the inputs the action needs, in place of the
	// inputs marked required: deleting a saved request needs only the choice.
	Requires []string `toml:"requires"`
}

// The looks of an action's button.
const (
	StylePrimary       = "primary"        // filled with the theme's primary color
	StyleSecondary     = "secondary"      // filled, muted
	StyleOutline       = "outline"        // a border, no fill
	StyleGhost         = "ghost"          // text only
	StyleDanger        = "danger"         // filled red: an action that destroys
	StyleDangerOutline = "danger-outline" // a red border
)

// Output is a place the tool's results are shown, named by the key the plugin
// returns them under.
type Output struct {
	ID       string `toml:"id"`
	Type     string `toml:"type"`
	Label    string `toml:"label"`
	Copyable bool   `toml:"copyable"`
}

// LoadManifest reads and checks dir/plugin.toml.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if err := m.check(); err != nil {
		return &m, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	return &m, nil
}

func (m *Manifest) check() error {
	if m.ID == "" {
		return fmt.Errorf("id is empty")
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	switch m.Runtime {
	case "lua", "js":
		if m.Entry == "" {
			return fmt.Errorf("runtime %q needs entry", m.Runtime)
		}
	case "exec":
		if len(m.Command) == 0 {
			return fmt.Errorf("runtime exec needs command")
		}
	case "":
		return fmt.Errorf("runtime is empty")
	}
	if len(m.Tools) == 0 {
		return fmt.Errorf("no [[tools]]")
	}
	tools := map[string]bool{}
	for i := range m.Tools {
		t := &m.Tools[i]
		if t.ID == "" {
			return fmt.Errorf("tool %d has no id", i+1)
		}
		if tools[t.ID] {
			return fmt.Errorf("tool %q declared twice", t.ID)
		}
		tools[t.ID] = true
		if t.Name == "" {
			t.Name = t.ID
		}
		if len(t.Actions) == 0 {
			t.Actions = []Action{{ID: "run", Label: "运行"}}
		}
		if len(t.Outputs) == 0 {
			t.Outputs = []Output{{ID: "result", Type: OutputText, Label: "结果", Copyable: true}}
		}
		seen := map[string]bool{}
		for j := range t.Inputs {
			in := &t.Inputs[j]
			if in.ID == "" || seen[in.ID] {
				return fmt.Errorf("tool %q: input %d has an empty or repeated id", t.ID, j+1)
			}
			seen[in.ID] = true
			if in.Type == "" {
				in.Type = InputText
			}
			if in.Label == "" {
				in.Label = in.ID
			}
			switch in.Type {
			// A select may start with no options: a result's $options fills it.
			case InputText, InputPassword, InputTextarea, InputNumber, InputSwitch, InputFile, InputSelect:
			default:
				return fmt.Errorf("tool %q: input %q has unknown type %q", t.ID, in.ID, in.Type)
			}
		}
		for j := range t.Actions {
			a := &t.Actions[j]
			if a.ID == "" {
				return fmt.Errorf("tool %q: action %d has no id", t.ID, j+1)
			}
			if a.Label == "" {
				a.Label = a.ID
			}
			if a.Style == "" {
				a.Style = StyleOutline
				if j == 0 {
					a.Style = StylePrimary
				}
			}
			for _, id := range a.Requires {
				if !seen[id] {
					return fmt.Errorf("tool %q: action %q requires an unknown input %q", t.ID, a.ID, id)
				}
			}
			switch a.Style {
			case StylePrimary, StyleSecondary, StyleOutline, StyleGhost, StyleDanger, StyleDangerOutline:
			default:
				return fmt.Errorf("tool %q: action %q has unknown style %q（可选 primary、secondary、outline、ghost、danger、danger-outline）", t.ID, a.ID, a.Style)
			}
		}
		for j := range t.Outputs {
			o := &t.Outputs[j]
			if o.ID == "" {
				return fmt.Errorf("tool %q: output %d has no id", t.ID, j+1)
			}
			if o.Type == "" {
				o.Type = OutputText
			}
			if o.Type != OutputText && o.Type != OutputTable {
				return fmt.Errorf("tool %q: output %q has unknown type %q", t.ID, o.ID, o.Type)
			}
			if o.Label == "" {
				o.Label = o.ID
			}
		}
	}
	return nil
}
