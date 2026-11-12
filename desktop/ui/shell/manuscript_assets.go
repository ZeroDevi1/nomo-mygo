package shell

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"nomo/desktop/typeset"
)

type manuscriptAuthorAsset struct {
	path, importBase string
	name             string
	rows             [][]string
	revision         int64
}

const manuscriptImageLimit = 32 << 20

func (s *Shell) dropManuscriptAsset(v *manuscriptView, paths []string) {
	latex := typeset.IsLaTeXPath(v.path)
	for _, path := range paths {
		if kind := manuscriptAssetKind(path, latex); kind != "" {
			if len(paths) != 1 {
				s.status = s.tr("author-dropInvalid")
				return
			}
			s.startManuscriptAuthoring(v, kind, true)
			if !v.author.blocked {
				s.stageManuscriptAsset(v, paths)
			}
			return
		}
	}
	s.openDropped(paths)
}

func manuscriptAssetKind(path string, latex bool) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return "table"
	case ".png", ".jpg", ".jpeg":
		return "figure"
	case ".pdf":
		if latex {
			return "figure"
		}
	case ".svg", ".gif":
		if !latex {
			return "figure"
		}
	}
	return ""
}

func (s *Shell) manuscriptAssetBase(v *manuscriptView) (base, root string) {
	project := s.manuscriptBlockProject(v)
	base, root = filepath.Dir(v.path), project.Root
	if root == "" {
		root = base
	}
	if typeset.IsLaTeXPath(v.path) && project.Main != "" {
		base = filepath.Dir(project.Main)
	}
	return base, root
}

func (s *Shell) stageManuscriptAsset(v *manuscriptView, files []string) {
	f := &v.author
	latex := typeset.IsLaTeXPath(v.path)
	if f.assetBusy {
		return
	}
	if len(files) != 1 || manuscriptAssetKind(files[0], latex) != f.action {
		f.issue = "author-dropInvalid"
		return
	}
	path, err := filepath.EvalSymlinks(files[0])
	if err != nil {
		f.issue = "author-dataUnavailable"
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		f.issue = "author-dataUnavailable"
		return
	}
	if f.action == "table" {
		if f.native != nil {
			return
		}
		rows, data, err := readManuscriptCSV(path, info.Size())
		if err != nil {
			f.issue = "author-csvLimit"
			return
		}
		f.asset = manuscriptAuthorAsset{name: filepath.Base(path), rows: rows}
		f.setValue("csv", data)
		f.setValue("csv-name", f.asset.name)
		f.setValue("data", "")
	} else {
		if info.Size() > manuscriptImageLimit || manuscriptAssetKind(path, latex) != "figure" {
			f.issue = "author-dropInvalid"
			return
		}
		base, root := s.manuscriptAssetBase(v)
		base, baseErr := filepath.EvalSymlinks(base)
		root, rootErr := filepath.EvalSymlinks(root)
		if baseErr != nil || rootErr != nil {
			f.issue = "author-dataUnavailable"
			return
		}
		fromRoot, rootRelErr := filepath.Rel(root, path)
		relative, relErr := filepath.Rel(base, path)
		asset := manuscriptAuthorAsset{path: path, importBase: base, revision: info.ModTime().UnixNano() ^ info.Size()}
		if rootRelErr == nil && filepath.IsLocal(fromRoot) && relErr == nil &&
			(!latex || !strings.ContainsAny(relative, "{}%\\#")) {
			asset.importBase = ""
		} else {
			relative = ""
		}
		f.asset = asset
		f.setValue("path", filepath.ToSlash(relative))
	}
	f.issue = ""
}

func readManuscriptCSV(path string, size int64) ([][]string, string, error) {
	const maxCSV = 8 << 10
	if size > maxCSV {
		return nil, "", errors.New("CSV exceeds the table form limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	reader := csv.NewReader(io.LimitReader(f, maxCSV+1))
	var rows [][]string
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(row) > 12 || len(rows) >= 30 {
			return nil, "", errors.New("CSV must be rectangular and fit the table form")
		}
		if len(rows) == 0 {
			row[0] = strings.TrimPrefix(row[0], "\ufeff")
		}
		rows = append(rows, row)
	}
	data, err := json.Marshal(rows)
	if err != nil || len(rows) == 0 || len(data) > maxCSV {
		return nil, "", errors.New("CSV exceeds the table form limit")
	}
	return rows, string(data), nil
}

var manuscriptAssetName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func importManuscriptFigure(asset manuscriptAuthorAsset) (string, error) {
	root, err := os.OpenRoot(asset.importBase)
	if err != nil {
		return "", err
	}
	defer root.Close()
	in, err := os.Open(asset.path)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if stat, err := in.Stat(); err != nil || !stat.Mode().IsRegular() || stat.Size() > manuscriptImageLimit {
		return "", errors.New("invalid image file")
	}
	if err := root.Mkdir("assets", 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(asset.path))
	stem := strings.Trim(manuscriptAssetName.ReplaceAllString(strings.TrimSuffix(filepath.Base(asset.path), filepath.Ext(asset.path)), "-"), ".-")
	if stem == "" {
		stem = "image"
	}
	stem = stem[:min(len(stem), 80)]
	for i := 1; i <= 64; i++ {
		name := stem
		if i > 1 {
			name += "-" + strconv.Itoa(i)
		}
		name = filepath.Join("assets", name+ext)
		out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		n, copyErr := io.Copy(out, io.LimitReader(in, manuscriptImageLimit+1))
		closeErr := out.Close()
		if n > 0 && n <= manuscriptImageLimit && copyErr == nil && closeErr == nil {
			return filepath.ToSlash(name), nil
		}
		_ = root.Remove(name)
		return "", errors.New("could not copy image")
	}
	return "", errors.New("no available image file name")
}

func (s *Shell) applyManuscriptForm(v *manuscriptView) {
	f := &v.author
	apply := func() bool {
		if err := s.applyAuthoring(v); err != nil {
			f.issue = err.Error()
			return false
		}
		f.open = false
		s.focusEditor = true
		return true
	}
	if f.asset.importBase == "" {
		apply()
		return
	}
	if f.version != v.buffer.Version() {
		f.issue = "manuscriptStyleSelectionChanged"
		return
	}
	post := s.post
	if post == nil {
		return
	}
	f.assetBusy = true
	serial, asset := f.serial, f.asset
	go func() {
		path, err := importManuscriptFigure(asset)
		removeCopy := func() {
			if root, err := os.OpenRoot(asset.importBase); err == nil {
				_ = root.Remove(filepath.FromSlash(path))
				root.Close()
			}
		}
		post(func() {
			if s.manuscript != v || f.serial != serial || !f.open || f.version != v.buffer.Version() {
				if err == nil {
					removeCopy()
				}
				if s.manuscript == v && f.serial == serial {
					f.assetBusy = false
					f.issue = "manuscriptStyleSelectionChanged"
				}
				return
			}
			f.assetBusy = false
			if err != nil {
				f.issue = "author-assetImportFailed"
				return
			}
			f.setValue("path", path)
			if apply() {
				f.asset = manuscriptAuthorAsset{}
			} else {
				f.setValue("path", "")
				removeCopy()
			}
		})
	}()
}

func (s *Shell) pickManuscriptAsset(v *manuscriptView) {
	serial, post, parent := v.author.serial, s.post, s.win
	if post == nil {
		return
	}
	extensions := []string{"csv"}
	if v.author.action == "figure" {
		extensions = []string{"png", "jpg", "jpeg"}
		if typeset.IsLaTeXPath(v.path) {
			extensions = append(extensions, "pdf")
		} else {
			extensions = append(extensions, "svg", "gif")
		}
	}
	label := s.tr("author-" + v.author.action)
	go func() {
		files, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: parent,
			Filters: []mygo.FileFilter{{Name: label, Extensions: extensions}}})
		if err == nil && len(files) != 0 {
			post(func() {
				if s.manuscript == v && v.author.open && v.author.serial == serial {
					s.stageManuscriptAsset(v, files)
				}
			})
		}
	}()
}

func (s *Shell) manuscriptAssetCard(c *ui.Context, v *manuscriptView) {
	f := &v.author
	card := ui.Column(c).FillWidth().Padding(10).Gap(8).Radius(8).Background(c.Theme().SurfaceHover).Border(1, s.ch.separator)
	if card.FileDragOver() {
		card.Border(2, s.ch.accent)
	}
	if files := card.DroppedFiles(); len(files) != 0 {
		s.stageManuscriptAsset(v, files)
	}
	card.Children(func() {
		if f.action == "figure" && f.asset.path != "" {
			var img ui.ImageSource
			var w, h float32
			if strings.EqualFold(filepath.Ext(f.asset.path), ".pdf") {
				img, w, h = v.editor.ManuscriptPDFPreview(f.asset.path, f.asset.revision)
			} else {
				img, w, h = v.editor.LocalImagePreview(url.PathEscape(f.asset.path))
			}
			if img != nil && w > 0 && h > 0 {
				scale := min(1, 360/w, 140/h)
				ui.Row(c.Key("thumbnail")).FillWidth().Justify(ui.Center).Children(func() {
					ui.Image(c, img).Size(w*scale, h*scale).Label(filepath.Base(f.asset.path))
				})
			}
		}
		if len(f.asset.rows) != 0 {
			s.manuscriptTableGrid(c.Key("preview"), f.asset.rows)
		}
		label := s.tr("author-drop-" + f.action)
		if f.asset.name != "" {
			label = f.asset.name
		} else if f.asset.path != "" {
			label = filepath.Base(f.asset.path)
		} else if f.value("path") != "" {
			label = f.value("path")
		}
		ui.Text(c.Key("file"), label).Bold().Wrap()
		ui.Row(c.Key("actions")).Gap(6).Children(func() {
			if ui.Button(c.Key("pick"), s.tr("author-chooseFile")).Disabled(f.assetBusy).Clicked() {
				s.pickManuscriptAsset(v)
			}
			if f.action == "figure" && ui.Button(c.Key("manual"), s.tr("author-editPath")).Disabled(f.assetBusy).Clicked() {
				f.showPath = !f.showPath
			}
			if f.action == "table" && len(f.asset.rows) != 0 && ui.Button(c.Key("manual"), s.tr("author-enterTable")).Clicked() {
				f.asset = manuscriptAuthorAsset{}
				f.setValue("csv", "")
				f.setValue("csv-name", "")
			}
		})
	})
}

func (s *Shell) manuscriptTableGrid(c *ui.Context, rows [][]string) {
	for i, row := range rows[:min(6, len(rows))] {
		el := ui.Row(c.Key(i)).FillWidth().Gap(1)
		el.Children(func() {
			for j, value := range row[:min(5, len(row))] {
				ui.Text(c.Key(j), value).SingleLine().FontSize(12).Padding(4, 6).MinWidth(0).
					Basis(0).Grow(1).Background(c.Theme().Surface)
			}
		})
	}
	if len(rows) > 6 || len(rows) > 0 && len(rows[0]) > 5 {
		ui.Text(c.Key("dimensions"), s.tr("author-tableDimensions", "rows", strconv.Itoa(len(rows)), "cols", strconv.Itoa(len(rows[0])))).
			FontSize(11).TextColor(s.ch.muted)
	}
}

func (s *Shell) manuscriptWidthChoices(c *ui.Context, f *manuscriptAuthorForm) {
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		label := s.tr("author-field-width")
		ui.Text(c.Key("label"), label).FontSize(12).Bold()
		ui.Row(c.Key("presets")).Gap(5).Children(func() {
			for _, pct := range []string{"25", "50", "75", "100"} {
				btn := ui.Button(c.Key(pct), pct+"%").Disabled(f.assetBusy)
				if f.value("width") == pct || f.value("width") == "" && pct == "75" {
					btn.Border(1, s.ch.accent)
				}
				if btn.Clicked() {
					f.setValue("width", pct)
				}
			}
		})
		ui.TextInput(c.Key("exact"), f.input("width")).Label(label).FillWidth().MaxLength(12).Placeholder("75")
	})
}

func (s *Shell) manuscriptPathField(c *ui.Context, v *manuscriptView) {
	f := &v.author
	old := f.value("path")
	s.authorField(c, f, authorField{key: "path", hint: "figures/example.png"})
	if path := f.value("path"); old != path {
		f.asset = s.manuscriptFigureAsset(v, path)
	}
}

func (s *Shell) manuscriptFigureAsset(v *manuscriptView, path string) manuscriptAuthorAsset {
	if path == "" {
		return manuscriptAuthorAsset{}
	}
	file, ok := s.manuscriptAsset(v, path)
	if !ok {
		return manuscriptAuthorAsset{}
	}
	asset := manuscriptAuthorAsset{path: file}
	if strings.EqualFold(filepath.Ext(file), ".pdf") {
		if info, err := os.Stat(file); err == nil {
			asset.revision = info.ModTime().UnixNano() ^ info.Size()
		}
	}
	return asset
}
