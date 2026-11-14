package shell

import (
	"context"
	"slices"
	"strings"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk/experimental/v2"

	"nomo/desktop/internal/scholar"
)

// Bound retained drafts by count and payload. Eight full-size PNGs use 16 MiB;
// the remaining space covers text and manuscript context.
const (
	scholarACPInactiveDraftLimit  = 8
	scholarACPInactiveDraftBudget = 20 << 20
)

type scholarACPDraft struct {
	thread                    scholarACPThread
	instruction, conversation string
	target                    scholarTarget
	parts                     [4]string
	include                   [4]bool
	files                     []scholarContextFile
	multi                     bool
	quotes                    []scholarACPQuote
	settings                  []acp.SetSessionConfigOptionRequest
}

func (a *scholarAgentState) currentACPDraft() scholarACPDraft {
	return scholarACPDraft{
		thread: a.acp.thread, instruction: a.instruction, conversation: a.conversation,
		target: a.target, parts: a.parts, include: a.include, files: a.files, multi: a.multi,
		quotes: a.acp.quotes, settings: a.acp.settings,
	}
}

func sameScholarACPThread(a, b scholarACPThread) bool {
	if a.ID != "" || b.ID != "" {
		return a.ID != "" && a.ID == b.ID
	}
	if a.Workspace != "" || b.Workspace != "" {
		return a.Workspace != "" && a.Workspace == b.Workspace
	}
	return a.Scope == b.Scope
}

func (a *scholarACPState) draftIndex(thread scholarACPThread) int {
	return slices.IndexFunc(a.drafts, func(draft scholarACPDraft) bool {
		return sameScholarACPThread(draft.thread, thread)
	})
}

func (draft scholarACPDraft) payloadBytes() int {
	size := len(draft.instruction) + len(draft.conversation) + len(draft.target.before)
	for _, part := range draft.parts {
		size += len(part)
	}
	for _, file := range draft.files {
		size += len(file.target.before)
	}
	for _, quote := range draft.quotes {
		size += len(quote.text) + len(quote.png)
	}
	return size
}

func (s *Shell) activateScholarACPThread(p *scholarState, thread scholarACPThread) {
	a := &p.agent
	state := &a.acp
	state.historyOpen, state.focusComposer, state.modelDetails = false, true, false
	if sameScholarACPThread(state.thread, thread) {
		state.thread = thread
		return
	}

	draft := scholarACPDraft{thread: thread}
	if index := state.draftIndex(thread); index >= 0 {
		draft = state.drafts[index]
		state.drafts = slices.Delete(state.drafts, index, index+1)
	}
	if a.hasDraft() {
		previous := a.currentACPDraft()
		if previous.thread.Scope == "" {
			previous.thread = s.scholarDefaultACPThread(p)
		}
		if previous.thread.Workspace == "" {
			previous.thread.Workspace = scholar.NewACPWorkspace()
		}
		state.drafts = append(state.drafts, previous)
	}

	p.returnConversation, state.focusTranscript = false, false
	state.thread, state.progress, state.history = thread, scholar.ACPProgress{}, nil
	state.quotes, state.settings = draft.quotes, draft.settings
	state.previewQuote, state.previewImage, state.requestedContextWindow = 0, nil, 0
	state.closeSourcePreview()
	state.scroll.X, state.scroll.Y, state.scrollToEnd = 0, 0, false
	a.instruction, a.conversation, a.files = draft.instruction, draft.conversation, draft.files
	a.target, a.multi, a.parts, a.include = draft.target, draft.multi, draft.parts, draft.include
	a.showParts, a.attachments = 0, false
}

// Apply a history switch only after ACP has replayed the destination session.
// The live draft stays with the current session while the request is in flight.
func (s *Shell) switchScholarACPThread(p *scholarState, thread scholarACPThread, newThread bool, after func()) bool {
	a := &p.agent
	state := &a.acp
	if p.busy || a.pendingPatch() != nil {
		return false
	}
	switching := newThread || !sameScholarACPThread(state.thread, thread)
	canSwitch := func() bool {
		if !switching || !a.hasDraft() {
			return true
		}
		count, size := 1, a.currentACPDraft().payloadBytes()
		for _, draft := range state.drafts {
			if newThread || !sameScholarACPThread(draft.thread, thread) {
				count++
				size += draft.payloadBytes()
			}
		}
		if count > scholarACPInactiveDraftLimit || size > scholarACPInactiveDraftBudget {
			p.err = s.tr("scholarACPDraftLimit")
			return false
		}
		return true
	}
	if !canSwitch() {
		return false
	}
	apply := func() {
		p.err = ""
		s.activateScholarACPThread(p, thread)
		if after != nil {
			after()
		}
	}
	if thread.ID == "" {
		if switching && thread.Workspace == "" {
			thread.Workspace = scholar.NewACPWorkspace()
		}
		apply()
		return true
	}
	s.scholarJob(p, func(ctx context.Context, job uint64) (func(), error) {
		loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		client, err := scholar.StartACP(loadCtx, thread.Workspace, scholar.ACPOptions{FormChanged: s.scholarACPFormChanged(p, job)})
		if err != nil {
			return nil, err
		}
		defer client.Close()
		title := thread.Title
		list, listErr := client.ListSessions(loadCtx, acp.ListSessionsRequest{Cwd: &client.Cwd})
		if listErr == nil {
			for _, item := range list.Sessions {
				if item.SessionId == thread.ID && item.Title != nil {
					title = scholarACPTitle(*item.Title)
					break
				}
			}
		}
		id, _, _, err := client.OpenSession(loadCtx, thread.ID, true)
		if err != nil {
			if scholarACPNeedsLogin(err) {
				return func() { observeScholarACPAuth(client.Info, err) }, err
			}
			return nil, err
		}
		defer client.ReleaseSession(id)
		progress := client.Progress()
		return func() {
			if ctx.Err() != nil || !canSwitch() {
				return
			}
			thread.Title = title
			apply()
			state.history, progress.Messages = progress.Messages, nil
			state.progress, state.scrollToEnd = progress, true
		}, nil
	})
	return true
}

func (draft scholarACPDraft) acceptsQuote(ref scholarACPQuote) bool {
	if len(ref.png) > scholarACPImageLimit || len(ref.text) > scholarSelectionLimit || len(draft.quotes) >= 8 {
		return false
	}
	textBytes := len(ref.text) + len(ref.label) + len(ref.locator) + len(draft.instruction)
	for i, part := range draft.parts {
		if i != 0 && draft.include[i] {
			textBytes += len(part)
		}
	}
	for _, quote := range draft.quotes {
		if len(ref.png) > 0 && len(quote.png) > 0 {
			return false
		}
		textBytes += len(quote.text) + len(quote.label) + len(quote.locator)
	}
	return textBytes <= 48<<10
}

func (a *scholarAgentState) hasDraft() bool {
	return strings.TrimSpace(a.instruction) != "" || len(a.acp.quotes) > 0 ||
		len(a.files) > 0 || slices.Contains(a.include[:], true) || a.conversation != ""
}
