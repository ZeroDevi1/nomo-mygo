package scholar

import (
	"context"
	"errors"
	"sync"
	"time"
)

// CodexConversation keeps an ephemeral app-server thread alive between turns.
// Calls are serialized. It expires after two minutes without a request and is
// cancelled with the project. No manuscript path is supplied to the process.
type CodexConversation struct {
	lifetime context.Context
	mu       sync.Mutex
	requests chan codexTurn
	done     chan struct{}
	stop     context.CancelFunc
	model    string
	multi    bool
}
type codexTurn struct {
	ctx    context.Context
	prompt string
	stream func(string)
	result chan codexTurnResult
}
type codexTurnResult struct {
	text string
	err  error
}

func NewCodexConversation(lifetime context.Context) *CodexConversation {
	return &CodexConversation{lifetime: lifetime}
}
func (c *CodexConversation) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		c.stop()
	}
}
func (c *CodexConversation) Run(ctx context.Context, model, prompt string, stream func(string), multi bool) (string, error) {
	if len(prompt) > 48<<10 {
		return "", errors.New("Codex request exceeds 48 KiB")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	c.mu.Lock()
	restart := c.requests == nil || c.model != model || c.multi != multi
	if !restart {
		select {
		case <-c.done:
			restart = true
		default:
		}
	}
	if restart {
		if c.stop != nil {
			c.stop()
		}
		lifetime, stop := context.WithCancel(c.lifetime)
		c.stop, c.requests, c.done, c.model, c.multi = stop, make(chan codexTurn), make(chan struct{}), model, multi
		go c.serve(lifetime, c.requests, c.done, model, multi, stop)
	}
	requests, done := c.requests, c.done
	c.mu.Unlock()
	request := codexTurn{ctx, prompt, stream, make(chan codexTurnResult, 1)}
	select {
	case requests <- request:
	case <-ctx.Done():
		return "", ctx.Err()
	case <-done:
		return "", errors.New("Codex session closed. Send again to reconnect.")
	}
	select {
	case result := <-request.result:
		return result.text, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-done:
		select {
		case result := <-request.result:
			return result.text, result.err
		default:
			return "", errors.New("Codex session disconnected. Reconnect to continue.")
		}
	}
}
func (c *CodexConversation) serve(ctx context.Context, requests <-chan codexTurn, done chan struct{}, model string, multi bool, stop context.CancelFunc) {
	defer close(done)
	defer stop()
	// Startup failures also reach the waiting caller through done.
	_ = codex(ctx, func(r *codexRPC) error {
		thread := ""
		idle := time.NewTimer(2 * time.Minute)
		defer idle.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-idle.C:
				return nil
			case request := <-requests:
				idle.Stop()
				kill := context.AfterFunc(request.ctx, func() {
					timer := time.NewTimer(750 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-ctx.Done():
					case <-timer.C:
						stop()
					}
				})
				text, err := runCodexTurn(request.ctx, r, &thread, model, request.prompt, request.stream, multi)
				kill()
				request.result <- codexTurnResult{text, err}
				if err != nil {
					return err
				}
				idle.Reset(2 * time.Minute)
			}
		}
	})
}
func (c *CodexConversation) ProposePatch(ctx context.Context, model, prompt string, stream func(string)) (PatchProposal, error) {
	raw, err := c.Run(ctx, model, prompt, stream, true)
	if err != nil {
		return PatchProposal{}, err
	}
	return DecodeProposal(raw)
}
