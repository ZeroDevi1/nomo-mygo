package shell

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"nomo/desktop/internal/scholar"
	"nomo/desktop/typeset"
)

func (s *Shell) manuscriptAsset(v *manuscriptView, path string) (string, bool) {
	project := s.manuscriptBlockProject(v)
	base, root := filepath.Dir(v.path), project.Root
	if root == "" {
		root = base
	}
	if strings.HasPrefix(path, "/") && !typeset.IsLaTeXPath(v.path) {
		base, path = root, strings.TrimPrefix(path, "/")
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", false
	}
	bases := []string{base}
	if typeset.IsLaTeXPath(v.path) && project.Main != "" {
		bases = append(bases, filepath.Dir(project.Main))
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", false
	}
	for _, dir := range bases {
		path := filepath.Join(dir, filepath.FromSlash(path))
		if filepath.Ext(path) == "" && typeset.IsLaTeXPath(v.path) {
			path += ".tex"
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		if _, err := scholar.Relative(root, resolved); err == nil {
			if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() {
				return resolved, true
			}
		}
	}
	return "", false
}

func (s *Shell) loadManuscriptData(v *manuscriptView, f *manuscriptAuthorForm, path string) {
	file, ok := s.manuscriptAsset(v, path)
	if !ok {
		return
	}
	f.dataFile = file
	in, err := os.Open(file)
	if err != nil {
		return
	}
	defer in.Close()
	const maxPreviewBytes = 64 << 10
	input := io.Reader(io.LimitReader(in, maxPreviewBytes))
	if tab := s.scholarTab(file); tab != nil && tab.Dirty() {
		snap := tab.Source().Buffer.Snapshot()
		input = strings.NewReader(snap.Slice(0, min(snap.Len(), maxPreviewBytes)))
	}
	if strings.EqualFold(filepath.Ext(file), ".tex") {
		if data, err := io.ReadAll(input); err == nil {
			f.dataRows = manuscriptPreviewRows(typeset.TeXTablePreview(string(data)))
		}
		return
	}
	reader := csv.NewReader(input)
	reader.FieldsPerRecord = -1
	rows := manuscriptPreviewRows(f.native.Fields["data"])
	for range 32 {
		row, err := reader.Read()
		if err != nil {
			break
		}
		rows = append(rows, row)
	}
	if _, err := reader.Read(); !errors.Is(err, io.EOF) && len(rows) >= 32 {
		rows = append(rows, []string{"…"})
	}
	f.dataRows = rows
}

func manuscriptPreviewRows(data string) [][]string {
	var rows [][]string
	for line := range strings.Lines(data) {
		if len(rows) >= 32 {
			break
		}
		row := strings.SplitN(strings.TrimSpace(line), "|", 13)
		for i := range row {
			row[i] = strings.TrimSpace(row[i])
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *Shell) manuscriptNativeFields(c *ui.Context, v *manuscriptView) {
	f, obj := &v.author, v.author.native
	if obj.Label != "" {
		ui.Text(c.Key("label"), obj.Label).FontSize(12).TextColor(s.ch.muted)
	}
	ui.Text(c.Key("native-help"), s.tr("author-nativeHelp")).Wrap().FontSize(12).TextColor(s.ch.muted)
	if _, ok := obj.Ranges["path"]; ok {
		s.manuscriptAssetCard(c.Key("file-card"), v)
		if f.showPath {
			s.manuscriptPathField(c, v)
		}
	}
	if _, ok := obj.Ranges["width"]; ok {
		s.manuscriptWidthChoices(c.Key("image-width"), f)
	}
	if _, ok := obj.Ranges["caption"]; ok {
		s.authorField(c, f, authorField{key: "caption", multiline: true})
	}
	if obj.DataPath != "" {
		ui.Text(c.Key("data-path"), obj.DataPath).Wrap()
		if f.dataFile == "" {
			ui.Text(c.Key("data-missing"), s.tr("author-dataUnavailable")).Wrap()
		} else if ui.Button(c.Key("open-data"), s.tr("author-openData")).Clicked() {
			f.open = false
			s.OpenFiles([]string{f.dataFile})
		}
	}
	if len(f.dataRows) != 0 {
		ui.Column(c.Key("data-preview")).FillWidth().Padding(8).Gap(1).Radius(8).
			Background(c.Theme().SurfaceHover).Border(1, s.ch.separator).Children(func() {
			ui.Text(c.Key("label"), s.tr("author-dataPreview")).Bold().FontSize(12)
			s.manuscriptTableGrid(c, f.dataRows)
		})
	}
}

func (v *manuscriptView) nativePageSettings(snap editor.Snapshot, latex bool, limit int) *typeset.NativePageSettings {
	if latex {
		head, ok := authorLaTeXHead(snap)
		if !ok {
			return nil
		}
		return typeset.ReadNativePageSettings(head[:min(len(head), limit)], true)
	}
	end := min(limit, authorHeadLimit, v.editor.ManuscriptSetupEnd())
	return typeset.ReadNativePageSettings(snap.Slice(0, end), false)
}

func applyManuscriptSourceChanges(v *manuscriptView, base int, changes []typeset.SourceChange) {
	if len(changes) == 0 {
		return
	}
	edits := make([]editor.Edit, 0, len(changes))
	for _, change := range changes {
		edits = append(edits, editor.Edit{Start: base + change.Start, End: base + change.End, Text: change.Text})
	}
	move := func(pos int) int {
		delta := 0
		for _, edit := range edits {
			if pos < edit.Start {
				break
			}
			if pos < edit.End {
				return edit.Start + delta + min(pos-edit.Start, len(edit.Text))
			}
			delta += len(edit.Text) - (edit.End - edit.Start)
		}
		return pos + delta
	}
	sel := v.editor.Doc.Sel
	sel.Anchor, sel.Head = move(sel.Anchor), move(sel.Head)
	v.editor.ApplyManuscriptEdits(edits, sel)
}

func (s *Shell) manuscriptNativePageFields(c *ui.Context, v *manuscriptView) {
	f := &v.author
	ui.Text(c.Key("hint"), s.tr("author-nativePageHelp")).Wrap().FontSize(12).TextColor(s.ch.muted)
	var fields []authorField
	switch f.section {
	case 0:
		fields = []authorField{
			{key: "paper", choices: []string{"a4", "a5", "us-letter"}},
			{key: "margin"}, {key: "margin-top"}, {key: "margin-bottom"}, {key: "margin-left"}, {key: "margin-right"},
		}
	case 1:
		fields = authorTextFields
	case 2:
		fields = []authorField{
			{key: "justify", choices: []string{"true", "false"}}, {key: "line"}, {key: "leading-pt"}, {key: "leading-em"},
			{key: "spacing"}, {key: "spacing-em"}, {key: "indent"}, {key: "indent-em"},
		}
	}
	for _, field := range fields {
		if _, ok := f.nativePage.Values[field.key]; ok {
			s.authorField(c, f, field)
		}
	}
	if ui.Button(c.Key("extras"), s.tr("author-pageExtras")).Clicked() {
		s.startAuthoring(v, "pageExtras")
		return
	}
	if ui.Button(c.Key("source"), s.tr("author-openTemplate")).Clicked() {
		f.open = false
		v.editor.Select(0, 0)
		v.editor.ScrollToSource(0)
		if p := s.scholarForTab(v.tab); p != nil {
			s.setTypesetView(p, v.tab, typesetSource)
		} else {
			s.closeManuscriptEditor()
			v.tab.typesetMode = typesetSource
			s.focusEditor = true
		}
	}
}

func (s *Shell) readManuscriptListing(v *manuscriptView) (string, string) {
	file, ok := s.manuscriptAsset(v, v.editor.ManuscriptListing())
	if !ok {
		return "", ""
	}
	const limit = 32 << 10
	if tab := s.scholarTab(file); tab != nil && tab.Dirty() {
		snap := tab.Source().Buffer.Snapshot()
		return file, listingPreview(snap.Slice(0, min(limit+1, snap.Len())), limit)
	}
	in, err := os.Open(file)
	if err != nil {
		return "", ""
	}
	defer in.Close()
	data, err := io.ReadAll(io.LimitReader(in, limit+1))
	if err != nil {
		return "", ""
	}
	return file, listingPreview(string(data), limit)
}

func listingPreview(data string, limit int) string {
	if len(data) > limit {
		return strings.ToValidUTF8(data[:limit], "�") + "\n…"
	}
	return strings.ToValidUTF8(data, "�")
}

func (s *Shell) manuscriptListingFields(c *ui.Context, v *manuscriptView) {
	f := &v.author
	if f.dataFile == "" {
		ui.Text(c.Key("missing"), s.tr("author-dataUnavailable")).Wrap()
		return
	}
	ui.Text(c.Key("path"), filepath.Base(f.dataFile)).Bold()
	ui.TextArea(c.Key("listing"), &f.dataPreview).ReadOnly(true).Label(s.tr("author-externalCode")).FillWidth().Height(250)
	if ui.Button(c.Key("open"), s.tr("author-openData")).Clicked() {
		f.open = false
		s.OpenFiles([]string{f.dataFile})
	}
}
