package shell

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

type scholarACPFormDraft struct {
	form   *scholar.ACPForm
	fields []scholarACPFormAnswer
	err    string
	sent   bool
}

type scholarACPFormAnswer struct {
	enabled, checked bool
	text             string
	choice           int
	selected         []bool
}

func newScholarACPFormDraft(form *scholar.ACPForm) scholarACPFormDraft {
	draft := scholarACPFormDraft{form: form, fields: make([]scholarACPFormAnswer, len(form.Fields))}
	for i, field := range form.Fields {
		answer := &draft.fields[i]
		answer.enabled = field.Required || field.Default != nil
		switch value := field.Default.(type) {
		case string:
			answer.text = value
			answer.choice = slices.IndexFunc(field.Choices, func(choice scholar.ACPFormChoice) bool {
				return choice.Value == value
			}) + 1
		case int64:
			answer.text = strconv.FormatInt(value, 10)
		case float64:
			answer.text = strconv.FormatFloat(value, 'g', -1, 64)
		case bool:
			answer.checked = value
		}
		if field.Kind == "array" {
			defaults, _ := field.Default.([]string)
			answer.selected = make([]bool, len(field.Choices))
			for k, choice := range field.Choices {
				answer.selected[k] = slices.Contains(defaults, choice.Value)
			}
		}
	}
	return draft
}

func (s *Shell) scholarACPFormChanged(p *scholarState, job uint64) func(*scholar.ACPForm, bool) {
	return func(form *scholar.ACPForm, pending bool) {
		s.post(func() {
			if s.scholar != p || p.job != job {
				return
			}
			state := &p.agent.acp
			if pending {
				state.forms = append(state.forms, newScholarACPFormDraft(form))
				state.historyOpen = false
				s.showScholarInspector(p, scholarCodex)
			} else {
				state.forms = slices.DeleteFunc(state.forms, func(draft scholarACPFormDraft) bool { return draft.form == form })
			}
		})
	}
}

func (s *Shell) scholarACPFormView(c *ui.Context, draft *scholarACPFormDraft) {
	form := draft.form
	c = c.Key(form)
	ui.Column(c).FillWidth().Gap(6).Padding(8).Radius(c.Theme().Radius).
		Border(1, s.ch.separator).Children(func() {
		label := "Codex · " + s.tr("scholarACPQuestion")
		if form.Title != "" {
			label = "Codex · " + form.Title
		}
		ui.Text(c.Key("title"), label).FontSize(12).Bold().Wrap()
		ui.Scroll(c.Key("fields")).FillWidth().MaxHeight(240).Children(func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				ui.Text(c.Key("message"), form.Message).FontSize(12).Wrap()
				if form.Description != "" {
					ui.Text(c.Key("description"), form.Description).FontSize(11).Wrap().TextColor(s.ch.muted)
				}
				for i, field := range form.Fields {
					s.scholarACPFormField(c.Key(i), field, &draft.fields[i], draft.sent)
				}
			})
		})
		if draft.err != "" {
			ui.Text(c.Key("error"), draft.err).FontSize(11).Wrap().TextColor(scholarInk(c, "warning"))
		}
		ui.Row(c.Key("actions")).Gap(4).Children(func() {
			if ui.Button(c.Key("answer"), s.tr("scholarACPAnswer")).Disabled(draft.sent).Clicked() {
				values := make(map[string]any)
				for i, field := range form.Fields {
					value := draft.fields[i].value(field)
					if !field.Valid(value) || draft.fields[i].enabled && value == nil {
						draft.err = fmt.Sprintf(s.tr("scholarACPInvalidAnswer"), field.Label)
						return
					}
					if value != nil {
						values[field.Name] = value
					}
				}
				draft.err = ""
				draft.sent = form.Respond("accept", values)
			}
			if ui.Button(c.Key("decline"), s.tr("scholarACPDecline")).Disabled(draft.sent).Clicked() {
				draft.sent = form.Respond("decline", nil)
			}
			if ui.Button(c.Key("cancel"), s.tr("cancel")).Disabled(draft.sent).Clicked() {
				draft.sent = form.Respond("cancel", nil)
			}
		})
	})
}

func (s *Shell) scholarACPFormField(c *ui.Context, field scholar.ACPFormField, answer *scholarACPFormAnswer, sent bool) {
	ui.Column(c).FillWidth().Gap(3).Children(func() {
		if field.Required {
			ui.Text(c.Key("label"), field.Label+" *").FontSize(12).Wrap()
		} else {
			ui.Checkbox(c.Key("enabled"), &answer.enabled, field.Label).FontSize(12).Disabled(sent)
		}
		if field.Description != "" {
			ui.Text(c.Key("detail"), field.Description).FontSize(11).Wrap().TextColor(s.ch.muted)
		}
		if !answer.enabled {
			return
		}
		switch {
		case field.Kind == "boolean":
			ui.Checkbox(c.Key("bool"), &answer.checked, s.tr("scholarACPTrue")).FontSize(12).Disabled(sent)
		case field.Kind == "array":
			for i, choice := range field.Choices {
				ui.Checkbox(c.Key(i), &answer.selected[i], choice.Label).FontSize(12).Disabled(sent)
			}
		case len(field.Choices) > 0:
			labels := make([]string, 1, len(field.Choices)+1)
			labels[0] = s.tr("scholarACPChoose")
			for _, choice := range field.Choices {
				labels = append(labels, choice.Label)
			}
			scholarSelectIndex(c.Key("choice"), &answer.choice, labels, sent)
		default:
			ui.TextInput(c.Key("text"), &answer.text).FillWidth().Disabled(sent)
		}
	})
}

func (a scholarACPFormAnswer) value(field scholar.ACPFormField) any {
	if !a.enabled {
		return nil
	}
	switch field.Kind {
	case "boolean":
		return a.checked
	case "integer":
		if n, err := strconv.ParseInt(strings.TrimSpace(a.text), 10, 64); err == nil {
			return n
		}
	case "number":
		if n, err := strconv.ParseFloat(strings.TrimSpace(a.text), 64); err == nil {
			return n
		}
	case "array":
		values := []string{}
		for i, selected := range a.selected {
			if selected {
				values = append(values, field.Choices[i].Value)
			}
		}
		return values
	case "string":
		if len(field.Choices) == 0 {
			return a.text
		}
		if a.choice > 0 && a.choice <= len(field.Choices) {
			return field.Choices[a.choice-1].Value
		}
	}
	return nil
}
