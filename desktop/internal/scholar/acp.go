package scholar

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	rpc "github.com/caelis-labs/acp-go-sdk"
	acp "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
)

const ACPFileLimit = 64 << 10

var activeACPWorkspaces sync.Map

type ACPConnection struct {
	*acp.ClientSideConnection
	Info      acp.InitializeResponse
	Cwd       acp.AbsolutePath
	events    *acpEvents
	process   *stdio.Process
	closeOnce sync.Once
}

func NewACPWorkspace() string { return rand.Text() }

type ACPOptions struct {
	Editing       bool
	ContextWindow int
	FormChanged   func(*ACPForm, bool)
}

func StartACP(ctx context.Context, workspace string, launch ACPOptions) (*ACPConnection, error) {
	if len(workspace) != 26 || strings.ContainsFunc(workspace, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= '2' && r <= '7')
	}) {
		return nil, errors.New("invalid ACP workspace identifier")
	}
	exe := os.Getenv("NOMO_CODEX_ACP")
	if exe == "" {
		exe = "codex-acp"
	} else if !filepath.IsAbs(exe) {
		return nil, errors.New("NOMO_CODEX_ACP must be an absolute executable path")
	}
	path, err := exec.LookPath(exe)
	if err != nil && exe == "codex-acp" && runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		for _, dir := range []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
			path, err = exec.LookPath(filepath.Join(dir, exe))
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("找不到 codex-acp。请安装 @agentclientprotocol/codex-acp@2.6.0，或设置 NOMO_CODEX_ACP：%w", err)
	}
	var args []string
	exeDir := filepath.Dir(path)
	if runtime.GOOS == "windows" && strings.EqualFold(filepath.Ext(path), ".cmd") {
		script := filepath.Join(exeDir, "node_modules", "@agentclientprotocol", "codex-acp", "dist", "index.js")
		if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("无法定位 npm 安装的 Codex ACP 脚本，请设置 NOMO_CODEX_ACP 指向适配器可执行文件")
		}
		path, err = exec.LookPath("node.exe")
		if err != nil {
			return nil, fmt.Errorf("找不到 Node.js：%w", err)
		}
		args = []string{script}
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	parent := filepath.Join(cache, "nomo", "scholar-acp")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(parent, workspace)
	// The application holds the single-instance lock. Fence overlapping jobs
	// in different windows and reclaim a workspace left by an interrupted run.
	if _, busy := activeACPWorkspaces.LoadOrStore(dir, struct{}{}); busy {
		return nil, errors.New("该 ACP 会话正在另一个窗口中运行")
	}
	started := false
	defer func() {
		if !started {
			activeACPWorkspaces.Delete(dir)
		}
	}()
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("无法打开 ACP 工作目录 %s：%w", dir, err)
	}
	mode := "read-only"
	if launch.Editing {
		mode = "workspace-write"
	}
	config := `{"mcp_servers":{},"features":{"shell_tool":false,"hooks":false,"apps":false,"plugins":false,"multi_agent":false},"web_search":"disabled"`
	if launch.ContextWindow > 0 {
		config += `,"model_context_window":` + strconv.Itoa(launch.ContextWindow)
	}
	env := append(os.Environ(),
		"INITIAL_AGENT_MODE="+mode,
		"PATH="+exeDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CODEX_CONFIG="+config+"}",
	)
	process, err := stdio.Start(context.WithoutCancel(ctx), stdio.Command{Executable: path, Args: args, Dir: dir, Env: env, Stderr: io.Discard})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	events := &acpEvents{idle: make(chan acp.IdleStateUpdate, 1), jobCtx: ctx, formChanged: launch.FormChanged}
	options := rpc.DefaultConnectionOptions()
	options.MaxFrameSize = 8 << 20
	options.MaxNotificationBytes = 16 << 20
	options.RejectBatch = true
	wire, err := acp.NewClientSideConnectionWithOptions(events, process.Input(), process.Output(), options)
	client := &ACPConnection{ClientSideConnection: wire, Cwd: acp.AbsolutePath(dir), events: events, process: process}
	started = true
	if err != nil {
		client.Close()
		return nil, err
	}
	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	title := "Nomo Scholar"
	request := acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		Info:            acp.Implementation{Name: "nomo_scholar", Title: &title, Version: "1.0"},
	}
	if launch.FormChanged != nil {
		request.Capabilities.Elicitation = &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}
	}
	client.Info, err = wire.Initialize(initCtx, request)
	if err != nil || client.Info.Capabilities.Session == nil {
		client.Close()
		if err == nil {
			err = errors.New("agent did not advertise ACP v2 sessions")
		}
		return nil, fmt.Errorf("ACP v2 握手失败：%w", err)
	}
	return client, nil
}

func (c *ACPConnection) stopProcess() error {
	// Closing stdin lets Codex ACP reap its app-server child before the
	// transport checks the process group, including on macOS.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.process.Shutdown(ctx)
	if c.ClientSideConnection != nil {
		_ = c.ClientSideConnection.Close()
	}
	return err
}

func (c *ACPConnection) Close() {
	c.closeOnce.Do(func() {
		_ = c.stopProcess()
		_ = os.RemoveAll(string(c.Cwd))
		activeACPWorkspaces.Delete(string(c.Cwd))
	})
}

func (c *ACPConnection) ReleaseSession(id acp.SessionId) {
	select {
	case <-c.Done():
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, _ = c.CloseSession(ctx, acp.CloseSessionRequest{SessionId: id})
}

func (c *ACPConnection) OpenSession(ctx context.Context, id acp.SessionId, replay bool) (acp.SessionId, []acp.SessionConfigOption, []acp.AvailableCommand, error) {
	if id == "" {
		res, err := c.NewSession(ctx, acp.NewSessionRequest{Cwd: c.Cwd})
		if err == nil {
			c.events.bind(res.SessionId, res.ConfigOptions, res.AvailableCommands)
		}
		return res.SessionId, res.ConfigOptions, res.AvailableCommands, err
	}
	c.events.bind(id, nil, nil)
	req := acp.ResumeSessionRequest{SessionId: id, Cwd: c.Cwd}
	if replay {
		req.ReplayFrom = &acp.ReplayFrom{Start: &acp.ReplayFromStartInline{Type: "start"}}
	}
	res, err := c.ResumeSession(ctx, req)
	if err == nil {
		c.events.bind(id, res.ConfigOptions, res.AvailableCommands)
	}
	return id, res.ConfigOptions, res.AvailableCommands, err
}

func (c *ACPConnection) SetOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) ([]acp.SessionConfigOption, error) {
	res, err := c.SetSessionConfigOption(ctx, req)
	if err == nil {
		c.events.replaceOptions(res.ConfigOptions)
	}
	return res.ConfigOptions, err
}

func (c *ACPConnection) RunTurn(ctx context.Context, id acp.SessionId, blocks []acp.ContentBlock, cancels <-chan ACPActivity, progress func(ACPProgress)) (bool, error) {
	prompt := c.Info.Capabilities.Session.Prompt
	for _, block := range blocks {
		if block.Image != nil && (prompt == nil || prompt.Image == nil) {
			return false, errors.New("当前 ACP agent 未声明图像输入能力")
		}
	}
	c.events.arm()
	res, err := c.Prompt(ctx, acp.PromptRequest{SessionId: id, Prompt: blocks})
	if err != nil {
		if ctx.Err() != nil {
			c.cancelTurn(id)
		}
		return false, err
	}
	c.events.acknowledge(res.MessageId)
	progress(c.Progress())
	defer func() { progress(c.Progress()) }()
	ticker := time.NewTicker(65 * time.Millisecond)
	defer ticker.Stop()
	finished := false
	for {
		select {
		case <-ctx.Done():
			c.cancelTurn(id)
			return true, ctx.Err()
		case <-c.Done():
			return true, fmt.Errorf("ACP 连接中断：%w", c.Err())
		case idle := <-c.events.idle:
			if err := acpStopError(idle); err != nil {
				return true, err
			}
			finished = true
			if c.Progress().Background == 0 {
				return true, nil
			}
		case activity := <-cancels:
			if err := c.cancelActivity(ctx, id, activity); err != nil && ctx.Err() == nil {
				c.events.mu.Lock()
				c.events.putActivity(ACPActivity{ID: "cancel-error", Title: acpTail(err.Error(), 512)})
				c.events.mu.Unlock()
			}
		case <-ticker.C:
			value := c.Progress()
			progress(value)
			if finished && value.Background == 0 {
				return true, nil
			}
		}
	}
}

func (c *ACPConnection) cancelActivity(ctx context.Context, id acp.SessionId, activity ACPActivity) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if activity.Terminal {
		_, err := c.CancelTerminal(ctx, acp.CancelTerminalRequest{SessionId: id, TerminalId: acp.TerminalId(strings.TrimPrefix(activity.ID, "terminal:"))})
		return err
	}
	_, err := c.CancelToolCall(ctx, acp.CancelToolCallRequest{SessionId: id, ToolCallId: acp.ToolCallId(activity.ID)})
	return err
}

func (c *ACPConnection) cancelTurn(id acp.SessionId) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if c.Cancel(ctx, acp.CancelSessionNotification{SessionId: id}) != nil {
		return
	}
	select {
	case <-c.events.idle:
	case <-c.Done():
	case <-ctx.Done():
	}
}

func acpStopError(idle acp.IdleStateUpdate) error {
	switch {
	case idle.EndTurn != nil:
		return nil
	case idle.Error != nil && idle.Error.Error != nil:
		return errors.New(idle.Error.Error.Message)
	case idle.Cancelled != nil:
		return context.Canceled
	case idle.MaxTokens != nil:
		return errors.New("ACP 达到输出限制")
	case idle.MaxTurnRequests != nil:
		return errors.New("ACP 达到本轮请求次数限制")
	case idle.Refusal != nil:
		return errors.New("ACP agent 拒绝了本轮请求")
	default:
		return errors.New("ACP agent 未正常完成本轮")
	}
}

func (c *ACPConnection) Stage(files map[string]string) error {
	if len(files) == 0 {
		return nil
	}
	root, err := os.OpenRoot(string(c.Cwd))
	if err != nil {
		return err
	}
	defer root.Close()
	for name, body := range files {
		if !filepath.IsLocal(name) || len(body) > ACPFileLimit {
			return fmt.Errorf("无效的 ACP 稿件：%q", name)
		}
		if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := io.WriteString(f, body)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return err
		}
	}
	return nil
}

// ReadEdits stops the agent before checking the work tree. An unsupported
// operation is reported independently of the other staged manuscripts.
func (c *ACPConnection) ReadEdits(files map[string]string) (map[string]string, error) {
	if err := c.stopProcess(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(string(c.Cwd))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	changes := make(map[string]string)
	var warnings []error
	for _, name := range slices.Sorted(maps.Keys(files)) {
		after, err := readACPManuscript(root, name)
		if err != nil {
			warnings = append(warnings, err)
		} else if after != files[name] {
			changes[name] = after
		}
	}
	// One representative path is enough to report extra outputs. Valid
	// edits to staged files stay available for review.
	err = filepath.WalkDir(string(c.Cwd), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name, err := filepath.Rel(string(c.Cwd), path)
		if err != nil {
			return err
		}
		if _, ok := files[name]; !ok {
			return fmt.Errorf("ACP 创建或移动了文件，无法在稿件审阅中应用：%s", name)
		}
		return nil
	})
	return changes, errors.Join(append(warnings, err)...)
}

func readACPManuscript(root *os.Root, name string) (string, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("ACP 移除或移动了稿件：%s", name)
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("ACP 改动包含无法审阅的文件：%s", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, ACPFileLimit+1))
	if err != nil {
		return "", err
	}
	after := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	if len(data) > ACPFileLimit || !utf8.ValidString(after) || strings.ContainsRune(after, 0) {
		return "", fmt.Errorf("ACP 改动超过文本限制：%s", name)
	}
	return after, nil
}
