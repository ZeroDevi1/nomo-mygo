package shell

import (
	"slices"
	"strconv"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/egoist/mygo/ui"
)

const (
	scholarACPStandardWindow = 500_000
	scholarACPLargeWindow    = 872_000
)

func (s *Shell) scholarACPModelDetails(c *ui.Context, p *scholarState) {
	a := &p.agent.acp
	model := a.selectByCategory(acp.SessionConfigOptionCategoryModel)
	thought := a.selectByCategory(acp.SessionConfigOptionCategoryThoughtLevel)
	contextOption := a.contextWindowOption()
	modelID := a.selectedModel()
	fallback := contextOption == nil && scholarACPHasContextPresets(modelID)
	actual := a.progress.ContextWindow
	if model != nil && model.CurrentValue != a.optionValue(model) ||
		contextOption != nil && contextOption.CurrentValue != a.optionValue(contextOption) ||
		fallback && a.requestedContextWindow != s.scholarACPWindowOverride(a) {
		actual = 0
	}

	var labels []string
	if thought != nil {
		labels = append(labels, s.scholarACPThoughtLabel(a.optionValue(thought), a.optionLabel(thought)))
	}
	switch {
	case contextOption != nil:
		labels = append(labels, a.optionLabel(contextOption))
	case fallback:
		window := s.scholarACPWindowOverride(a)
		if window > 0 {
			labels = append(labels, scholarACPFormatTokens(uint64(window)))
		} else if actual > 0 {
			labels = append(labels, scholarACPFormatTokens(actual))
		} else {
			labels = append(labels, s.tr("scholarACPContextAuto"))
		}
	case actual > 0:
		labels = append(labels, scholarACPFormatTokens(actual))
	}
	summary := strings.Join(labels, " · ")
	modelLabel := s.tr("scholarACPModel")
	if model != nil {
		modelLabel = a.optionLabel(model)
	}
	label := s.tr("scholarACPModelDetails") + " · " + modelLabel
	if summary != "" {
		label += " · " + summary
	}
	enabled := !p.busy && registry.acp.ready() && (len(a.configOptions()) > 0 || actual > 0)
	button := ui.ButtonBase(c.Key("button")).MaxWidth(204).MinWidth(0).Height(26).Padding(0, 6).
		Radius(c.Theme().Radius).Gap(5).AlignItems(ui.Center).FocusRing(true).Label(label).Tooltip(label).Disabled(!enabled)
	switch {
	case enabled && (a.modelDetails || button.Pressed()):
		button.Background(s.ch.pressed)
	case enabled && button.Hovered():
		button.Background(s.ch.hover)
	}
	button.Children(func() {
		ui.Text(c.Key("model"), modelLabel).FontSize(12).SingleLine().MinWidth(0).TextColor(s.ch.text)
		if summary != "" {
			ui.Text(c.Key("summary"), summary).FontSize(11).SingleLine().MinWidth(0).MaxWidth(96).TextColor(s.ch.muted)
		}
		ui.Icon(c.Key("arrow"), s.icon("chevron-down")).FontSize(10).Shrink(0).TextColor(s.ch.muted)
	})
	if button.Clicked() {
		p.agent.attachments = false
		a.modelDetails = !a.modelDetails
		a.modelPicker = false
	}
	if !enabled {
		a.modelDetails = false
	}
	ui.PopoverBase(c.Key("popover"), button, &a.modelDetails, func(panel ui.Element) {
		panel.Width(264).Padding(8).Margin(6, 0, 0).Radius(c.Theme().Radius+4).
			Background(s.ch.content).Border(1, s.ch.separator).Shadow(0, 5, 14, 0, ui.RGBA(0, 0, 0, 0.15))
		if a.modelPicker && model != nil {
			s.scholarACPModelPicker(c.Key("picker"), a, model)
			return
		}
		ui.Scroll(c.Key("details")).FillWidth().MaxHeight(300).Children(func() {
			ui.Column(c.Key("content")).FillWidth().Gap(8).Children(func() {
				if model != nil && s.scholarACPModelRow(c.Key("choose-model"), modelLabel, "chevron-right",
					s.tr("scholarACPModel")+" · "+modelLabel, false).Clicked() {
					a.modelPicker = true
				}
				if thought != nil {
					s.scholarACPReasoning(c.Key("thought"), a, thought)
				}
				if contextOption != nil || fallback || actual > 0 {
					ui.Column(c.Key("context")).FillWidth().Gap(4).Children(func() {
						s.scholarACPDetailHeading(c.Key("context-label"), s.tr("scholarACPContextSize"), "")
						switch {
						case contextOption != nil:
							s.scholarACPContextSelect(c.Key("context-option"), a, contextOption)
						case fallback:
							s.scholarACPContextPresets(c.Key("context-presets"), a, modelID)
						}
						if actual > 0 {
							ui.Text(c.Key("effective"), s.tr("scholarACPContextEffective")+" "+scholarACPFormatTokens(actual)).
								FontSize(11).TextColor(s.ch.muted)
						}
					})
				}
				for _, config := range a.configOptions() {
					switch {
					case config.Select != nil && config.Select != model && config.Select != thought &&
						config.Select != contextOption && config.Select.ConfigId != "mode":
						option := config.Select
						ck := c.Key(string(option.ConfigId))
						ui.Row(ck).FillWidth().AlignItems(ui.Center).Gap(4).Children(func() {
							ui.Text(ck.Key("label"), s.scholarACPConfigLabel(option.ConfigId, option.Name)).
								FontSize(12).SingleLine().MinWidth(0).Grow(1).TextColor(s.ch.muted)
							s.scholarACPDetailMenu(ck.Key("menu"), a, option)
						})
					case config.Boolean != nil:
						option := config.Boolean
						current := option.CurrentValue
						for _, choice := range a.settings {
							if choice.Boolean != nil && choice.Boolean.ConfigId == option.ConfigId {
								current = choice.Boolean.Value
							}
						}
						name := s.scholarACPConfigLabel(option.ConfigId, option.Name)
						if ui.Checkbox(c.Key(string(option.ConfigId)), &current, name).FontSize(12).Changed() {
							a.setOption(acp.SetSessionConfigOptionRequest{Boolean: &acp.SetSessionConfigOptionBoolean{
								Type: "boolean", ConfigId: option.ConfigId, Value: current,
							}})
						}
					}
				}
			})
		})
	})
}

func (s *Shell) scholarACPModelPicker(c *ui.Context, a *scholarACPState, option *acp.SessionConfigOptionSelect) {
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		title := s.tr("scholarACPModel")
		back := s.tr("scholarACPModelDetails")
		ui.Row(c.Key("header")).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
			if s.scholarCompactIcon(c.Key("back"), "arrow-left", back, false).Label(back).FocusRing(true).Clicked() {
				a.modelPicker = false
			}
			ui.Text(c.Key("title"), title).FontSize(12).Bold().TextColor(s.ch.muted)
		})
		ui.Scroll(c.Key("list")).FillWidth().MaxHeight(248).Children(func() {
			ui.Column(c.Key("items")).FillWidth().Gap(2).Children(func() {
				current := a.optionValue(option)
				choose := func(c *ui.Context, value acp.SessionConfigSelectOption) {
					selected := value.Value == current
					icon := ""
					if selected {
						icon = "check"
					}
					if s.scholarACPModelRow(c.Key(string(value.Value)), value.Name, icon, value.Name, selected).Clicked() {
						if !selected {
							a.setSelectOption(option, value.Value)
						}
						a.modelPicker = false
					}
				}
				if values := option.Options.Ungrouped; values != nil {
					for _, value := range *values {
						choose(c.Key("ungrouped"), value)
					}
				}
				if groups := option.Options.Grouped; groups != nil {
					for i, group := range *groups {
						ck := c.Key("group").Key(i)
						ui.Text(ck.Key("title"), group.Name).Padding(4, 8, 1).FontSize(11).TextColor(s.ch.muted)
						for _, value := range group.Options {
							choose(ck, value)
						}
					}
				}
			})
		})
	})
}

func (s *Shell) scholarACPModelRow(c *ui.Context, name, glyph, label string, selected bool) ui.Element {
	button := ui.ButtonBase(c).FillWidth().MinWidth(0).Height(27).Padding(0, 8).Gap(4).
		AlignItems(ui.Center).Radius(c.Theme().Radius).FocusRing(true).Label(label).Tooltip(name).Selected(selected)
	switch {
	case selected:
		button.Background(s.ch.accent.Alpha(0.12))
	case button.Hovered():
		button.Background(s.ch.selected)
	case glyph == "chevron-right":
		button.Background(s.ch.hover)
	}
	button.Children(func() {
		ui.Text(c.Key("name"), name).FontSize(12).SingleLine().Grow(1).MinWidth(0).TextColor(s.ch.text)
		if glyph != "" {
			color := s.ch.muted
			if selected {
				color = s.ch.accent
			}
			ui.Icon(c.Key("glyph"), s.icon(glyph)).FontSize(13).Shrink(0).TextColor(color)
		}
	})
	return button
}

func (s *Shell) scholarACPDetailHeading(c *ui.Context, label, value string) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Text(c.Key("label"), label).FontSize(12).TextColor(s.ch.muted)
		if value != "" {
			ui.Spacer(c)
			ui.Text(c.Key("value"), value).FontSize(12).Bold().TextColor(s.ch.text)
		}
	})
}

func (s *Shell) scholarACPReasoning(c *ui.Context, a *scholarACPState, option *acp.SessionConfigOptionSelect) {
	label := s.tr("scholarACPThoughtLevel")
	current := a.optionValue(option)
	name := s.scholarACPThoughtLabel(current, a.optionLabel(option))
	ui.Column(c).FillWidth().Gap(2).Children(func() {
		values := option.Options.Ungrouped
		if values == nil || len(*values) < 2 || len(*values) > 10 {
			s.scholarACPDetailHeading(c.Key("header"), label, "")
			s.scholarACPDetailMenu(c.Key("menu"), a, option)
			return
		}
		index := slices.IndexFunc(*values, func(value acp.SessionConfigSelectOption) bool { return value.Value == current })
		if index < 0 {
			s.scholarACPDetailHeading(c.Key("header"), label, "")
			s.scholarACPDetailMenu(c.Key("menu"), a, option)
			return
		}
		s.scholarACPDetailHeading(c.Key("header"), label, name)
		position := float64(index)
		slider := ui.StepSlider(c.Key("slider"), &position, 0, float64(len(*values)-1), 1).
			FillWidth().FocusRing(true).Label(label)
		next := int(position)
		if slider.Changed() && next >= 0 && next < len(*values) && next != index {
			value := (*values)[next]
			a.setSelectOption(option, value.Value)
			name = s.scholarACPThoughtLabel(value.Value, value.Name)
		}
		slider.Value(name).Tooltip(name)
	})
}

func (s *Shell) scholarACPDetailMenu(c *ui.Context, a *scholarACPState, option *acp.SessionConfigOptionSelect) {
	label := s.scholarACPValueLabel(option, a.optionValue(option), a.optionLabel(option))
	s.scholarACPMenuControl(c, label, s.scholarACPConfigLabel(option.ConfigId, option.Name)+" · "+label).
		Menu(func(m *ui.Menu) { s.scholarACPSelectMenu(m, a, option) })
}

func (s *Shell) scholarACPContextSelect(c *ui.Context, a *scholarACPState, option *acp.SessionConfigOptionSelect) {
	values := option.Options.Ungrouped
	if values == nil || len(*values) == 0 || len(*values) > 4 {
		s.scholarACPDetailMenu(c, a, option)
		return
	}
	labels := make([]string, len(*values))
	current := a.optionValue(option)
	selected := -1
	for i, value := range *values {
		labels[i] = value.Name
		if value.Value == current {
			selected = i
		}
	}
	if next := s.scholarACPContextChips(c, labels, selected); next >= 0 && next != selected {
		a.setSelectOption(option, (*values)[next].Value)
	}
}

func (s *Shell) scholarACPContextChips(c *ui.Context, labels []string, selected int) (next int) {
	next = -1
	ui.Row(c).FillWidth().Gap(4).Children(func() {
		for i, label := range labels {
			ck := c.Key(i)
			active := i == selected
			button := ui.ButtonBase(ck).Grow(1).Basis(0).MinWidth(0).Height(23).Padding(0, 5).Center().
				Radius(12).FocusRing(true).Label(label).Tooltip(label).Selected(active)
			if active {
				button.Background(s.ch.accent.Alpha(0.12))
			} else if button.Hovered() {
				button.Background(s.ch.selected)
			} else {
				button.Background(s.ch.hover)
			}
			button.Children(func() {
				text := ui.Text(ck, label).FontSize(11).SingleLine().MinWidth(0).TextColor(s.ch.text)
				if active {
					text.Bold()
				}
			})
			if button.Clicked() {
				next = i
			}
		}
	})
	return next
}

func (s *Shell) scholarACPConfigLabel(id acp.SessionConfigId, fallback string) string {
	switch id {
	case "collaboration_mode":
		return s.tr("scholarACPCollaboration")
	case "fast-mode":
		return s.tr("scholarACPFast")
	default:
		return fallback
	}
}

func (s *Shell) scholarACPValueLabel(option *acp.SessionConfigOptionSelect, value acp.SessionConfigValueId, fallback string) string {
	if option.Category != nil && *option.Category == acp.SessionConfigOptionCategoryThoughtLevel {
		return s.scholarACPThoughtLabel(value, fallback)
	}
	switch option.ConfigId {
	case "collaboration_mode":
		switch value {
		case "default":
			return s.tr("scholarACPDefaultMode")
		case "plan":
			return s.tr("scholarACPPlanMode")
		}
	}
	return fallback
}

func (s *Shell) scholarACPThoughtLabel(value acp.SessionConfigValueId, fallback string) string {
	switch value {
	case "low":
		return s.tr("scholarACPThoughtLow")
	case "medium":
		return s.tr("scholarACPThoughtMedium")
	case "high":
		return s.tr("scholarACPThoughtHigh")
	case "xhigh":
		return s.tr("scholarACPThoughtXHigh")
	case "max":
		return s.tr("scholarACPThoughtMax")
	default:
		return fallback
	}
}

func (a *scholarACPState) selectedModel() string {
	if model := a.selectByCategory(acp.SessionConfigOptionCategoryModel); model != nil {
		return string(a.optionValue(model))
	}
	return ""
}

func (a *scholarACPState) contextWindowOption() *acp.SessionConfigOptionSelect {
	for _, config := range a.configOptions() {
		if option := config.Select; option != nil {
			switch strings.ReplaceAll(string(option.ConfigId), "-", "_") {
			case "context_size", "context_window", "model_context_window":
				return option
			}
		}
	}
	return nil
}

// These are the two window presets shown for 6.1 Sol in VS Code. The
// bundled Codex catalog gives Sol and Terra the same maximum of 872000.
func scholarACPHasContextPresets(model string) bool {
	return model == "gpt-6.1-sol" || model == "gpt-6.1-terra"
}

func (s *Shell) scholarACPWindowOverride(a *scholarACPState) int {
	model := a.selectedModel()
	if !scholarACPHasContextPresets(model) || a.contextWindowOption() != nil {
		return 0
	}
	value := s.prefs.ScholarACPContextWindows[model]
	if value == scholarACPStandardWindow || value == scholarACPLargeWindow {
		return value
	}
	return 0
}

func (s *Shell) scholarACPContextPresets(c *ui.Context, a *scholarACPState, model string) {
	values := [...]int{0, scholarACPStandardWindow, scholarACPLargeWindow}
	labels := [...]string{s.tr("scholarACPContextAuto"), "500K", "872K"}
	selected := slices.Index(values[:], s.scholarACPWindowOverride(a))
	i := s.scholarACPContextChips(c, labels[:], selected)
	if i < 0 || i == selected {
		return
	}
	if i == 0 {
		delete(s.prefs.ScholarACPContextWindows, model)
	} else {
		if s.prefs.ScholarACPContextWindows == nil {
			s.prefs.ScholarACPContextWindows = make(map[string]int)
		}
		s.prefs.ScholarACPContextWindows[model] = values[i]
	}
	s.setPref("scholarACPContextWindows")
}

func scholarACPFormatTokens(tokens uint64) string {
	if tokens < 1_000 {
		return strconv.FormatUint(tokens, 10)
	}
	return strconv.FormatFloat(float64(tokens)/1000, 'f', -1, 64) + "K"
}
