package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

func (s *Shell) manuscriptReferencePicker(c *ui.Context, f *manuscriptAuthorForm) {
	ui.TextInput(c.Key("search"), &f.refSearch).Label(s.tr("author-searchReference")).Placeholder(s.tr("author-searchReference")).FillWidth()
	types := [...]string{"", "section", "equation", "figure", "table"}
	filter := s.tr("author-referenceKind-" + types[f.refKind])
	labels := make([]string, len(types))
	for i, kind := range types {
		labels[i] = s.tr("author-referenceKind-" + kind)
	}
	if ui.Select(c.Key("kind"), &filter, labels).FillWidth().Changed() {
		for i, label := range labels {
			if label == filter {
				f.refKind = i
				break
			}
		}
	}
	q := strings.ToLower(strings.TrimSpace(f.refSearch))
	found := 0
	for _, item := range f.references {
		if kind := types[f.refKind]; kind != "" && item.Kind != kind {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(item.Label+" "+item.Title+" "+item.Number), q) {
			continue
		}
		found++
		if found > 24 {
			ui.Text(c.Key("more"), s.tr("author-referenceMore")).FontSize(12).TextColor(s.ch.muted)
			break
		}
		kind := ""
		if item.Kind != "" {
			kind = s.tr("author-referenceKind-" + item.Kind)
		}
		desc := strings.TrimSpace(kind + " " + item.Number + " " + item.Title)
		if desc != "" {
			desc += " · "
		}
		if ui.Button(c.Key(item.Label), desc+item.Label).FillWidth().Tooltip(item.Label).Clicked() {
			f.setValue("name", item.Label)
		}
	}
	if found == 0 {
		ui.Text(c.Key("no-matches"), s.tr("author-referenceEmpty")).FontSize(12).TextColor(s.ch.muted)
	}
}
