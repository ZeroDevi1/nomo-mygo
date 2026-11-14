package scholar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	acp "github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

type ACPMessage struct {
	ID   acp.MessageId
	Role string
	Text string
	meta map[string]json.RawMessage
}

type ACPActivity struct {
	ID, Title, Status, Text string
	Terminal, Cancellable   bool
	meta                    map[string]json.RawMessage
}

type ACPProgress struct {
	PromptID      acp.MessageId
	Messages      []ACPMessage
	Activity      []ACPActivity
	Config        []acp.SessionConfigOption
	Commands      []acp.AvailableCommand
	State         string
	Usage         string
	ContextWindow uint64
	Title         string
	Background    int
}

type acpEvents struct {
	jobCtx              context.Context
	formChanged         func(*ACPForm, bool)
	openForms           int
	mu                  sync.Mutex
	session             acp.SessionId
	messages            map[acp.MessageId]ACPMessage
	order               []acp.MessageId
	activity            []ACPActivity
	config              []acp.SessionConfigOption
	commands            []acp.AvailableCommand
	state, title, usage string
	armed, started      bool
	ack                 acp.MessageId
	idle                chan acp.IdleStateUpdate
	background          []acp.BackgroundWork
	cost                *acp.Cost
	contextWindow       uint64
}

func (e *acpEvents) bind(id acp.SessionId, config []acp.SessionConfigOption, commands []acp.AvailableCommand) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.session = id
	e.config, e.commands = config, commands
}

func (e *acpEvents) replaceOptions(config []acp.SessionConfigOption) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.config = config
}

func (e *acpEvents) arm() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.armed, e.started, e.ack = true, false, ""
	e.messages, e.order, e.activity = nil, nil, nil
}

func (e *acpEvents) acknowledge(id acp.MessageId) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ack = id
}

func (c *ACPConnection) Progress() ACPProgress {
	e := c.events
	e.mu.Lock()
	defer e.mu.Unlock()
	out := ACPProgress{PromptID: e.ack, State: e.state, Usage: e.usage, ContextWindow: e.contextWindow, Title: e.title, Config: e.config, Commands: e.commands}
	if e.cost != nil {
		out.Usage += fmt.Sprintf(" · %.4g %s", e.cost.Amount, acpTail(e.cost.Currency, 12))
	}
	for _, work := range e.background {
		switch {
		case work.Terminal != nil:
			if e.getActivity("terminal:"+string(work.Terminal.TerminalId)).Status != "exited" {
				out.Background++
			}
		case work.ToolCall != nil:
			switch e.getActivity(string(work.ToolCall.ToolCallId)).Status {
			case "completed", "failed", "cancelled":
			default:
				out.Background++
			}
		}
	}
	out.Activity = append(out.Activity, e.activity...)
	for _, id := range e.order {
		out.Messages = append(out.Messages, e.messages[id])
	}
	return out
}

func (e *acpEvents) RequestPermission(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// The agent may work inside its sandbox. Escalations beyond it are declined.
	if e.session == req.SessionId {
		e.putActivity(ACPActivity{ID: "permission", Title: acpTail(req.Title, 512), Status: "denied"})
		for _, option := range req.Options {
			if option.Kind == acp.PermissionOptionKindRejectOnce {
				return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
					Selected: &acp.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: option.OptionId},
				}}, nil
			}
		}
	}
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{
		Cancelled: &acp.RequestPermissionOutcomeCancelled{Outcome: "cancelled"},
	}}, nil
}

func (e *acpEvents) SessionUpdate(_ context.Context, note acp.UpdateSessionNotification) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if note.SessionId != e.session {
		return nil
	}
	u := note.Update
	switch {
	case u.UserMessage != nil:
		m := u.UserMessage
		e.started = e.started || e.armed && (e.ack == "" || e.ack == m.MessageId)
		e.upsertMessage(m.MessageId, "user", m.ContentState(), m.Content, m.MetaState(), m.Meta)
	case u.UserMessageChunk != nil:
		m := u.UserMessageChunk
		e.started = e.started || e.armed && (e.ack == "" || e.ack == m.MessageId)
		e.message(m.MessageId, "user", true, m.Content)
	case u.AgentMessage != nil:
		m := u.AgentMessage
		e.upsertMessage(m.MessageId, "agent", m.ContentState(), m.Content, m.MetaState(), m.Meta)
	case u.AgentMessageChunk != nil:
		m := u.AgentMessageChunk
		e.message(m.MessageId, "agent", true, m.Content)
	case u.AgentThought != nil:
		m := u.AgentThought
		e.upsertMessage(m.MessageId, "thought", m.ContentState(), m.Content, m.MetaState(), m.Meta)
	case u.AgentThoughtChunk != nil:
		m := u.AgentThoughtChunk
		e.message(m.MessageId, "thought", true, m.Content)
	case u.StateUpdate != nil:
		state := u.StateUpdate.StateUpdate
		switch {
		case state.Running != nil:
			e.state, e.started, e.background = "running", e.armed, state.Running.Background
		case state.RequiresAction != nil:
			e.state, e.started, e.background = "requires_action", e.armed, state.RequiresAction.Background
		case state.Idle != nil:
			e.state = "idle"
			idle := state.Idle.IdleStateUpdate
			e.background = acpIdleBackground(idle)
			if e.armed && idle.None == nil && (e.started || e.ack != "" || idle.Error != nil) {
				select {
				case e.idle <- idle:
				default:
				}
			}
		}
	case u.ConfigOptionUpdate != nil:
		e.config = u.ConfigOptionUpdate.ConfigOptions
	case u.AvailableCommandsUpdate != nil:
		e.commands = u.AvailableCommandsUpdate.AvailableCommands
	case u.SessionInfoUpdate != nil:
		if u.SessionInfoUpdate.TitleState() != acp.NullableFieldAbsent {
			e.title = ""
			if u.SessionInfoUpdate.Title != nil {
				e.title = acpTail(*u.SessionInfoUpdate.Title, 512)
			}
		}
	case u.UsageUpdate != nil:
		if u.UsageUpdate.CostState() != acp.NullableFieldAbsent {
			e.cost = u.UsageUpdate.Cost
		}
		e.usage = fmt.Sprintf("%d / %d tokens", u.UsageUpdate.Used, u.UsageUpdate.Size)
		e.contextWindow = u.UsageUpdate.Size
	case u.Notice != nil:
		e.putActivity(ACPActivity{ID: "notice", Title: acpTail(u.Notice.Title, 512), Status: string(u.Notice.Severity)})
	case u.PlanUpdate != nil && u.PlanUpdate.Plan.Items != nil:
		plan := u.PlanUpdate.Plan.Items
		var text strings.Builder
		for _, item := range plan.Entries {
			fmt.Fprintf(&text, "%s · %s\n", item.Status, acpTail(item.Content, 512))
			if text.Len() >= 2048 {
				break
			}
		}
		e.putActivity(ACPActivity{ID: "plan:" + string(plan.PlanId), Title: "Plan", Text: text.String()})
	case u.ToolCallUpdate != nil:
		call := u.ToolCallUpdate
		a := e.getActivity(string(call.ToolCallId))
		a.meta = acpPatchMeta(a.meta, call.MetaState(), call.Meta)
		if call.TitleState() != acp.NullableFieldAbsent {
			a.Title = ""
			if call.Title != nil {
				a.Title = acpTail(*call.Title, 512)
			}
		}
		if call.StatusState() != acp.NullableFieldAbsent {
			a.Status = ""
			if call.Status != nil {
				a.Status = string(*call.Status)
			}
		}
		if call.CapabilitiesState() != acp.NullableFieldAbsent {
			a.Cancellable = call.Capabilities != nil && call.Capabilities.Cancel != nil
		}
		if call.ContentState() != acp.NullableFieldAbsent {
			a.Text = ""
			for _, block := range call.Content {
				a.Text = acpToolText(a.Text, block)
			}
		}
		e.putActivity(a)
	case u.ToolCallContentChunk != nil:
		chunk := u.ToolCallContentChunk
		a := e.getActivity(string(chunk.ToolCallId))
		a.Text = acpToolText(a.Text, chunk.Content)
		e.putActivity(a)
	case u.TerminalUpdate != nil:
		term := u.TerminalUpdate
		a := e.getActivity("terminal:" + string(term.TerminalId))
		a.Terminal = true
		a.meta = acpPatchMeta(a.meta, term.MetaState(), term.Meta)
		if term.CommandState() != acp.NullableFieldAbsent {
			a.Title = ""
			if term.Command != nil {
				a.Title = acpTail(*term.Command, 512)
			}
		}
		if term.OutputState() != acp.NullableFieldAbsent {
			a.Text = ""
			if term.Output != nil {
				a.Text = acpTerminalText("", term.Output.Data)
			}
		}
		if term.CapabilitiesState() != acp.NullableFieldAbsent {
			a.Cancellable = term.Capabilities != nil && term.Capabilities.Cancel != nil
		}
		if term.ExitStatus != nil {
			a.Cancellable, a.Status = false, "exited"
		}
		e.putActivity(a)
	case u.TerminalOutputChunk != nil:
		chunk := u.TerminalOutputChunk
		a := e.getActivity("terminal:" + string(chunk.TerminalId))
		a.Terminal, a.Text = true, acpTerminalText(a.Text, chunk.Data)
		e.putActivity(a)
	}
	return nil
}

func (e *acpEvents) upsertMessage(id acp.MessageId, role string, contentState acp.NullableFieldState, blocks []acp.ContentBlock, metaState acp.NullableFieldState, meta map[string]json.RawMessage) {
	e.message(id, role, contentState == acp.NullableFieldAbsent, blocks...)
	m := e.messages[id]
	m.meta = acpPatchMeta(m.meta, metaState, meta)
	e.messages[id] = m
}

func acpPatchMeta(current map[string]json.RawMessage, state acp.NullableFieldState, patch map[string]json.RawMessage) map[string]json.RawMessage {
	switch state {
	case acp.NullableFieldAbsent:
		return current
	case acp.NullableFieldNull:
		return nil
	}
	next := make(map[string]json.RawMessage)
	size := 0
	for key, value := range current {
		if _, changed := patch[key]; !changed {
			next[key] = value
			size += len(key) + len(value)
		}
	}
	for key, value := range patch {
		if string(value) != "null" && size+len(key)+len(value) <= 2048 {
			next[key] = value
			size += len(key) + len(value)
		}
	}
	return next
}

func acpIdleBackground(idle acp.IdleStateUpdate) []acp.BackgroundWork {
	switch {
	case idle.None != nil:
		return idle.None.Background
	case idle.EndTurn != nil:
		return idle.EndTurn.Background
	case idle.Error != nil:
		return idle.Error.Background
	case idle.Cancelled != nil:
		return idle.Cancelled.Background
	case idle.Refusal != nil:
		return idle.Refusal.Background
	case idle.MaxTokens != nil:
		return idle.MaxTokens.Background
	case idle.MaxTurnRequests != nil:
		return idle.MaxTurnRequests.Background
	default:
		return nil
	}
}

func (e *acpEvents) message(id acp.MessageId, role string, appendContent bool, blocks ...acp.ContentBlock) {
	if e.messages == nil {
		e.messages = make(map[acp.MessageId]ACPMessage)
	}
	m, exists := e.messages[id]
	if !exists {
		if len(e.order) == 32 {
			delete(e.messages, e.order[0])
			e.order = e.order[1:]
		}
		e.order = append(e.order, id)
	}
	if !appendContent {
		m.Text = ""
	}
	m.ID, m.Role = id, role
	limit := 16 << 10
	if role == "user" {
		limit = 96 << 10 // Text, staged manuscript copies, and their ACP framing.
	}
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			content := block.Text.Text
			// Codex ACP replays images as Markdown links to inline data URLs.
			// Keep the transcript and its adjacent citation markers in bounds.
			if role == "user" && strings.HasPrefix(content, "[@image](data:image/") &&
				strings.HasSuffix(content, ")") && strings.Contains(content, ";base64,") {
				content = "\n[图片]"
			}
			m.Text = acpTail(m.Text+content, limit)
		case block.Image != nil:
			m.Text = acpTail(m.Text+"\n[图片]", limit)
		case block.Resource != nil && block.Resource.Resource.TextResourceContents != nil:
			m.Text = acpTail(m.Text+"\n"+block.Resource.Resource.TextResourceContents.Text, limit)
		case block.ResourceLink != nil:
			m.Text = acpTail(m.Text+"\n"+block.ResourceLink.Name, limit)
		}
	}
	e.messages[id] = m
}

func (e *acpEvents) getActivity(id string) ACPActivity {
	for _, item := range e.activity {
		if item.ID == id {
			return item
		}
	}
	return ACPActivity{ID: id}
}

func (e *acpEvents) putActivity(value ACPActivity) {
	for i, item := range e.activity {
		if item.ID == value.ID {
			e.activity[i] = value
			return
		}
	}
	if len(e.activity) == 20 {
		copy(e.activity, e.activity[1:])
		e.activity = e.activity[:19]
	}
	e.activity = append(e.activity, value)
}

func acpToolText(text string, block acp.ToolCallContent) string {
	switch {
	case block.Content != nil && block.Content.Content.Text != nil:
		return acpTail(text+block.Content.Content.Text.Text, 2048)
	case block.Diff != nil:
		var summary strings.Builder
		for _, change := range block.Diff.Changes {
			switch {
			case change.Add != nil:
				fmt.Fprintf(&summary, "\n+ %s", filepath.Base(string(change.Add.Path)))
			case change.Delete != nil:
				fmt.Fprintf(&summary, "\n− %s", filepath.Base(string(change.Delete.Path)))
			case change.Modify != nil:
				fmt.Fprintf(&summary, "\n~ %s", filepath.Base(string(change.Modify.Path)))
			case change.Move != nil:
				fmt.Fprintf(&summary, "\n→ %s → %s", filepath.Base(string(change.Move.OldPath)), filepath.Base(string(change.Move.Path)))
			case change.Copy != nil:
				fmt.Fprintf(&summary, "\n+ %s → %s", filepath.Base(string(change.Copy.OldPath)), filepath.Base(string(change.Copy.Path)))
			}
			if summary.Len() >= 1024 {
				break
			}
		}
		if patch := block.Diff.Patch; patch != nil && patch.Text != "" {
			summary.WriteByte('\n')
			summary.WriteString(acpTail(patch.Text, 1024))
		}
		return acpTail(text+summary.String(), 2048)
	default:
		return text
	}
}

func acpTerminalText(text, data string) string {
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return text
	}
	return acpTail(text+string(bytes), 2048)
}

func acpTail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	at := len(value) - limit
	for at < len(value) && !utf8.RuneStart(value[at]) {
		at++
	}
	return strings.Clone(value[at:])
}
