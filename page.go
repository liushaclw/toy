package main

import (
	"github.com/vivaui/viva/app"
	"github.com/vivaui/viva/ui"

	"toy/internal/plugin"
)

// page declares the window: the tools on the left; on the right the chosen
// tool, its inputs beside its outputs, over a log that is one line until opened.
func (m *model) page(c ui.C) {
	theme := c.Theme()
	ui.Row(c).Fill().Children(func() {
		ui.Column(c).Width(m.split.Pos).Background(theme.Card).Pad(12).Gap(10).Children(func() { m.sidebar(c) })
		ui.Splitter(c, &m.split).Mins(180, 520)
		ui.Column(c).Grow(1).Children(func() {
			if m.showLog {
				ui.Column(c).Height(m.logPane.Pos).Children(func() { m.toolArea(c) })
				ui.Splitter(c, &m.logPane).Mins(240, 80)
				ui.Column(c).Grow(1).Children(func() { m.logPanel(c) })
				return
			}
			ui.Column(c).Grow(1).Children(func() { m.toolArea(c) })
			m.logBar(c)
		})
	})
	m.confirmDialog(c)
	ui.Toasts(c, &m.toaster)
}

// confirmDialog asks before an action that has Confirm runs.
func (m *model) confirmDialog(c ui.C) {
	if !m.confirm.open {
		return
	}
	cf := &m.confirm
	ui.ModalWith(c, &cf.open, "确认"+cf.action.Label, ui.ModalSpec{Width: 380}, func() {
		ui.Wrap(c, cf.text)
	}, func() {
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Outline().Clicked() {
				cf.open = false
			}
			yes := ui.Button(c, cf.action.Label).Disabled(cf.tool.running)
			if s := cf.action.Style; s == plugin.StyleDanger || s == plugin.StyleDangerOutline {
				yes.Destructive()
			}
			if yes.Clicked() {
				cf.open = false
				m.start(cf.tool, cf.action)
			}
		})
	})
}

func (m *model) toolArea(c ui.C) {
	if m.selected >= len(m.tools) {
		m.empty(c)
		return
	}
	tv := m.tools[m.selected]
	ui.Column(c.Key(tv.key)).Fill().Children(func() { m.toolPage(c, tv) })
}

func (m *model) sidebar(c ui.C) {
	theme := c.Theme()
	ui.Row(c).Items(ui.Center).Gap(8).Children(func() {
		ui.Text(c, "工具箱").FontSize(theme.Metrics.FontSizeL).Bold()
		ui.Spacer(c)
		if ui.Button(c, "重载").Ghost().Small().Tooltip("重新扫描插件目录").Clicked() {
			c.After(m.load)
		}
	})
	ui.TextField(c, &m.query, "搜索工具", "搜索…")
	ui.Scroll(c).Grow(1).Gap(2).Children(func() {
		for i, tv := range m.tools {
			if !tv.matches(m.query) {
				continue
			}
			row := ui.Row(c.Key(tv.key)).Gap(8).PadX(10).PadY(8).Items(ui.Center).
				Radius(theme.Metrics.RadiusControl).Hit(ui.ID(c), tv.title)
			if i == m.selected {
				row.Background(theme.Accent)
			}
			row.Hover(ui.Look{}.Background(theme.Hover))
			if row.Children(func() {
				ui.Column(c).Grow(1).Gap(2).Children(func() {
					ui.Text(c, tv.title).Ellipsis()
					ui.Text(c, tv.detail).Muted().FontSize(theme.Metrics.FontSizeS).Ellipsis()
				})
				if tv.broken != "" {
					ui.Text(c, "错误").FontSize(theme.Metrics.FontSizeS).Color(theme.Danger)
				} else if tv.running {
					ui.Spinner(c)
				}
			}).Clicked() {
				m.selected = i
				m.initTool(tv)
			}
		}
	})
	ui.Paragraph(c, "把插件放进 plugins 目录，保存后自动重载。", 2).Muted().FontSize(theme.Metrics.FontSizeS)
}

func (m *model) empty(c ui.C) {
	ui.Column(c).Fill().Center().Gap(8).Children(func() {
		ui.Text(c, "没有插件").FontSize(18).Bold()
		ui.Paragraph(c, "在 plugins 目录下新建一个带 plugin.toml 的文件夹。", 2).Muted()
	})
}

func (m *model) toolPage(c ui.C, tv *toolView) {
	theme := c.Theme()
	ui.Column(c).PadSides(16, 20, 12, 20).Gap(4).Children(func() {
		ui.Row(c).Gap(10).Items(ui.Baseline).Children(func() {
			ui.Text(c, tv.title).FontSize(18).Bold()
			ui.Text(c, tv.detail).Muted().FontSize(theme.Metrics.FontSizeS).Ellipsis().Shrink(1)
		})
		if tv.tool != nil && tv.tool.Description != "" {
			ui.Paragraph(c, tv.tool.Description, 2).Muted().FontSize(theme.Metrics.FontSizeS)
		}
	})
	ui.Divider(c)
	if tv.broken != "" {
		ui.Column(c).Pad(20).Children(func() { errorBox(c, "插件无法加载："+tv.broken) })
		return
	}
	ui.Row(c).Grow(1).Children(func() {
		// Inputs: they scroll, the actions stay at the bottom.
		ui.Column(c).Width(m.ioSplit.Pos).Children(func() {
			ui.Scroll(c).Grow(1).PadSides(16, 16, 16, 20).Gap(14).Children(func() {
				for i := range tv.tool.Inputs {
					in := &tv.tool.Inputs[i]
					ui.Column(c.Key("in/" + in.ID)).Gap(6).Children(func() { m.input(c, tv, i, in) })
				}
			})
			ui.Divider(c)
			ui.Column(c).PadSides(12, 16, 12, 20).Gap(8).Children(func() { m.actions(c, tv) })
		})
		ui.Splitter(c, &m.ioSplit).Mins(260, 260)
		// Outputs: they share the height; long text and tables scroll in place.
		m.outputs(c, tv)
	})
}

func (m *model) actions(c ui.C, tv *toolView) {
	theme := c.Theme()
	ui.Flow(c).Gap(8).Items(ui.Center).Children(func() {
		for j := range tv.tool.Actions {
			a := &tv.tool.Actions[j]
			b := ui.Button(c.Key("act/"+a.ID), a.Label).Disabled(tv.running)
			switch a.Style {
			case plugin.StyleSecondary:
				b.Secondary()
			case plugin.StyleOutline:
				b.Outline()
			case plugin.StyleGhost:
				b.Ghost()
			case plugin.StyleDanger:
				b.Destructive()
			case plugin.StyleDangerOutline:
				b.DestructiveOutline()
			}
			if b.Clicked() {
				m.press(tv, a)
			}
		}
	})
	if !tv.running && tv.status == "" {
		return
	}
	ui.Row(c).Gap(8).Items(ui.Center).Children(func() {
		if tv.running {
			if tv.progress < 0 {
				ui.Spinner(c)
			} else {
				ui.Progress(c, float32(tv.progress)).Grow(1).Height(6)
			}
			if ui.Button(c, "取消").Ghost().XSmall().Clicked() {
				m.mgr.Cancel(tv.job)
			}
			return
		}
		ui.Text(c, tv.status).Muted().FontSize(theme.Metrics.FontSizeS)
	})
}

// The output column's sizes. A label row and the gap under it.
const (
	outputHead    = 30  // an output's title row and the gap under it
	outputGap     = 12  // between outputs
	mainMinHeight = 240 // the main text output, however little room there is
	sideHeight    = 160 // a long text output that is not the main one
)

// outputs is a scroll area, as the inputs are: the main text output is sized to
// fill what the window shows, the others sit below it at their own height, and
// what does not fit is a scroll away. A table shows all its rows.
func (m *model) outputs(c ui.C, tv *toolView) {
	theme := c.Theme()
	area := ui.Scroll(c).Grow(1).PadSides(16, 20, 16, 16).Gap(outputGap).Scrollbars()
	view := area.ScrollState().Viewport // as of the last layout
	if view <= 0 {                      // the tool's first paint: lay out again at the real height
		c.Canvas().RequestFrame()
		view = 400
	}
	main := tv.main()
	mainHeight := max(mainMinHeight, view-tv.besideMain(main))
	area.Children(func() {
		if tv.err != "" {
			errorBox(c, tv.err)
		}
		shown := false
		for i := range tv.tool.Outputs {
			out := &tv.tool.Outputs[i]
			if !tv.hasOutput(i) {
				continue
			}
			shown = true
			height := float32(sideHeight)
			switch {
			case i == main:
				height = mainHeight - outputHead
			case tv.compact(i):
				height = rowsHeight(lineCount(tv.outText[i]))
			}
			ui.Column(c.Key("out/" + out.ID)).Gap(6).Children(func() { m.output(c, tv, i, out, height) })
		}
		if !shown && tv.err == "" {
			hint := "运行后结果显示在这里"
			if tv.running {
				hint = "运行中…"
			}
			ui.Column(c).Height(max(view, 120)).Center().Children(func() {
				ui.Text(c, hint).Muted().FontSize(theme.Metrics.FontSizeS)
			})
		}
	})
}

func (m *model) input(c ui.C, tv *toolView, i int, in *plugin.Input) {
	theme := c.Theme()
	if in.Type == plugin.InputSwitch {
		if ui.Switch(c, &tv.bools[i], in.Label).Changed() { // draws its label beside it
			m.changedInput(tv, in)
		}
		return
	}
	ui.Text(c, in.Label).FontSize(theme.Metrics.FontSizeS).Bold()
	switch in.Type {
	case plugin.InputTextarea:
		rows := in.Rows
		if rows <= 0 {
			rows = 5
		}
		ui.TextArea(c, &tv.texts[i], in.Label, in.Placeholder).Height(rowsHeight(rows))
	case plugin.InputPassword:
		ui.TextField(c, &tv.texts[i], in.Label, in.Placeholder).Secure()
	case plugin.InputSelect:
		if len(tv.options[i]) == 0 {
			ui.Text(c, "（暂无）").Muted().FontSize(theme.Metrics.FontSizeS)
			return
		}
		if ui.Select(c, &tv.choices[i], tv.options[i], nil).Placeholder("请选择").Label(in.Label).MaxWidth(320).Changed() {
			m.changedInput(tv, in)
		}
	case plugin.InputFile:
		ui.Row(c).Gap(8).Children(func() {
			ui.TextField(c, &tv.texts[i], in.Label, in.Placeholder).Grow(1)
			if ui.Button(c, "浏览…").Outline().Clicked() {
				target := &tv.texts[i]
				c.After(func() {
					if paths, err := app.OpenFiles(app.OpenFileOptions{Title: in.Label}); err == nil && len(paths) > 0 {
						*target = paths[0]
					}
				})
			}
		})
	default: // text, number
		ui.TextField(c, &tv.texts[i], in.Label, in.Placeholder)
	}
}

// output is one output under its title; height is its text box's (a table is as
// tall as its rows).
func (m *model) output(c ui.C, tv *toolView, i int, out *plugin.Output, height float32) {
	theme := c.Theme()
	ui.Row(c).Items(ui.Center).Gap(8).Children(func() {
		ui.Text(c, out.Label).FontSize(theme.Metrics.FontSizeS).Bold()
		ui.Spacer(c)
		if out.Copyable && tv.tables[i] == nil {
			if ui.Button(c, "复制").Ghost().XSmall().Clicked() {
				text := tv.outBox[i].Text() // as shown, edits included
				c.After(func() {
					if err := app.SetClipboardText(text); err == nil {
						m.toaster.Show("已复制", "", ui.ToastSuccess)
					}
				})
			}
		}
	})
	if t := tv.tables[i]; t != nil {
		ui.Table(c, &t.state, ui.TableSpec{Headers: t.headers, Columns: t.columns, Source: &t.rows, Striped: true})
		return
	}
	// A text area, so the result can be selected, scrolled and edited further.
	ui.TextArea(c, &tv.outBox[i], out.Label, "").Mono().Height(height)
}

func (m *model) logPanel(c ui.C) {
	theme := c.Theme()
	ui.Row(c).PadX(16).PadY(6).Gap(4).Items(ui.Center).Children(func() {
		ui.Text(c, "日志").FontSize(theme.Metrics.FontSizeS).Bold()
		ui.Spacer(c)
		if ui.Button(c, "清空").Ghost().XSmall().Clicked() {
			m.logs = m.logs[:0]
		}
		if ui.Button(c, "收起").Ghost().XSmall().Clicked() {
			m.showLog = false
		}
	})
	ui.Scroll(c).Grow(1).PadX(16).PadBottom(8).Gap(2).Follow(true).Children(func() {
		for _, line := range m.logs {
			ui.Wrap(c, line).Mono().FontSize(12).Muted()
		}
	})
}

// logBar is the log while closed: its last line, and a button to open it.
func (m *model) logBar(c ui.C) {
	ui.Divider(c)
	ui.Row(c).PadX(12).PadY(4).Gap(8).Items(ui.Center).Children(func() {
		if ui.Button(c, "日志").Ghost().XSmall().Tooltip("展开日志").Clicked() {
			m.showLog = true
		}
		last := ""
		if len(m.logs) > 0 {
			last = m.logs[len(m.logs)-1]
		}
		ui.Text(c, last).Mono().FontSize(12).Muted().Ellipsis().Grow(1)
	})
}

func errorBox(c ui.C, message string) {
	theme := c.Theme()
	ui.Paragraph(c, message, 12).Color(theme.Danger).Background(theme.Danger.WithAlpha(0.08)).
		Pad(10).Radius(theme.Metrics.RadiusControl)
}
