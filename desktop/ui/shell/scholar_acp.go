package shell

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	acp "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

const scholarACPThreadLimit = 32

type scholarACPThread struct {
	ID        acp.SessionId `json:"id"`
	Workspace string        `json:"workspace"`
	Scope     string        `json:"scope"`
	Label     string        `json:"label"`
	Title     string        `json:"title,omitempty"`
}

type scholarACPQuote struct {
	scope, label, locator, text string
	png                         []byte
}

type scholarACPState struct {
	thread                    scholarACPThread
	progress                  scholar.ACPProgress
	history                   []scholar.ACPMessage
	quotes                    []scholarACPQuote
	settings                  []acp.SetSessionConfigOptionRequest
	historyOpen, activityOpen bool
	focusComposer             bool
	modelDetails, modelPicker bool
	requestedContextWindow    int
	cancelActivities          chan scholar.ACPActivity
	scrollToEnd               bool
	forms                     []scholarACPFormDraft
	previewQuote              int
	previewImage              *ui.Bitmap
}

func (s *Shell) scholarAgentView(c *ui.Context, p *scholarState, singlePane bool) {
	a := &p.agent
	state := &a.acp
	sessions := state.historyOpen || state.thread.ID == "" && !a.running
	ui.Column(c).Fill().Grow(1).MinHeight(0).Children(func() {
		s.scholarACPHeader(c.Key("header"), p, singlePane, sessions)
		body := ui.Scroll(c.Key("chat-body")).Grow(1).FillWidth().MinHeight(0)
		body.Children(func() {
			ui.Column(c).FillWidth().Padding(2, 10, 8).Gap(6).Children(func() {
				switch {
				case a.batch != nil:
					s.scholarACPError(c.Key("error"), p)
					s.scholarPatchSetView(c, p, a.batch)
				case a.patch != nil:
					s.scholarACPError(c.Key("error"), p)
					s.scholarPatchView(c, p, a.patch)
				case sessions:
					s.scholarACPSessions(c.Key("sessions"), p)
				default:
					if state.thread.Title != "" {
						ui.Text(c.Key("title"), state.thread.Title).FontSize(12).Bold().SingleLine().MinWidth(0).
							Tooltip(state.thread.Title)
					}
					s.scholarACPTranscript(c, p, body.Bounds())
				}
			})
		})
		if a.pendingPatch() == nil {
			ui.Column(c.Key("bottom")).FillWidth().Padding(0, 10, 6).Gap(5).Children(func() {
				s.scholarACPConnectionNotice(c.Key("connection"), p)
				s.scholarACPError(c.Key("error"), p)
				if len(state.forms) > 0 {
					s.scholarACPFormView(c.Key("question"), &state.forms[0])
				} else {
					s.scholarACPComposer(c.Key("composer"), p)
				}
			})
		}
	})
}

func (s *Shell) scholarACPHeader(c *ui.Context, p *scholarState, singlePane, sessions bool) {
	a := &p.agent
	state := &a.acp
	ui.Row(c).Height(s.pf.scholarNavHeight()+3).FillWidth().Padding(0, 8).Gap(3).AlignItems(ui.Center).Children(func() {
		ui.Text(c.Key("chat"), s.tr("scholarACPChat")).Padding(4, 8).FontSize(12).Bold().
			Radius(5).Background(s.ch.hover)
		ui.Box(c).Grow(1)
		blocked := p.busy || a.pendingPatch() != nil
		if len(state.forms) > 0 && p.cancelJob != nil &&
			s.toolButton(c.Key("stop-form"), "x", s.tr("scholarAgentCancel"), false).Clicked() {
			p.cancelJob()
		}
		if s.toolButton(c.Key("new"), "plus", s.tr("scholarACPNew"), false).Disabled(blocked).Clicked() {
			s.selectScholarACPThread(p, s.scholarDefaultACPThread(p))
		}
		if s.toolButton(c.Key("history"), "history", s.tr("scholarACPHistory"), sessions).
			Disabled(a.pendingPatch() != nil || state.thread.ID == "" && !a.running).Clicked() {
			state.historyOpen = !state.historyOpen
		}
		s.toolButton(c.Key("more"), "ellipsis", s.tr("scholarACPOptions"), false).Menu(func(m *ui.Menu) {
			if m.Item(s.tr("scholarACPRefresh")).Disabled(blocked || state.thread.ID == "").Chosen() {
				s.restoreScholarACPThread(p, state.thread)
			}
			if len(a.applied) > 0 && m.Item(s.tr("scholarRollbackFiles")).Disabled(blocked).Chosen() {
				s.rollbackScholarFiles(p)
			}
			if m.Item(s.tr("scholarACPRetry")).Disabled(blocked || registry.acp.cancel != nil).Chosen() {
				startScholarACP()
			}
		})
		label := s.tr("scholarCloseDrawer")
		icon := "x"
		if singlePane {
			label, icon = s.tr("scholarReturnManuscript"), "chevron-left"
		}
		if s.toolButton(c.Key("close"), icon, label, false).Label(label).FocusRing(true).Clicked() {
			s.closeScholarInspector(p)
		}
	})
}

func (s *Shell) scholarACPError(c *ui.Context, p *scholarState) {
	if p.err != "" {
		ui.Text(c, p.err).FontSize(11).Wrap().TextColor(scholarInk(c, "warning"))
	}
}

func (s *Shell) scholarACPSessions(c *ui.Context, p *scholarState) {
	state := &p.agent.acp
	ui.Text(c.Key("heading"), s.tr("scholarACPHistory")).FontSize(12).Bold().Padding(0, 3, 2)
	threads := s.prefs.ScholarProjects[p.project.Root].ACPThreads
	if len(threads) == 0 {
		ui.Text(c.Key("empty"), s.tr("scholarACPEmptyHistory")).FontSize(11).TextColor(s.ch.muted).Padding(4, 3)
	}
	for _, thread := range threads {
		ck := c.Key(string(thread.ID))
		title := thread.Title
		if title == "" {
			title = thread.Label
		}
		row := ui.ButtonBase(ck).FillWidth().MinWidth(0).Padding(5, 7).Gap(8).Radius(5).
			AlignItems(ui.Start).FocusRing(true).Label(title).Tooltip(thread.Label).Disabled(p.busy)
		if thread.ID == state.thread.ID || row.Hovered() {
			row.Background(s.ch.hover)
		}
		row.Children(func() {
			ui.Box(ck.Key("dot")).Size(4, 4).Margin(6, 0, 0).Radius(2).Background(s.ch.muted)
			ui.Column(ck.Key("labels")).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(ck.Key("title"), title).FontSize(12).SingleLine()
				if thread.Label != "" && thread.Label != title {
					ui.Text(ck.Key("source"), thread.Label).FontSize(11).SingleLine().TextColor(s.ch.muted)
				}
			})
		})
		if row.Clicked() {
			s.selectScholarACPThread(p, thread)
			s.restoreScholarACPThread(p, thread)
		}
	}
}

func (s *Shell) scholarACPTranscript(c *ui.Context, p *scholarState, viewport ui.Rect) {
	state := &p.agent.acp
	for i, m := range state.history {
		s.scholarACPMessage(c.Key("history-message").Key(i), m)
	}
	if p.agent.running {
		s.scholarACPMessage(c.Key("pending-message"), scholar.ACPMessage{Role: "user", Text: p.agent.instruction})
	}
	for i, m := range state.progress.Messages {
		if m.Role != "user" {
			s.scholarACPMessage(c.Key("message").Key(i), m)
		}
	}
	end := ui.Box(c.Key("chat-end")).Height(1).FillWidth()
	y := end.Bounds().Y
	follow := p.agent.running && viewport.H > 0 && y >= viewport.Y && y <= viewport.Y+viewport.H+24
	if state.scrollToEnd || follow {
		end.ScrollIntoView(ui.ScrollIntoViewOptions{Block: ui.ScrollEnd})
		state.scrollToEnd = false
	}
	if len(state.progress.Activity) == 0 {
		return
	}
	if ui.Button(c.Key("activity"), s.tr("scholarACPActivity")).Clicked() {
		state.activityOpen = !state.activityOpen
	}
	if state.activityOpen {
		for _, activity := range state.progress.Activity {
			ui.Column(c.Key(activity.ID)).FillWidth().Gap(3).Children(func() {
				ui.Text(c, strings.TrimSpace(activity.Title+" "+activity.Status)).FontSize(11).Wrap()
				if p.agent.running && activity.Cancellable && state.cancelActivities != nil &&
					ui.Button(c.Key(activity.ID).Key("stop"), s.tr("scholarAgentCancel")).Clicked() {
					select {
					case state.cancelActivities <- activity:
					default:
					}
				}
				if activity.Text != "" {
					ui.Text(c, activity.Text).FontSize(11).Wrap().TextColor(s.ch.muted)
				}
			})
		}
	}
}

func (s *Shell) scholarACPMessage(c *ui.Context, m scholar.ACPMessage) {
	if m.Text == "" || m.Role == "thought" {
		return
	}
	if m.Role == "user" {
		ui.Row(c).FillWidth().Justify(ui.End).Padding(4, 0, 2, 16).Children(func() {
			ui.Column(c.Key("bubble")).MinWidth(0).Padding(7, 9).Radius(c.Theme().Radius+3).
				Background(s.ch.content).Border(1, s.ch.separator).Children(func() {
				ui.Text(c.Key("text"), m.Text).FontSize(13).Wrap().MinWidth(0)
			})
		})
		return
	}
	ui.Column(c).FillWidth().Gap(4).Padding(5, 2).Children(func() {
		ui.Text(c.Key("author"), "Codex").Bold().FontSize(11).TextColor(s.ch.muted)
		ui.Text(c.Key("text"), m.Text).Wrap().FontSize(13)
	})
}

func (s *Shell) scholarACPComposer(c *ui.Context, p *scholarState) {
	a := &p.agent
	state := &a.acp
	connection := &registry.acp
	command := strings.TrimSpace(a.instruction)
	if strings.HasPrefix(command, "/") && !p.busy {
		s.scholarACPCommands(c.Key("completion"), a, command)
	}
	card := ui.Column(c.Key("card")).FillWidth().MinWidth(0).Padding(4, 6).Gap(2).
		Radius(c.Theme().Radius+4).Background(s.ch.content).Border(1, s.ch.separator)
	if card.FocusWithin() {
		card.BorderColor(s.ch.accent)
	}
	card.Children(func() {
		if len(state.quotes) > 0 || a.multi && len(a.files) > 0 || slices.Contains(a.include[:], true) {
			ui.Scroll(c.Key("references")).FillWidth().MaxHeight(112).Children(func() {
				ui.Column(c).FillWidth().Padding(2).Gap(2).Children(func() {
					s.scholarACPComposerReferences(c, p)
				})
			})
		}
		input := ui.TextAreaBase(c.Key("text"), &a.instruction).FillWidth().Height(52).Padding(7, 5, 0).
			FontSize(13).Placeholder(s.tr("scholarAgentInput")).Disabled(a.running)
		if state.focusComposer && !a.running {
			input.Focus()
			state.focusComposer = false
		}
		ui.Row(c.Key("tools")).FillWidth().MinWidth(0).Gap(4).AlignItems(ui.Center).Children(func() {
			s.scholarAgentAttachmentButton(c.Key("attach"), p, input.Shortcut(ui.Cmd, ui.KeySlash))
			s.scholarACPModelDetails(c.Key("model-details"), p)
			ui.Box(c).Grow(1)
			ready := !p.busy && command != "" && !connection.needsLogin && connection.err == nil
			if a.running {
				if s.scholarACPSendButton(c.Key("stop"), s.tr("scholarAgentCancel"), "■", p.cancelJob != nil).Clicked() && p.cancelJob != nil {
					p.cancelJob()
				}
			} else {
				send := s.scholarACPSendButton(c.Key("send"), s.tr("scholarAgentSend"), "↑", ready).Clicked()
				if ready && (send || input.Shortcut(ui.Cmd, ui.KeyEnter)) {
					s.sendScholarACP(p)
				}
			}
		})
	})
	s.scholarACPFooter(c.Key("footer"), p)
}

func (s *Shell) scholarACPComposerReferences(c *ui.Context, p *scholarState) {
	state := &p.agent.acp
	s.scholarAgentAttachments(c.Key("attachments"), p)
	remove := -1
	for i, ref := range state.quotes {
		ck := c.Key("quote").Key(i)
		ui.Row(ck).FillWidth().Gap(4).Padding(0, 3).Radius(c.Theme().Radius + 2).
			Background(s.ch.hover).AlignItems(ui.Center).Children(func() {
			s.scholarACPQuotePreview(ck.Key("preview"), p, i, ref)
			if s.scholarCompactIcon(ck.Key("remove"), "x", s.tr("scholarRemoveAttachment"), false).Disabled(p.busy).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		state.quotes = slices.Delete(state.quotes, remove, remove+1)
		state.previewQuote, state.previewImage = 0, nil
	}
}

func (s *Shell) scholarACPSendButton(c *ui.Context, label, glyph string, enabled bool) ui.Element {
	button := ui.ButtonBase(c).Size(25, 25).Shrink(0).Center().Radius(13).
		FocusRing(true).Label(label).Tooltip(label).Disabled(!enabled)
	color := s.ch.muted
	if enabled {
		color = c.Theme().AccentText
		background := s.ch.accent
		if button.Hovered() {
			background = c.Theme().AccentHover
		}
		button.Background(background)
	}
	button.Children(func() { ui.Text(c, glyph).FontSize(15).Bold().TextColor(color) })
	return button
}

func (s *Shell) scholarACPFooter(c *ui.Context, p *scholarState) {
	a := &p.agent
	state := &a.acp
	connection := &registry.acp
	ui.Row(c).FillWidth().MinWidth(0).Padding(1, 2).Gap(7).AlignItems(ui.Center).Children(func() {
		if len(connection.info.AuthMethods) > 0 {
			s.scholarACPMenuControl(c.Key("account"), "Codex", s.tr("scholarACPAccount")).
				Disabled(p.busy || connection.cancel != nil).Menu(func(m *ui.Menu) { s.scholarACPAccountMenu(m, p) })
		} else {
			ui.Text(c.Key("provider"), "Codex").FontSize(11).TextColor(s.ch.muted)
		}
		label := s.tr("scholarACPReadMode")
		editing := a.multi && len(a.files) > 0 || !a.multi && a.include[0] && a.target.tabID != 0
		if editing {
			label = s.tr("scholarACPEditMode")
		}
		switch {
		case a.running:
			label = s.tr("scholarACPResponding")
			if editing {
				label = s.tr("scholarGenerating")
			}
			if state.progress.State == "requires_action" {
				label = s.tr("scholarACPRequiresAction")
			} else if state.progress.Background > 0 && state.progress.State == "idle" {
				label = s.tr("scholarACPBackground")
			}
		case connection.cancel != nil:
			label = s.tr("scholarACPConnecting")
		}
		ui.Text(c.Key("mode"), label).FontSize(11).SingleLine().MinWidth(0).TextColor(s.ch.muted).Tooltip(label)
		ui.Box(c).Grow(1)
		if state.progress.Usage != "" {
			ui.Text(c.Key("usage"), state.progress.Usage).FontSize(11).SingleLine().MinWidth(0).
				TextColor(s.ch.muted).Tooltip(state.progress.Usage)
		}
	})
}

func (s *Shell) scholarACPQuotePreview(c *ui.Context, p *scholarState, i int, ref scholarACPQuote) {
	state := &p.agent.acp
	title := ref.label + " · " + ref.locator
	if len(ref.png) > 0 {
		title = s.tr("scholarACPImage") + " · " + title
	}
	icon := "quote"
	if len(ref.png) > 0 {
		icon = "image"
	}
	button := s.scholarACPAction(c, icon, title, false).Grow(1)
	if button.Clicked() {
		if state.previewQuote == i+1 {
			state.previewQuote, state.previewImage = 0, nil
		} else {
			state.previewQuote, state.previewImage = i+1, nil
			if len(ref.png) > 0 {
				bitmap, err := ui.DecodeBitmap(ref.png)
				if err != nil {
					p.err = err.Error()
					state.previewQuote = 0
				} else {
					state.previewImage = bitmap
				}
			}
		}
	}
	open := state.previewQuote == i+1
	ui.Popover(c, button, &open, func() {
		ui.Scroll(c).Width(280).MaxHeight(250).Children(func() {
			if state.previewImage != nil {
				w, h := state.previewImage.Size()
				ui.Image(c, state.previewImage).FillWidth().AspectRatio(float32(w) / float32(h)).Label(title)
			} else {
				ui.Text(c, ref.text).Wrap().Padding(8).FontSize(12)
			}
		})
	})
	if !open && state.previewQuote == i+1 {
		state.previewQuote, state.previewImage = 0, nil
	}
}

func (s *Shell) scholarACPCommands(c *ui.Context, a *scholarAgentState, query string) {
	if strings.ContainsAny(query, " \t\n") {
		return
	}
	ui.Scroll(c).FillWidth().MaxHeight(140).Children(func() {
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			for i, command := range a.acp.commands() {
				if opt := command.Command; opt != nil && strings.HasPrefix("/"+opt.Name, query) &&
					ui.Button(c.Key(i), "/"+opt.Name).Tooltip(opt.Description).Clicked() {
					a.instruction = "/" + opt.Name + " "
				}
			}
			for i, command := range scholarAgentPrompts {
				if strings.HasPrefix(command.name, query) &&
					ui.Button(c.Key("local").Key(i), command.name+" · "+s.tr(command.prompt)).Clicked() {
					a.instruction = s.tr(command.prompt)
				}
			}
		})
	})
}

func (a *scholarACPState) configOptions() []acp.SessionConfigOption {
	if a.progress.Config != nil {
		return a.progress.Config
	}
	return registry.acp.defaults.Config
}

func (a *scholarACPState) commands() []acp.AvailableCommand {
	if a.progress.Commands != nil {
		return a.progress.Commands
	}
	return registry.acp.defaults.Commands
}

func (s *Shell) scholarACPMenuControl(c *ui.Context, label, accessible string) ui.Element {
	button := ui.ButtonBase(c).Padding(3, 4).MinWidth(0).Radius(4).
		FocusRing(true).Label(accessible).Tooltip(label)
	if button.Hovered() {
		button.Background(s.ch.hover)
	}
	button.Children(func() {
		ui.Row(c).MinWidth(0).Gap(3).AlignItems(ui.Center).Children(func() {
			ui.Text(c.Key("label"), label).FontSize(11).SingleLine().MinWidth(0)
			ui.Icon(c.Key("arrow"), s.icon("chevron-down")).FontSize(10).Shrink(0).TextColor(s.ch.muted)
		})
	})
	return button
}

func (a *scholarACPState) selectByCategory(category acp.SessionConfigOptionCategory) *acp.SessionConfigOptionSelect {
	for _, config := range a.configOptions() {
		if option := config.Select; option != nil && option.Category != nil && *option.Category == category {
			return option
		}
	}
	return nil
}

func (a *scholarACPState) optionValue(option *acp.SessionConfigOptionSelect) acp.SessionConfigValueId {
	for _, setting := range a.settings {
		if setting.Id != nil && setting.Id.ConfigId == option.ConfigId {
			return setting.Id.Value
		}
	}
	return option.CurrentValue
}

func (a *scholarACPState) optionLabel(option *acp.SessionConfigOptionSelect) string {
	current := a.optionValue(option)
	if values := option.Options.Ungrouped; values != nil {
		for _, value := range *values {
			if value.Value == current {
				return value.Name
			}
		}
	}
	if groups := option.Options.Grouped; groups != nil {
		for _, group := range *groups {
			for _, value := range group.Options {
				if value.Value == current {
					return value.Name
				}
			}
		}
	}
	return string(current)
}

func (s *Shell) scholarACPSelectMenu(menu *ui.Menu, a *scholarACPState, option *acp.SessionConfigOptionSelect) {
	current := a.optionValue(option)
	choose := func(target *ui.Menu, value acp.SessionConfigSelectOption) {
		label := s.scholarACPValueLabel(option, value.Value, value.Name)
		if target.Item(label).Radio(value.Value == current).Chosen() && value.Value != current {
			a.setSelectOption(option, value.Value)
		}
	}
	if values := option.Options.Ungrouped; values != nil {
		for _, value := range *values {
			choose(menu, value)
		}
	}
	if groups := option.Options.Grouped; groups != nil {
		for _, group := range *groups {
			menu.Submenu(group.Name, func(groupMenu *ui.Menu) {
				for _, value := range group.Options {
					choose(groupMenu, value)
				}
			})
		}
	}
}

func (a *scholarACPState) setSelectOption(option *acp.SessionConfigOptionSelect, value acp.SessionConfigValueId) {
	a.setOption(acp.SetSessionConfigOptionRequest{Id: &acp.SetSessionConfigOptionId{
		Type: "id", ConfigId: option.ConfigId, Value: value,
	}})
}

func acpConfigID(req acp.SetSessionConfigOptionRequest) acp.SessionConfigId {
	if req.Id != nil {
		return req.Id.ConfigId
	}
	return req.Boolean.ConfigId
}

func (a *scholarACPState) setOption(req acp.SetSessionConfigOptionRequest) {
	a.settings = slices.DeleteFunc(a.settings, func(pending acp.SetSessionConfigOptionRequest) bool {
		return acpConfigID(pending) == acpConfigID(req)
	})
	a.settings = append(a.settings, req)
}

func (s *Shell) scholarDefaultACPThread(p *scholarState) scholarACPThread {
	if t := p.references.focused; t != nil && !p.references.catalog {
		return scholarACPThread{Scope: scholarReferenceScope(t), Label: t.title}
	}
	if t := s.activeTab(); isManuscript(t) {
		if name, err := scholar.Relative(p.project.Root, t.Path); err == nil {
			return scholarACPThread{Scope: "source:" + name, Label: filepath.Base(name)}
		}
	}
	return scholarACPThread{Scope: "project", Label: filepath.Base(p.project.Root)}
}

func (s *Shell) selectScholarACPThread(p *scholarState, thread scholarACPThread) {
	a := &p.agent
	state := &a.acp
	state.historyOpen, state.focusComposer, state.modelDetails = false, true, false
	if state.thread.ID != "" && state.thread.ID == thread.ID {
		return
	}
	p.err = ""
	state.thread, state.progress, state.history, state.quotes, state.settings = thread, scholar.ACPProgress{}, nil, nil, nil
	state.previewQuote, state.previewImage, state.requestedContextWindow = 0, nil, 0
	a.instruction, a.conversation, a.files = "", "", nil
	a.target, a.multi, a.parts, a.include = scholarTarget{}, false, [4]string{}, [4]bool{}
}

func (s *Shell) restoreScholarACPThread(p *scholarState, thread scholarACPThread) {
	if thread.ID == "" {
		return
	}
	s.scholarJob(p, func(ctx context.Context, job uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		client, err := scholar.StartACP(ctx, thread.Workspace, scholar.ACPOptions{FormChanged: s.scholarACPFormChanged(p, job)})
		if err != nil {
			return nil, err
		}
		defer client.Close()
		list, listErr := client.ListSessions(ctx, acp.ListSessionsRequest{Cwd: &client.Cwd})
		if listErr == nil {
			for _, item := range list.Sessions {
				if item.SessionId == thread.ID && item.Title != nil {
					thread.Title = scholarACPTitle(*item.Title)
					break
				}
			}
		}
		id, _, _, err := client.OpenSession(ctx, thread.ID, true)
		if err != nil {
			if scholarACPNeedsLogin(err) {
				return func() { observeScholarACPAuth(client.Info, err) }, nil
			}
			return nil, err
		}
		defer client.ReleaseSession(id)
		progress := client.Progress()
		return func() {
			p.agent.acp.history = progress.Messages
			p.agent.acp.thread.Title, p.agent.acp.scrollToEnd = thread.Title, true
			progress.Messages = nil
			p.agent.acp.progress = progress
		}, nil
	})
}

func (s *Shell) rememberScholarACPThread(p *scholarState, thread scholarACPThread) {
	if s.prefs.ScholarProjects == nil {
		s.prefs.ScholarProjects = make(map[string]scholarProjectPrefs)
	}
	pref := s.prefs.ScholarProjects[p.project.Root]
	recent := slices.DeleteFunc(slices.Clone(pref.ACPThreads), func(old scholarACPThread) bool { return old.ID == thread.ID })
	pref.ACPThreads = append([]scholarACPThread{thread}, recent[:min(len(recent), scholarACPThreadLimit-1)]...)
	s.prefs.ScholarProjects[p.project.Root] = pref
	s.setPref("scholarProjects")
}

func (s *Shell) scholarSourceQuote(p *scholarState, target scholarTarget) scholarACPQuote {
	name, _ := scholar.Relative(p.project.Root, target.path)
	return scholarACPQuote{
		scope: "source:" + name, label: filepath.Base(name),
		locator: fmt.Sprintf(s.tr("scholarACPLine"), target.line+1), text: target.before,
	}
}

func scholarReferenceScope(t *scholarReferenceTab) string {
	if t.entry.RemoteID != "" || t.entry.DOI != "" {
		return scholarEntryACPScope(t.entry)
	}
	if t.web != nil {
		return fmt.Sprintf("web:%x", sha256.Sum256([]byte(t.web.source)))
	}
	return "pdf:" + t.path
}

func scholarEntryACPScope(e scholar.Entry) string {
	if doi := scholar.NormalizeDOI(e.DOI); doi != "" {
		return "doi:" + doi
	}
	if e.RemoteID != "" {
		return fmt.Sprintf("zotero:%d/%s", e.Library, e.RemoteID)
	}
	return "reference:" + e.Source + "#" + e.Key
}

func (s *Shell) scholarACPSelectionBar(c *ui.Context, x, y, width float32, build func()) (panel ui.Element) {
	ui.Overlay(c, func() {
		panel = ui.Row(c).Absolute().Left(x).Top(y).Width(width).Height(36).Padding(4).Gap(3).
			AlignItems(ui.Center).Radius(c.Theme().Radius+4).Background(s.ch.content).Border(1, s.ch.separator).
			Shadow(0, 4, 12, 0, ui.RGBA(0, 0, 0, 0.16)).Role(ui.RolePopup)
		panel.Children(build)
	})
	return panel
}

func (s *Shell) scholarACPAction(c *ui.Context, glyph, label string, accent bool) ui.Element {
	button := ui.ButtonBase(c).MinWidth(0).Height(26).Padding(0, 6).Gap(4).AlignItems(ui.Center).
		Radius(c.Theme().Radius).Label(label).Tooltip(label).FocusRing(true)
	color := s.ch.muted
	if accent {
		color = s.ch.accent
	}
	switch {
	case button.Pressed():
		button.Background(s.ch.selected)
	case button.Hovered():
		button.Background(s.ch.hover)
	case accent:
		button.Background(s.ch.accent.Alpha(0.10))
	}
	button.Children(func() {
		ui.Icon(c.Key("icon"), s.icon(glyph)).FontSize(13).Shrink(0).TextColor(color)
		ui.Text(c.Key("label"), label).FontSize(12).SingleLine().MinWidth(0).TextColor(s.ch.text)
	})
	return button
}

func (s *Shell) quoteScholar(p *scholarState, ref scholarACPQuote, newChat bool) {
	if p.busy || p.agent.pendingPatch() != nil {
		return
	}
	if len(ref.png) > scholarACPImageLimit || len(ref.text) > scholarSelectionLimit {
		p.err = s.tr("scholarACPQuoteLimit")
		return
	}
	state := &p.agent.acp
	thread := scholarACPThread{Scope: ref.scope, Label: ref.label}
	if !newChat {
		for _, old := range s.prefs.ScholarProjects[p.project.Root].ACPThreads {
			if old.Scope == ref.scope {
				thread = old
				break
			}
		}
		if state.thread.ID == "" && state.thread.Scope == ref.scope && thread.ID == "" {
			thread = state.thread
		}
	}
	sameDraft := !newChat && state.thread.ID == thread.ID && state.thread.Scope == thread.Scope
	if !sameDraft {
		s.selectScholarACPThread(p, thread)
	}
	if len(state.quotes) >= 8 && (ref.text != "" || len(ref.png) > 0) {
		p.err = s.tr("scholarACPQuoteLimit")
		return
	}
	if len(ref.png) > 0 && slices.ContainsFunc(state.quotes, func(q scholarACPQuote) bool { return len(q.png) > 0 }) {
		p.err = s.tr("scholarACPQuoteLimit")
		return
	}
	if ref.text != "" || len(ref.png) != 0 {
		state.quotes = append(state.quotes, ref)
	}
	state.focusComposer, state.historyOpen, p.agent.inline = true, false, false
	p.err = ""
	s.showScholarInspector(p, scholarCodex)
	if !sameDraft && thread.ID != "" {
		s.restoreScholarACPThread(p, thread)
	}
}

const scholarACPImageLimit = 2 << 20

func acpQuoteBlocks(info acp.InitializeResponse, quote scholarACPQuote) []acp.ContentBlock {
	label := quote.label + " · " + quote.locator
	var blocks []acp.ContentBlock
	if quote.text != "" {
		uri := "nomo://reference/" + url.PathEscape(quote.label)
		prompt := info.Capabilities.Session.Prompt
		if prompt != nil && prompt.EmbeddedContext != nil {
			blocks = append(blocks, acp.ContentBlock{Resource: &acp.ContentBlockResource{
				Type: "resource", Resource: acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
					Uri: uri, Text: label + "\n" + quote.text,
				}},
			}})
		} else {
			blocks = append(blocks, acp.TextBlock(label+"\n"+quote.text))
		}
	}
	if len(quote.png) > 0 {
		blocks = append(blocks, acp.TextBlock(label), acp.ContentBlock{Image: &acp.ContentBlockImage{
			Type: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(quote.png),
		}})
	}
	return blocks
}

func (s *Shell) sendScholarACP(p *scholarState) {
	a := &p.agent
	if p.busy || a.pendingPatch() != nil {
		return
	}
	thread := a.acp.thread
	if thread.Scope == "" {
		thread = s.scholarDefaultACPThread(p)
	}
	if thread.Workspace == "" {
		thread.Workspace = scholar.NewACPWorkspace()
	}
	instruction := strings.TrimSpace(a.instruction)
	quotes := slices.Clone(a.acp.quotes)
	var textBytes = len(instruction)
	for _, ref := range quotes {
		textBytes += len(ref.text) + len(ref.label) + len(ref.locator)
	}
	for i, part := range a.parts {
		if i != 0 && a.include[i] && part != "" {
			quotes = append(quotes, scholarACPQuote{label: scholarContextNames[i], text: part})
			textBytes += len(part)
		}
	}
	if textBytes > 48<<10 || !utf8.ValidString(instruction) {
		p.err = s.tr("scholarACPQuoteLimit")
		return
	}
	bases, files := make(map[string]scholarTarget), make(map[string]string)
	targets := a.files
	if !a.multi {
		targets = nil
		if a.include[0] && a.target.tabID != 0 {
			targets = []scholarContextFile{{target: a.target}}
		}
	}
	fileBytes := 0
	for _, f := range targets {
		if !s.scholarTargetValid(f.target) {
			p.err = s.tr("scholarStaleBaseline")
			return
		}
		name, err := scholar.Relative(p.project.Root, f.target.path)
		if err != nil {
			p.err = err.Error()
			return
		}
		fileBytes += len(f.target.before)
		if len(bases) == 16 || fileBytes > 32<<10 {
			p.err = s.tr("scholarFileContextLimit")
			return
		}
		bases[name], files[name] = f.target, f.target.before
	}
	settings := slices.Clone(a.acp.settings)
	contextWindow := s.scholarACPWindowOverride(&a.acp)
	review, wasMulti := a.conversation, a.multi
	noEdits := s.tr("scholarACPNoEdits")
	a.running, a.acp.progress.Messages, a.acp.progress.Activity = true, nil, nil
	a.acp.previewQuote, a.acp.previewImage = 0, nil
	a.acp.modelDetails = false
	a.acp.requestedContextWindow, a.acp.progress.ContextWindow = contextWindow, 0
	a.acp.thread = thread
	cancelActivities := make(chan scholar.ACPActivity, 2)
	a.acp.cancelActivities, a.acp.scrollToEnd = cancelActivities, true
	s.scholarJob(p, func(ctx context.Context, job uint64) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		client, err := scholar.StartACP(ctx, thread.Workspace, scholar.ACPOptions{
			Editing: len(files) > 0, ContextWindow: contextWindow, FormChanged: s.scholarACPFormChanged(p, job),
		})
		if err != nil {
			return func() { a.running = false }, err
		}
		defer client.Close()
		if err := client.Stage(files); err != nil {
			return func() { a.running = false }, err
		}
		id, configs, _, err := client.OpenSession(ctx, thread.ID, false)
		if err != nil {
			if scholarACPNeedsLogin(err) {
				return func() {
					a.running = false
					observeScholarACPAuth(client.Info, err)
				}, nil
			}
			return func() { a.running = false }, err
		}
		defer client.ReleaseSession(id)
		thread.ID = id
		mode := acp.SessionConfigValueId("read-only")
		if len(files) > 0 {
			mode = "workspace-write"
		}
		for _, config := range configs {
			if opt := config.Select; opt != nil && opt.ConfigId == "mode" && opt.CurrentValue != mode {
				settings = append([]acp.SetSessionConfigOptionRequest{{Id: &acp.SetSessionConfigOptionId{
					Type: "id", ConfigId: "mode", Value: mode,
				}}}, settings...)
				break
			}
		}
		for _, setting := range settings {
			if setting.Id != nil {
				copy := *setting.Id
				copy.SessionId = id
				setting.Id = &copy
			}
			if setting.Boolean != nil {
				copy := *setting.Boolean
				copy.SessionId = id
				setting.Boolean = &copy
			}
			if _, err := client.SetOption(ctx, setting); err != nil {
				return func() {
					a.running, a.acp.progress = false, client.Progress()
				}, err
			}
		}
		blocks := []acp.ContentBlock{acp.TextBlock(instruction)}
		if len(files) > 0 {
			names := make([]string, 0, len(files))
			for name := range files {
				names = append(names, name)
			}
			sort.Strings(names)
			paths := make([]string, len(names))
			for i, name := range names {
				paths[i] = filepath.ToSlash(name)
			}
			pathList, _ := json.Marshal(paths)
			blocks = append(blocks, acp.TextBlock("Apply the requested changes directly to the attached manuscript copies in the ACP working directory. Keep their paths. Nomo will present the changes for review. The following reference blocks contain the current contents of each editable path: "+string(pathList)))
			for i, name := range names {
				body := files[name]
				if body == "" {
					blocks = append(blocks, acp.TextBlock(paths[i]+" is empty."))
				} else {
					blocks = append(blocks, acpQuoteBlocks(client.Info, scholarACPQuote{
						label: paths[i], locator: "editable manuscript copy", text: body,
					})...)
				}
			}
		}
		if review != "" {
			blocks = append(blocks, acp.TextBlock(review))
		}
		for _, ref := range quotes {
			blocks = append(blocks, acpQuoteBlocks(client.Info, ref)...)
		}
		var mu sync.Mutex
		var latest scholar.ACPProgress
		queued := false
		accepted, err := client.RunTurn(ctx, id, blocks, cancelActivities, func(progress scholar.ACPProgress) {
			mu.Lock()
			latest = progress
			post := !queued
			queued = true
			mu.Unlock()
			if post {
				s.post(func() {
					mu.Lock()
					value := latest
					queued = false
					mu.Unlock()
					if s.scholar == p && p.job == job && a.running {
						a.acp.progress = value
					}
				})
			}
		})
		if !accepted && scholarACPNeedsLogin(err) {
			return func() {
				a.running = false
				observeScholarACPAuth(client.Info, err)
			}, nil
		}
		progress := client.Progress()
		var patches []*scholarPatch
		var reviewWarning error
		if err == nil && ctx.Err() == nil && len(files) > 0 {
			client.ReleaseSession(id)
			var changed map[string]string
			changed, reviewWarning = client.ReadEdits(files)
			names := make([]string, 0, len(changed))
			for name := range changed {
				names = append(names, name)
			}
			sort.Strings(names)
			for i, name := range names {
				limit := scholarReviewMaxFragments - len(patches) - (len(names) - i - 1)
				patches = append(patches, splitScholarReview(bases[name], changed[name], limit, -1)...)
			}
		}
		completed := err == nil && ctx.Err() == nil
		if errors.Is(err, context.Canceled) && p.ctx.Err() == nil {
			err = nil
		}
		return func() {
			a.running, a.acp.progress = false, progress
			if accepted {
				if progress.Title != "" {
					thread.Title = scholarACPTitle(progress.Title)
				}
				if thread.Title == "" {
					thread.Title = scholarACPTitle(instruction)
				}
				a.acp.thread = thread
				a.acp.scrollToEnd = true
				s.rememberScholarACPThread(p, thread)
				a.instruction, a.acp.quotes, a.conversation = "", nil, ""
				a.parts, a.include, a.target, a.files, a.multi = [4]string{}, [4]bool{}, scholarTarget{}, nil, false
				a.acp.history = append(a.acp.history, scholar.ACPMessage{Role: "user", Text: instruction})
				for _, m := range progress.Messages {
					if m.Role == "agent" {
						a.acp.history = append(a.acp.history, m)
					}
				}
				if n := len(a.acp.history); n > 32 {
					a.acp.history = slices.Clone(a.acp.history[n-32:])
				}
				a.acp.progress.Messages = nil
			}
			if completed {
				warning := ""
				if reviewWarning != nil {
					warning = reviewWarning.Error()
				}
				stale := false
				patches = slices.DeleteFunc(patches, func(patch *scholarPatch) bool {
					if !s.scholarTargetValid(patch.target) {
						stale = true
						return true
					}
					return false
				})
				if stale {
					warning = strings.TrimSpace(warning + "\n" + s.tr("scholarStaleBaseline"))
				}
				switch {
				case len(patches) > 0:
					if len(patches) == 1 && len(bases) == 1 && !wasMulti && warning == "" {
						a.patch = patches[0]
					} else {
						a.batch = &scholarPatchSet{hunks: patches, summary: warning}
					}
					s.presentScholarReview(p)
				case warning != "":
					p.err = warning
				case accepted && len(bases) > 0:
					p.err = noEdits
				}
			}
		}, err
	})
}

func scholarACPTitle(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	runes := []rune(line)
	if len(runes) > 60 {
		return string(runes[:60]) + "…"
	}
	return line
}
