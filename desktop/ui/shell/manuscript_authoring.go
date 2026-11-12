package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/typeset"
	"nomo/desktop/ui/mdedit"
)

const authorHeadLimit = 64 << 10

type manuscriptAuthorForm struct {
	open          bool
	blocked       bool
	section       int
	action, issue string
	version       uint64
	start, end    int
	selected      string
	initialText   string
	inputs        map[string]*string
	formula       *manuscriptFormulaTarget
	native        *typeset.AuthorObject
	nativePage    *typeset.NativePageSettings
	dataFile      string
	dataPreview   string
	dataRows      [][]string
	references    []typeset.ManuscriptReference
	refSearch     string
	refKind       int
	commands      []string
	headingPrefix string
	headingLevel  string
	asset         manuscriptAuthorAsset
	assetBusy     bool
	showPath      bool
	serial        uint64
}

func (f *manuscriptAuthorForm) input(key string) *string {
	value := f.inputs[key]
	if value == nil {
		value = new(string)
		f.inputs[key] = value
	}
	return value
}

func (f *manuscriptAuthorForm) value(key string) string {
	if value := f.inputs[key]; value != nil {
		return *value
	}
	return ""
}

func (f *manuscriptAuthorForm) setValue(key, value string) {
	*f.input(key) = value
}

func (f *manuscriptAuthorForm) setValues(values typeset.AuthorValues) {
	for key, value := range values {
		f.setValue(key, value)
	}
}

func (f *manuscriptAuthorForm) values() typeset.AuthorValues {
	values := make(typeset.AuthorValues, len(f.inputs))
	for key, value := range f.inputs {
		values[key] = *value
	}
	return values
}

type manuscriptFormulaTarget struct {
	before, after, display string
}

func authorPageAction(action string) bool {
	return action == typeset.AuthorPage || action == "pageExtras"
}

var authorEquationLabel = regexp.MustCompile(`\\label\{([A-Za-z0-9_:.-]+)\}`)

func manuscriptPageStyle(snap editor.Snapshot, latex bool) (start, end int, fields typeset.AuthorValues, malformed bool) {
	head := snap.Slice(0, min(snap.Len(), authorHeadLimit))
	prefix, close := typeset.AuthorStylePrefix(latex, typeset.AuthorPage), typeset.AuthorStyleEnd(latex, typeset.AuthorPage)
	offset := 0
	for line := range strings.Lines(head) {
		if latex && strings.HasPrefix(strings.TrimSpace(line), `\begin{document}`) {
			break
		}
		if strings.HasPrefix(line, prefix) {
			at := offset + len(line)
			for candidate := range strings.Lines(head[at:]) {
				at += len(candidate)
				if strings.TrimSpace(candidate) == close {
					values, _, valid := typeset.ReadAuthorStyle(latex, typeset.AuthorPage, head[offset:at])
					return offset, at, values, !valid
				}
			}
			return offset, offset, nil, true
		}
		offset += len(line)
	}
	return 0, 0, nil, false
}

func (v *manuscriptView) canvasStyle(latex bool) typeset.AuthorCanvasStyle {
	version := v.editor.ManuscriptStyleVersion()
	if !v.styleCached || v.styleVersion != version {
		snap := v.buffer.Snapshot()
		a, z, extra, _ := manuscriptPageStyle(snap, latex)
		limit := snap.Len()
		if z > a {
			limit = a
		}
		fields := make(typeset.AuthorValues)
		if native := v.nativePageSettings(snap, latex, limit); native != nil {
			maps.Copy(fields, native.Values)
		}
		maps.Copy(fields, extra)
		v.canvas = fields.CanvasStyle()
		v.styleVersion, v.styleCached = version, true
	}
	return v.canvas
}

func (s *Shell) startAuthoring(v *manuscriptView, action string) {
	s.startManuscriptAuthoring(v, action, false)
}

func (s *Shell) startManuscriptAuthoring(v *manuscriptView, action string, insertAsset bool) {
	ed := v.editor
	ed.UnmarkText()
	ed.SyncManuscript()
	snap := v.buffer.Snapshot()
	a, z := ed.Doc.Sel.Range()
	latex := typeset.IsLaTeXPath(v.path)
	f := manuscriptAuthorForm{open: true, action: action, version: snap.Version(), start: a, end: z,
		inputs: make(map[string]*string), serial: v.author.serial + 1}
	var title string
	var heading bool
	var insertingAsset bool
	var styledText string
	var styled bool
	switch action {
	case typeset.AuthorPage, "pageExtras":
		a, z, fields, malformed := manuscriptPageStyle(snap, latex)
		if malformed {
			f.issue = "manuscriptStyleSourceChanged"
		} else {
			if action == typeset.AuthorPage {
				limit := snap.Len()
				if z > a {
					limit = a
				}
				f.nativePage = v.nativePageSettings(snap, latex, limit)
			}
			if f.nativePage != nil {
				f.setValues(f.nativePage.Values)
			} else {
				f.setValues(fields)
			}
		}
	case typeset.AuthorParagraph:
		if start, end, ok := ed.ManuscriptParagraph(); ok {
			f.start, f.end = start, end
		} else {
			f.issue = "manuscriptStyleSelectParagraph"
		}
	case "text":
		if start, end, body, fields, ok := ed.ManuscriptTextStyle(); ok {
			f.start, f.end, styledText, styled = start, end, body, true
			f.setValues(fields)
		}
	case "equation":
		if start, end, bodyStart, bodyEnd, display, ok := ed.ManuscriptFormula(); ok {
			f.start, f.end = start, end
			body := snap.Slice(bodyStart, bodyEnd)
			before, after := snap.Slice(start, bodyStart), snap.Slice(bodyEnd, end)
			content := strings.TrimSpace(body)
			if content != "" {
				left := strings.Index(body, content)
				before += body[:left]
				after = body[left+len(content):] + after
			}
			f.setValue("a", content)
			if display {
				f.setValue("display", "block")
			}
			switch {
			case strings.HasPrefix(before, `\begin{equation}`):
				f.setValue("display", "numbered")
				if match := authorEquationLabel.FindStringSubmatch(content); len(match) > 0 {
					f.setValue("name", match[1])
					f.setValue("a", strings.TrimSpace(strings.Replace(content, match[0], "", 1)))
				}
			case strings.HasPrefix(before, "#math.equation("):
				f.setValue("display", "numbered")
				if _, tail, ok := strings.Cut(after, " <"); ok {
					f.setValue("name", strings.TrimSuffix(tail, ">"))
				}
			default:
				f.formula = &manuscriptFormulaTarget{before: before, after: after, display: f.value("display")}
			}
		}
	case "heading":
		f.setValue("level", "1")
		if start, end, value, level, ok := ed.ManuscriptHeadingLine(); ok {
			f.start, f.end, title, heading = start, end, value, true
			f.setValue("level", strconv.Itoa(level))
			if latex {
				if name, _, ok := strings.Cut(strings.TrimSpace(snap.Slice(start, end)), "{"); ok &&
					slices.Contains([]string{`\part`, `\chapter`, `\section`, `\subsection`, `\subsubsection`, `\paragraph`, `\subparagraph`}, strings.TrimSuffix(name, "*")) {
					f.headingPrefix = name + "{"
					f.headingLevel = f.value("level")
				}
			}
		}
	case "table", "figure":
		if insertAsset {
			insertingAsset = true
		} else if start, end, fields, ok := ed.ManuscriptObject(action); ok {
			f.start, f.end = start, end
			f.setValues(fields)
			if data := fields["csv"]; action == "table" && data != "" {
				_ = json.Unmarshal([]byte(data), &f.asset.rows)
				f.asset.name = fields["csv-name"]
			}
		} else if start, end, object, ok := ed.NativeManuscriptObject(); ok {
			f.start, f.end, f.native = start, end, &object
			f.setValues(object.Fields)
			if object.Kind != action || len(object.Ranges) == 0 {
				f.issue = "author-nativeUnsupported"
			}
			if object.DataPath != "" {
				s.loadManuscriptData(v, &f, object.DataPath)
			}
			if len(f.dataRows) == 0 {
				f.dataRows = manuscriptPreviewRows(object.Fields["data"])
			}
		} else if ed.ManuscriptObjectKind() != "" {
			f.issue = "author-nativeUnsupported"
		} else {
			insertingAsset = true
		}
		if insertingAsset {
			if at, ok := ed.ManuscriptAssetInsertion(); ok {
				f.start, f.end = at, at
			} else {
				f.issue = "manuscriptStyleBodyOnly"
			}
		}
		if action == "figure" && f.value("path") != "" {
			f.asset = s.manuscriptFigureAsset(v, f.value("path"))
		}
	case "custom":
		f.commands = authorProjectNames(v, latex)
	case "reference":
		f.references = ed.ManuscriptReferenceChoices()
	case "externalCode":
		f.dataFile, f.dataPreview = s.readManuscriptListing(v)
	}
	if action != typeset.AuthorPage && f.end-f.start > 16<<10 {
		f.issue = "manuscriptStyleSelectionLimit"
	}
	if f.issue == "" && !insertingAsset && !authorPageAction(action) && action != "custom" && action != "externalCode" && !ed.ManuscriptBodyRange(f.start, f.end) {
		f.issue = "manuscriptStyleBodyOnly"
	}
	if f.issue == "" && action != typeset.AuthorPage {
		f.selected = snap.Slice(f.start, f.end)
		if heading {
			f.selected = title
		}
		if styled {
			f.selected = styledText
		}
		if action == typeset.AuthorParagraph {
			if fields, body, ok := typeset.ReadAuthorStyle(latex, action, f.selected); ok {
				f.setValues(fields)
				f.selected = body
			}
		}
	}
	f.initialText = f.selected
	f.blocked = f.issue != ""
	v.author = f
}

func authorLaTeXHead(snap editor.Snapshot) (string, bool) {
	head := snap.Slice(0, min(snap.Len(), authorHeadLimit))
	setup := mdedit.ManuscriptLaTeXPreamble(head)
	return setup, setup != "" || strings.HasPrefix(head, `\begin{document}`)
}

func (s *Shell) authorPackages(v *manuscriptView, snap editor.Snapshot, names []string) (string, int, error) {
	if len(names) == 0 || !typeset.IsLaTeXPath(v.path) {
		return "", 0, nil
	}
	head, standalone := authorLaTeXHead(snap)
	if !standalone {
		main := s.manuscriptBlockProject(v).Main
		if main != "" && !samePath(main, v.path) {
			if tab := s.scholarTab(main); tab != nil {
				head, _ = authorLaTeXHead(tab.Source().Buffer.Snapshot())
			} else if file, err := os.Open(main); err == nil {
				data, _ := io.ReadAll(io.LimitReader(file, authorHeadLimit))
				file.Close()
				head = mdedit.ManuscriptLaTeXPreamble(string(data))
			}
		}
	}
	var code strings.Builder
	for _, name := range names {
		// The guard also recognizes packages brought in by a document class.
		if !standalone {
			if strings.Contains(head, "{"+name+"}") {
				continue
			}
			return "", 0, errors.New("manuscriptStyleMainPackage")
		}
		marker := "% nomo-package:" + name
		if strings.Contains(head, marker+"\n") {
			continue
		}
		writeAuthorPackage(&code, name)
	}
	return code.String(), len(head), nil
}

func writeAuthorPackage(code *strings.Builder, name string) {
	option := ""
	if name == "ulem" {
		option = "[normalem]"
	}
	fmt.Fprintf(code, "\\makeatletter\n\\@ifpackageloaded{%s}{}{\\RequirePackage%s{%s}}\n\\makeatother %% nomo-package:%s\n", name, option, name, name)
}

func (s *Shell) applyAuthorPage(v *manuscriptView, snap editor.Snapshot, latex bool) error {
	f := &v.author
	if f.nativePage != nil {
		changes, err := f.nativePage.Changes(f.values())
		if err != nil {
			return err
		}
		applyManuscriptSourceChanges(v, 0, changes)
		return nil
	}
	source, err := typeset.AuthorStyleSource(latex, typeset.AuthorPage, f.values(), "")
	if err != nil {
		return err
	}
	a, z, _, malformed := manuscriptPageStyle(snap, latex)
	if malformed {
		return errors.New("manuscriptStyleSourceChanged")
	}
	if a == z {
		if source == "" {
			return nil
		}
		if latex {
			head, ok := authorLaTeXHead(snap)
			if !ok {
				if snap.Len() <= authorHeadLimit && strings.TrimSpace(snap.Slice(0, snap.Len())) == "" {
					start := "\\documentclass{article}\n" + source + "\\begin{document}\n"
					end := "\n\\end{document}\n"
					v.editor.ApplyManuscriptEdits([]editor.Edit{{Start: 0, End: snap.Len(), Text: start + end}}, mdedit.Selection{Anchor: len(start), Head: len(start)})
					return nil
				}
				return errors.New("manuscriptStyleMainOnly")
			}
			a, z = len(head), len(head)
		} else {
			a = v.editor.ManuscriptSetupEnd()
			z = a
			if a > 0 && snap.Slice(a-1, a) != "\n" {
				source = "\n" + source
			}
		}
	}
	sel := v.editor.Doc.Sel
	move := func(pos int) int {
		if pos >= z {
			return pos + len(source) - (z - a)
		}
		if pos >= a {
			return a + len(source)
		}
		return pos
	}
	v.editor.ApplyManuscriptEdits([]editor.Edit{{Start: a, End: z, Text: source}}, mdedit.Selection{Anchor: move(sel.Anchor), Head: move(sel.Head)})
	return nil
}

func (s *Shell) applyAuthoring(v *manuscriptView) error {
	f := &v.author
	snap := v.buffer.Snapshot()
	if f.version != snap.Version() {
		return errors.New("manuscriptStyleSelectionChanged")
	}
	latex := typeset.IsLaTeXPath(v.path)
	if authorPageAction(f.action) {
		return s.applyAuthorPage(v, snap, latex)
	}
	if f.native != nil {
		changes, err := f.native.Changes(f.values(), latex)
		if err != nil {
			return err
		}
		applyManuscriptSourceChanges(v, f.start, changes)
		return nil
	}
	a, z := f.start, f.end
	out := typeset.AuthorInsert{}
	var err error
	if f.action == typeset.AuthorParagraph {
		out.Body, err = typeset.AuthorStyleSource(latex, f.action, f.values(), f.selected)
		out.Block = true
		if f.value("color") != "" || f.value("background") != "" {
			out.Packages = append(out.Packages, "xcolor")
		}
		if f.value("font") != "" {
			out.Packages = append(out.Packages, "fontspec")
		}
	} else if f.action == "table" || f.action == "figure" {
		out, err = typeset.AuthorObjectSource(latex, f.action, f.values())
	} else {
		selected := f.selected
		if (f.action == "heading" || f.action == "link") && selected != f.initialText {
			selected = typeset.AuthorLiteral(latex, selected)
		}
		out, err = typeset.AuthorCommand(latex, f.action, f.values(), selected)
		if f.action == "heading" && f.headingPrefix != "" && f.value("level") == f.headingLevel {
			out.Before, out.After = f.headingPrefix, "}"
		}
		if f.formula != nil && f.value("display") == f.formula.display {
			out.Before, out.After, out.Block = f.formula.before, f.formula.after, false
		}
	}
	if err != nil {
		return err
	}
	if !out.Block && f.action != "custom" && f.action != "equation" && strings.Contains(f.selected, "\n") {
		return errors.New("manuscriptStyleInlineSelection")
	}
	if latex && f.action != "custom" {
		if head, ok := authorLaTeXHead(snap); ok && a < len(head)+len(`\begin{document}`) {
			return errors.New("manuscriptStyleBodyOnly")
		}
	}
	// Clicking the same formatting button again removes a wrapper around the current selection.
	if f.action != typeset.AuthorParagraph && f.action != "equation" && out.Before != "" && out.After != "" && !out.Block &&
		a >= len(out.Before) && z+len(out.After) <= snap.Len() &&
		snap.Slice(a-len(out.Before), a) == out.Before && snap.Slice(z, z+len(out.After)) == out.After {
		a, z = a-len(out.Before), z+len(out.After)
		out.Before, out.After, out.Packages = "", "", nil
	}
	if out.Block {
		if a > 0 {
			if snap.Slice(a-1, a) != "\n" {
				out.Before = "\n\n" + out.Before
			} else if a > 1 && snap.Slice(a-2, a-1) != "\n" {
				out.Before = "\n" + out.Before
			}
		}
		if z < snap.Len() {
			if snap.Slice(z, z+1) != "\n" {
				out.After += "\n\n"
			} else if z+1 < snap.Len() && snap.Slice(z+1, z+2) != "\n" {
				out.After += "\n"
			}
		} else {
			out.After += "\n"
		}
	}
	if latex && f.action != "custom" && snap.Len() <= authorHeadLimit && strings.TrimSpace(snap.Slice(0, snap.Len())) == "" {
		var head strings.Builder
		head.WriteString("\\documentclass{article}\n")
		for _, pkg := range out.Packages {
			writeAuthorPackage(&head, pkg)
		}
		head.WriteString("\\begin{document}\n")
		bodyStart := head.Len() + len(out.Before)
		src := head.String() + out.Source() + "\n\\end{document}\n"
		v.editor.ApplyManuscriptEdits([]editor.Edit{{Start: 0, End: snap.Len(), Text: src}},
			mdedit.Selection{Anchor: bodyStart, Head: bodyStart + len(out.Body)})
		return nil
	}
	packages, at, err := s.authorPackages(v, snap, out.Packages)
	if err != nil {
		return err
	}
	change := editor.Edit{Start: a, End: z, Text: out.Source()}
	edits := []editor.Edit{change}
	if packages != "" {
		if at > a {
			return errors.New("manuscriptStyleBodyOnly")
		}
		edits = slices.Insert(edits, 0, editor.Edit{Start: at, End: at, Text: packages})
	}
	bodyStart := a + len(out.Before) + len(packages)
	bodyEnd := bodyStart + len(out.Body)
	if f.action == typeset.AuthorParagraph || out.Block && f.action != "heading" && f.action != "equation" && f.action != "bullet" && f.action != "ordered" {
		bodyStart = bodyEnd + len(out.After)
		bodyEnd = bodyStart
	}
	v.editor.ApplyManuscriptEdits(edits, mdedit.Selection{Anchor: bodyStart, Head: bodyEnd})
	return nil
}

func (s *Shell) authorRun(v *manuscriptView, action string, showForm bool) {
	s.startAuthoring(v, action)
	if showForm || v.author.issue != "" {
		return
	}
	if err := s.applyAuthoring(v); err != nil {
		v.author.issue = err.Error()
		return
	}
	v.author.open = false
	s.focusEditor = true
}

func (s *Shell) manuscriptAuthorToolbar(c *ui.Context, v *manuscriptView) {
	width, _ := c.Size()
	if s.scholar != nil {
		width = s.scholarWorkspaceWidthAt(width)
	}
	ui.Row(c).FillWidth().Height(36).Shrink(0).Padding(0, 8).Gap(5).AlignItems(ui.Center).
		Background(c.Theme().Background).Border(1, s.ch.separator).BorderWidth(0, 0, 1, 0).Children(func() {
		if width >= 650 {
			for _, action := range []string{"bold", "italic", "underline"} {
				if s.toolButton(c.Key(action), action, s.tr("author-"+action), false).FocusRing(true).Clicked() {
					s.authorRun(v, action, false)
				}
			}
		}
		groups := []struct {
			title, icon string
			actions     []string
		}{
			{"character", "type", []string{"text", "bold", "italic", "underline", "strike", "monospace", "super", "sub"}},
			{"paragraph", "align-left", []string{"paragraph", "heading", "bullet", "ordered", "quote"}},
			{"page", "file-text", []string{"page", "pagebreak", "outline"}},
			{"insert", "plus", []string{"equation", "table", "figure", "link", "footnote", "citation", "reference", "label", "custom"}},
		}
		for _, group := range groups {
			label := s.tr("author-group-" + group.title)
			s.scholarMenuButton(c.Key(group.title), group.icon, label, width >= 355).Menu(func(m *ui.Menu) {
				for _, action := range group.actions {
					if m.Item(s.tr("author-" + action)).Chosen() {
						show := slices.Contains([]string{"text", "paragraph", "heading", "page", "equation", "table", "figure", "link", "citation", "reference", "label", "custom"}, action)
						s.authorRun(v, action, show)
					}
				}
			})
		}
		if kind := v.editor.ManuscriptObjectKind(); kind != "" {
			label := s.tr("author-edit-" + kind)
			if ui.Button(c.Key("edit-object"), label).Clicked() {
				s.authorRun(v, kind, true)
			}
		}
		if v.editor.ManuscriptListing() != "" && ui.Button(c.Key("external-code"), s.tr("author-externalCode")).Clicked() {
			s.authorRun(v, "externalCode", true)
		}
		if width >= 500 && s.toolButton(c.Key("formula"), "sigma", s.tr("author-equation"), false).FocusRing(true).Clicked() {
			s.authorRun(v, "equation", true)
		}
		if p := s.compile; width >= 650 && p != nil && p.tab == v.tab && p.busy &&
			(p.referenceIndex == nil || p.fullVersion != p.version || p.sourceVersion != v.buffer.Version()) {
			ui.Text(c.Key("number-status"), s.tr("author-numberPending")).FontSize(11).TextColor(s.ch.muted)
		}
	})
}
