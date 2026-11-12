package shell

import (
	"regexp"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/typeset"
)

type authorField struct {
	key, hint string
	label     string
	choices   []string
	multiline bool
}

var authorTextFields = []authorField{{key: "font", hint: "Times New Roman"}, {key: "size", hint: "12"}, {key: "color", hint: "#242424"}, {key: "background", hint: "#FFF4C2"}}
var authorParagraphFields = []authorField{
	{key: "line", hint: "1.5"}, {key: "spacing", hint: "6"}, {key: "indent", hint: "24"},
	{key: "alignment", choices: []string{"", "left", "center", "right", "justify"}},
}

var authorPaperFields = []authorField{
	{key: "paper", choices: []string{"", "a4", "a5", "us-letter"}},
	{key: "orientation", choices: []string{"", "portrait", "landscape"}},
	{key: "margin", hint: "25"},
	{key: "columns", choices: []string{"", "1", "2"}},
	{key: "numbering", choices: []string{"", "none", "1", "i", "I"}},
	{key: "header"}, {key: "footer"},
}

var authorFormulaTemplates = []string{"", "fraction", "root", "power", "subscript", "integral", "sum", "limit", "matrix", "cases"}

func (s *Shell) authorChoice(c *ui.Context, key, label string, selected *string, options []string) {
	labels := make([]string, len(options))
	for i, option := range options {
		labels[i] = s.tr("author-option-" + option)
		if option == "" {
			if custom := s.tr("author-default-" + key); custom != "author-default-"+key {
				labels[i] = custom
			}
		}
	}
	index := max(0, slices.Index(options, *selected))
	current := labels[index]
	if ui.Select(c, &current, labels).FillWidth().Label(label).Changed() {
		if i := slices.Index(labels, current); i >= 0 {
			*selected = options[i]
		}
	}
}

func (s *Shell) authorField(c *ui.Context, f *manuscriptAuthorForm, field authorField) {
	key := field.key
	label := s.tr("author-field-" + key)
	if field.label != "" {
		label = s.tr("author-field-" + field.label)
	}
	value := f.input(key)
	ui.Column(c.Key(key)).FillWidth().Gap(4).Children(func() {
		ui.Text(c, label).FontSize(12).Bold()
		switch {
		case len(field.choices) != 0:
			s.authorChoice(c.Key("select"), key, label, value, field.choices)
		case field.multiline:
			ui.TextArea(c.Key("input"), value).Label(label).FillWidth().Height(84).MaxLength(8192).Placeholder(field.hint)
		default:
			ui.TextInput(c.Key("input"), value).Label(label).FillWidth().MaxLength(512).Placeholder(field.hint)
		}
		if key == "color" || key == "background" {
			ui.Row(c.Key("swatches")).Gap(6).Children(func() {
				for _, color := range []string{"#242424", "#1F4E79", "#18775C", "#A52236", "#FFF4C2", "#E7F0FF", "#F2EAF9", "#FFFFFF"} {
					button := ui.ButtonBase(c.Key(color)).Size(22, 22).Radius(4).Background(ui.Hex(color)).
						Label(color).Tooltip(color).FocusRing(true).Border(1, s.ch.separator)
					if strings.EqualFold(*value, color) {
						button.Border(2, s.ch.accent)
					}
					if button.Clicked() {
						*value = color
					}
				}
			})
		}
	})
}

func (s *Shell) authorFormulaFields(c *ui.Context, f *manuscriptAuthorForm, latex bool) {
	s.authorField(c, f, authorField{key: "display", choices: []string{"", "block", "numbered"}})
	s.authorField(c, f, authorField{key: "template", choices: authorFormulaTemplates})
	fields := []authorField{{key: "a", hint: "x = y", multiline: true}}
	switch f.value("template") {
	case "fraction":
		fields = []authorField{{key: "a", label: "numerator", hint: "x + 1"}, {key: "b", label: "denominator", hint: "2"}}
	case "root":
		fields = []authorField{{key: "a", hint: "x"}}
	case "power", "subscript":
		fields = []authorField{{key: "a", hint: "x"}, {key: "b", hint: "2"}}
	case "integral", "sum", "limit":
		fields = []authorField{{key: "a", hint: "0"}, {key: "b", hint: "1"}, {key: "c", hint: "x"}}
	case "matrix":
		fields = []authorField{{key: "data", label: "matrix", hint: "a | b\nc | d", multiline: true}}
	case "cases":
		fields = []authorField{{key: "a", hint: "a, b"}, {key: "b", hint: "c, d"}}
	}
	for _, field := range fields {
		s.authorField(c, f, field)
	}
	if f.value("display") == "numbered" {
		s.authorField(c, f, authorField{key: "name", hint: "eq:example"})
	}
	ui.Row(c.Key("symbols")).Gap(5).Children(func() {
		for _, symbol := range [][3]string{
			{"α", `\alpha`, "alpha"}, {"β", `\beta`, "beta"}, {"π", `\pi`, "pi"},
			{"∞", `\infty`, "infinity"}, {"≤", `\leq`, "<="}, {"→", `\to`, "->"},
			{"×", `\times`, "times"}, {"≠", `\ne`, "!="},
		} {
			if ui.Button(c.Key(symbol[0]), symbol[0]).Clicked() {
				source := symbol[2]
				if latex {
					source = symbol[1]
				}
				target := "a"
				if f.value("template") == "matrix" {
					target = "data"
				}
				if f.value(target) != "" {
					*f.input(target) += " "
				}
				*f.input(target) += source
			}
		}
	})
}

var authorLaTeXNames = regexp.MustCompile(`\\(?:newcommand|renewcommand|providecommand|DeclareMathOperator)\*?\s*\{?\s*\\([A-Za-z@]+)`)
var authorTypstNames = regexp.MustCompile(`(?m)^[\t ]*#let[\t ]+([\p{L}_][\p{L}\p{N}_-]*)\s*\(`)

func authorProjectNames(v *manuscriptView, latex bool) []string {
	snap := v.buffer.Snapshot()
	head := snap.Slice(0, min(authorHeadLimit, snap.Len()))
	pattern := authorTypstNames
	if latex {
		pattern = authorLaTeXNames
		head = mdeditPreambleForNames(v, snap)
	}
	var names []string
	for _, match := range pattern.FindAllStringSubmatch(head, 64) {
		if !slices.Contains(names, match[1]) {
			names = append(names, strings.Clone(match[1]))
		}
	}
	slices.Sort(names)
	return names
}

func mdeditPreambleForNames(v *manuscriptView, snap editor.Snapshot) string {
	head, ok := authorLaTeXHead(snap)
	if !ok {
		return v.mainPreamble
	}
	return head
}

func (s *Shell) authorFormFields(c *ui.Context, v *manuscriptView, latex bool) {
	f := &v.author
	if f.native != nil {
		s.manuscriptNativeFields(c, v)
		return
	}
	if f.nativePage != nil {
		s.manuscriptNativePageFields(c, v)
		return
	}
	var fields []authorField
	switch f.action {
	case "externalCode":
		s.manuscriptListingFields(c, v)
	case "page", "pageExtras":
		switch f.section {
		case 0:
			fields = authorPaperFields
		case 1:
			fields = authorTextFields
		case 2:
			fields = authorParagraphFields
		}
	case "paragraph":
		fields = append(slices.Clone(authorParagraphFields), authorTextFields...)
	case "text":
		fields = authorTextFields
	case "heading":
		fields = []authorField{{key: "level", choices: []string{"0", "1", "2", "3", "4", "5"}}}
		s.authorSelectionText(c, f, "title")
	case "equation":
		s.authorFormulaFields(c, f, latex)
	case "table":
		s.manuscriptAssetCard(c.Key("table-file"), v)
		fields = []authorField{{key: "caption"}}
		if f.value("csv") == "" {
			fields = append(fields, authorField{key: "data", hint: "A | B\n1 | 2", multiline: true})
		}
	case "figure":
		s.manuscriptAssetCard(c.Key("figure-file"), v)
		fields = []authorField{{key: "caption"}}
		s.manuscriptWidthChoices(c.Key("width"), f)
		if f.showPath {
			s.manuscriptPathField(c, v)
		}
	case "link":
		fields = []authorField{{key: "path", hint: "https://example.com"}}
		s.authorSelectionText(c, f, "title")
	case "reference":
		s.manuscriptReferencePicker(c, f)
		fields = []authorField{{key: "name", hint: "example-key"}}
	case "citation", "label":
		fields = []authorField{{key: "name", hint: "example-key"}}
	case "custom":
		kinds := []string{"", "set", "show", "source"}
		if latex {
			kinds = []string{"", "environment", "source"}
		}
		s.authorField(c, f, authorField{key: "kind", choices: kinds})
		if f.value("kind") == "source" {
			s.authorSelectionText(c, f, "source")
			return
		}
		if len(f.commands) > 0 {
			cmd := s.tr("author-projectCommands")
			if ui.Select(c.Key("project-command"), &cmd, f.commands).FillWidth().Label(cmd).Changed() {
				f.setValue("name", cmd)
			}
		}
		fields = []authorField{{key: "name"}, {key: "args", multiline: true}}
		if latex {
			fields = append(fields, authorField{key: "options"})
		}
	}
	for _, field := range fields {
		s.authorField(c, f, field)
	}
}

func (s *Shell) authorSelectionText(c *ui.Context, f *manuscriptAuthorForm, key string) {
	label := s.tr("author-field-" + key)
	ui.Text(c, label).FontSize(12).Bold()
	if key == "source" {
		ui.TextArea(c.Key(key), &f.selected).FillWidth().Height(128).MaxLength(16384).Label(label)
	} else {
		ui.TextInput(c.Key(key), &f.selected).FillWidth().MaxLength(512).Label(label)
	}
}

func (s *Shell) manuscriptAuthorDialog(c *ui.Context, v *manuscriptView) {
	f := &v.author
	if !f.open {
		return
	}
	latex := typeset.IsLaTeXPath(v.path)
	w, h := c.Size()
	ui.Modal(c.Key("author-modal"), &f.open, func() {
		ui.Column(c).Width(min(500, max(230, w-64))).Gap(10).Children(func() {
			title := "author-" + f.action
			if f.native != nil {
				title = "author-edit-" + f.native.Kind
			}
			ui.Text(c, s.tr(title)).FontSize(17).Bold()
			if authorPageAction(f.action) {
				ui.Segmented(c.Key("style-section"), &f.section, s.tr("author-group-page"), s.tr("author-group-character"), s.tr("author-group-paragraph")).FillWidth()
			}
			if authorPageAction(f.action) && f.nativePage == nil || f.action == "paragraph" || f.action == "text" {
				ui.Text(c, s.tr("author-inheritHelp")).FontSize(12).TextColor(s.ch.muted).Wrap()
			}
			if f.action == "custom" {
				ui.Text(c, s.tr("author-customHelp")).FontSize(12).TextColor(s.ch.muted).Wrap()
			}
			ui.Scroll(c.Key("fields")).FillWidth().MaxHeight(max(150, min(470, h-220))).Children(func() {
				ui.Column(c).FillWidth().Gap(8).Children(func() { s.authorFormFields(c, v, latex) })
			})
			if f.issue != "" {
				key := f.issue
				label := s.tr(key)
				if label == key {
					label = s.tr("author-invalid", "field", s.tr("author-field-"+key))
				}
				ui.Text(c, label).Wrap().TextColor(ui.Hex("#C53B38"))
			}
			ui.Row(c.Key("actions")).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				if (f.native != nil || (f.action == "figure" || f.action == "table") && f.start < f.end) &&
					ui.Button(c.Key("source"), s.tr("author-objectSource")).Disabled(f.assetBusy).Clicked() {
					v.editor.Select(min(f.start+1, f.end), min(f.start+1, f.end))
					f.open = false
					s.focusEditor = true
				}
				ui.Spacer(c)
				if ui.Button(c.Key("cancel"), s.tr("author-cancel")).Clicked() {
					f.open = false
					s.focusEditor = true
				}
				if f.action != "externalCode" && ui.Button(c.Key("apply"), s.tr("author-apply")).Disabled(f.blocked || f.assetBusy).Clicked() {
					s.applyManuscriptForm(v)
				}
			})
		})
	})
}
