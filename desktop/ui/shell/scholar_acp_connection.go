package shell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"time"

	rpc "github.com/caelis-labs/acp-go-sdk"
	acp "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nomo/desktop/internal/scholar"
)

// Account and initial session options are shared by all windows. The probe
// has its own lifetime and never takes a project's foreground job slot.
type scholarACPConnectionState struct {
	cancel     context.CancelFunc
	probe      context.Context
	info       acp.InitializeResponse
	defaults   scholar.ACPProgress
	err        error
	needsLogin bool
}

func (state *scholarACPConnectionState) ready() bool {
	return state.cancel == nil && state.err == nil && !state.needsLogin
}

func startScholarACP() {
	state := &registry.acp
	if state.cancel != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	state.cancel, state.probe = cancel, ctx
	invalidateScholarACP()
	go func() {
		var info acp.InitializeResponse
		var progress scholar.ACPProgress
		client, err := scholar.StartACP(ctx, scholar.NewACPWorkspace(), scholar.ACPOptions{})
		if err == nil {
			info = client.Info
			progress, err = probeScholarACP(ctx, client)
			client.Close()
		}
		mygo.RunOnMain(func() {
			// When the last window closes, the next window owns a new probe.
			if registry.acp.probe != ctx {
				return
			}
			cancel()
			registry.acp.cancel, registry.acp.probe = nil, nil
			publishScholarACP(info, progress, err, false)
		})
	}()
}

func probeScholarACP(ctx context.Context, client *scholar.ACPConnection) (scholar.ACPProgress, error) {
	id, _, _, err := client.OpenSession(ctx, "", false)
	if err != nil {
		return scholar.ACPProgress{}, err
	}
	defer client.ReleaseSession(id)
	return client.Progress(), nil
}

func scholarACPNeedsLogin(err error) bool {
	var requestErr *rpc.RequestError
	return errors.As(err, &requestErr) && requestErr.Code == -32000
}

func publishScholarACP(info acp.InitializeResponse, defaults scholar.ACPProgress, err error, signedOut bool) {
	state := &registry.acp
	if info.ProtocolVersion != 0 {
		state.info = info
	}
	state.defaults, state.err, state.needsLogin = defaults, err, signedOut || scholarACPNeedsLogin(err)
	invalidateScholarACP()
}

func observeScholarACPAuth(info acp.InitializeResponse, err error) {
	if scholarACPNeedsLogin(err) {
		state := &registry.acp
		if state.cancel != nil {
			state.cancel()
			state.cancel, state.probe = nil, nil
		}
		publishScholarACP(info, scholar.ACPProgress{}, err, false)
	}
}

func invalidateScholarACP() {
	for _, window := range registry.shells {
		window.win.Invalidate()
	}
}

func (s *Shell) scholarACPAuth(p *scholarState, method acp.AuthMethodId, logout bool) {
	unavailable := s.tr("scholarACPSignInUnavailable")
	s.scholarJob(p, func(ctx context.Context, job uint64) (func(), error) {
		client, err := scholar.StartACP(ctx, scholar.NewACPWorkspace(), scholar.ACPOptions{FormChanged: s.scholarACPFormChanged(p, job)})
		if err != nil {
			return nil, err
		}
		defer client.Close()
		var progress scholar.ACPProgress
		if logout {
			_, err = client.LogoutAuth(ctx, acp.LogoutAuthRequest{})
		} else if slices.ContainsFunc(client.Info.AuthMethods, func(m acp.AuthMethod) bool {
			return m.Agent != nil && m.Agent.MethodId == method
		}) {
			_, err = client.LoginAuth(ctx, acp.LoginAuthRequest{MethodId: method})
			if err == nil {
				probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				progress, err = probeScholarACP(probeCtx, client)
				cancel()
			}
		} else {
			return nil, errors.New(unavailable)
		}
		return func() {
			publishScholarACP(client.Info, progress, err, logout && err == nil)
		}, nil
	})
}

func (s *Shell) scholarACPAccountMenu(m *ui.Menu, p *scholarState) {
	state := &registry.acp
	for _, method := range state.info.AuthMethods {
		if agent := method.Agent; agent != nil && m.Item(agent.Name).Disabled(p.busy || state.cancel != nil).Chosen() {
			s.scholarACPAuth(p, agent.MethodId, false)
		}
	}
	if len(state.info.AuthMethods) > 0 && !state.needsLogin &&
		m.Item(s.tr("scholarACPLogout")).Disabled(p.busy || state.cancel != nil).Chosen() {
		s.scholarACPAuth(p, "", true)
	}
}

func (s *Shell) scholarACPConnectionNotice(c *ui.Context, p *scholarState) {
	state := &registry.acp
	label := s.tr("scholarACPUnavailable")
	glyph := "info"
	connecting := state.cancel != nil
	missing := !state.needsLogin && (errors.Is(state.err, exec.ErrNotFound) || errors.Is(state.err, os.ErrNotExist))
	switch {
	case connecting:
		label, glyph = s.tr("scholarACPConnecting"), "loader-circle"
	case state.needsLogin:
		label = s.tr("scholarACPSignInRequired")
	case missing:
		label = s.tr("scholarCodexSetup")
	}
	ui.Column(c).FillWidth().MinWidth(0).Padding(6, 8).Gap(3).
		Background(s.ch.hover).Radius(c.Theme().Radius).Children(func() {
		ui.Row(c.Key("status")).FillWidth().Gap(7).AlignItems(ui.Start).Children(func() {
			ui.Icon(c.Key("icon"), s.icon(glyph)).FontSize(13).Shrink(0).TextColor(s.ch.muted)
			message := scholarReaderText(c.Key("message"), label).Grow(1).MinWidth(0)
			if state.err != nil && !connecting {
				message.Tooltip(state.err.Error())
			}
		})
		if connecting {
			return
		}
		if state.needsLogin && len(state.info.AuthMethods) > 0 {
			s.scholarReaderCommand(c.Key("sign-in"), "user", s.tr("scholarACPSignIn"), true).
				Disabled(p.busy).Menu(func(m *ui.Menu) { s.scholarACPAccountMenu(m, p) })
			return
		}
		if missing && s.scholarReaderCommand(c.Key("copy"), "copy", s.tr("scholarACPCopyInstall"), false).Clicked() {
			c.Services().WriteClipboard("npm install -g @agentclientprotocol/codex-acp@2.6.0")
			s.status = s.tr("copied")
		}
		if s.scholarReaderCommand(c.Key("retry"), "refresh-cw", s.tr("scholarACPRetry"), false).Disabled(p.busy).Clicked() {
			p.err = ""
			startScholarACP()
		}
	})
}
