package scholar

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"nomo/desktop/internal/childproc"
)

const CodexOutputLimit = 32 << 10

type Model struct{ ID, Model, DisplayName string }
type CodexInfo struct {
	Models []Model
	More   bool
}

type rpcMessage struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Error  *struct {
		Code    int
		Message string
	}
}

type codexRPC struct {
	writeMu sync.Mutex
	in      *json.Encoder
	scan    *bufio.Scanner
	next    int
}

func codex(ctx context.Context, action func(*codexRPC) error) error {
	path, err := exec.LookPath("codex")
	if err != nil && runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		for _, dir := range []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
			path, err = exec.LookPath(filepath.Join(dir, "codex"))
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		return fmt.Errorf("未找到 Codex CLI：%w", err)
	}
	dir, err := os.MkdirTemp("", "nomo-codex-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "app-server", "--listen", "stdio://",
		"-c", "mcp_servers={}", "-c", "features.shell_tool=false", "-c", `web_search="disabled"`, "--disable", "hooks", "--disable", "apps", "--disable", "plugins", "--disable", "multi_agent")
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return err
	}
	// App-server stderr can contain local configuration or paths. Do not surface it.
	cmd.Stderr = io.Discard
	cleanup, err := childproc.Start(cmd)
	if err != nil {
		_ = in.Close()
		_ = out.Close()
		return err
	}
	defer func() {
		_ = in.Close()
		cancel()
		_ = cmd.Wait()
		cleanup()
	}()
	scan := bufio.NewScanner(out)
	scan.Buffer(make([]byte, 16<<10), 512<<10)
	rpc := &codexRPC{in: json.NewEncoder(in), scan: scan}
	if err := rpc.call("initialize", map[string]any{"clientInfo": map[string]string{
		"name": "nomo_scholar", "title": "Nomo Scholar", "version": "1.0",
	}}, nil, nil); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Codex 握手失败：%w", err)
	}
	if err := rpc.write(map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	err = action(rpc)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (r *codexRPC) write(message any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return r.in.Encode(message)
}

func (r *codexRPC) read() (rpcMessage, error) {
	var msg rpcMessage
	if !r.scan.Scan() {
		if err := r.scan.Err(); err != nil {
			return msg, err
		}
		return msg, errors.New("Codex 已断开")
	}
	if err := json.Unmarshal(r.scan.Bytes(), &msg); err != nil {
		return msg, err
	}
	if msg.Method != "" && len(msg.ID) != 0 {
		// Do not grant approval, credentials, tools or user input to server requests.
		err := r.write(map[string]any{"id": msg.ID, "error": map[string]any{
			"code": -32601, "message": "Nomo Scholar denied the server request",
		}})
		return rpcMessage{}, err
	}
	return msg, nil
}

func (r *codexRPC) call(method string, params any, output any, onEvent func(rpcMessage) error) error {
	r.next++
	id := fmt.Sprint(r.next)
	if err := r.write(map[string]any{"id": r.next, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		msg, err := r.read()
		if err != nil {
			return err
		}
		if string(msg.ID) != id {
			if onEvent != nil {
				if err := onEvent(msg); err != nil {
					return err
				}
			}
			continue
		}
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", method, cleanField(msg.Error.Message, 1024))
		}
		if output == nil {
			return nil
		}
		return json.Unmarshal(msg.Result, output)
	}
}

func (r *codexRPC) account() error {
	var result struct {
		Account            json.RawMessage
		RequiresOpenAIAuth bool
	}
	if err := r.call("account/read", map[string]bool{"refreshToken": false}, &result, nil); err != nil {
		return err
	}
	if result.RequiresOpenAIAuth && (len(result.Account) == 0 || string(result.Account) == "null") {
		return errors.New("Codex 未登录，请在终端运行 codex login")
	}
	return nil
}

func ProbeCodex(ctx context.Context) (CodexInfo, error) {
	var info CodexInfo
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := codex(ctx, func(r *codexRPC) error {
		if err := r.account(); err != nil {
			return err
		}
		var list struct {
			Data       []Model
			NextCursor string
		}
		if err := r.call("model/list", map[string]int{"limit": 50}, &list, nil); err != nil {
			return err
		}
		info.Models = list.Data
		info.More = list.NextCursor != ""
		return nil
	})
	return info, err
}

// Propose sends only caller-supplied context and returns a replacement. It never receives a project path.
func Propose(ctx context.Context, model, prompt string, stream func(string)) (string, error) {
	return propose(ctx, model, prompt, stream, false)
}

func ProposePatch(ctx context.Context, model, prompt string, stream func(string)) (PatchProposal, error) {
	raw, err := propose(ctx, model, prompt, stream, true)
	if err != nil {
		return PatchProposal{}, err
	}
	return DecodeProposal(raw)
}

func propose(ctx context.Context, model, prompt string, stream func(string), multi bool) (string, error) {
	if len(prompt) > 48<<10 {
		return "", errors.New("Codex 请求超过 48 KiB")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	// Give the stdio connection time to send turn/interrupt before killing its process tree.
	processCtx, stopProcess := context.WithCancel(context.WithoutCancel(ctx))
	defer stopProcess()
	killTimer := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(750 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-processCtx.Done():
		case <-timer.C:
			stopProcess()
		}
	})
	defer killTimer()
	var replacement string
	err := codex(processCtx, func(r *codexRPC) error {
		var err error
		replacement, err = runCodexTurn(ctx, r, new(string), model, prompt, stream, multi)
		return err
	})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return replacement, err
}

func runCodexTurn(ctx context.Context, r *codexRPC, savedThread *string, model, prompt string, stream func(string), multi bool) (string, error) {
	var replacement string
	run := func() error {
		if err := r.account(); err != nil {
			return err
		}
		threadID := *savedThread
		if threadID == "" {
			var started struct {
				Thread  struct{ ID string }
				Sandbox struct {
					Type          string
					NetworkAccess bool
				}
				ApprovalsReviewer string
			}
			instructions := "You are Nomo Scholar's academic copy editor. Use only attached excerpts. Do not use tools or execute commands. Preserve citation keys. Return a JSON object with a replacement string for the exact selection."
			if multi {
				instructions = "You are Nomo Scholar's academic editor. Use only explicitly attached files and context. Never use tools or commands. Return summary and edits. Each edit has a project-relative path, an exact unique before substring from the supplied baseline, and after text. Before may be empty only for an attached empty file. Keep hunks disjoint and preserve citation keys. Unchanged files must be omitted."
			}
			params := map[string]any{
				"sandbox": "read-only", "approvalPolicy": "on-request", "approvalsReviewer": "user", "ephemeral": true,
				"developerInstructions": instructions,
			}
			if model != "" {
				params["model"] = model
			}
			if err := r.call("thread/start", params, &started, nil); err != nil {
				return err
			}
			if started.Sandbox.Type != "readOnly" || started.Sandbox.NetworkAccess || started.ApprovalsReviewer != "user" {
				return errors.New("Codex 未确认只读、禁用命令网络及客户端审批策略")
			}
			threadID = started.Thread.ID
			if threadID == "" {
				return errors.New("Codex 没有返回会话 ID")
			}
			*savedThread = threadID
		}

		var final, status string
		var streamText strings.Builder
		lastUpdate := time.Time{}
		handle := func(msg rpcMessage) error {
			switch msg.Method {
			case "item/agentMessage/delta":
				var event struct{ ThreadID, Delta string }
				if json.Unmarshal(msg.Params, &event) == nil && event.ThreadID == threadID {
					if streamText.Len()+len(event.Delta) > CodexOutputLimit {
						return errors.New("Codex 流超过 32 KiB")
					}
					streamText.WriteString(event.Delta)
					if time.Since(lastUpdate) >= 100*time.Millisecond {
						stream(strings.Clone(streamText.String()))
						lastUpdate = time.Now()
					}
				}
			case "item/started":
				var event struct{ Item struct{ Type string } }
				if json.Unmarshal(msg.Params, &event) == nil {
					switch event.Item.Type {
					case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch":
						return errors.New("Codex attempted an unavailable tool. The proposal was cancelled.")
					}
				}
			case "item/completed":
				var event struct {
					ThreadID string
					Item     struct{ Type, Text, Phase string }
				}
				if json.Unmarshal(msg.Params, &event) == nil && event.ThreadID == threadID &&
					event.Item.Type == "agentMessage" && event.Item.Phase != "commentary" {
					if len(event.Item.Text) > CodexOutputLimit {
						return errors.New("Codex 提议超过 32 KiB")
					}
					final = event.Item.Text
				}
			case "turn/completed":
				var event struct {
					ThreadID string
					Turn     struct {
						Status string
						Error  *struct{ Message string }
					}
				}
				if json.Unmarshal(msg.Params, &event) == nil && event.ThreadID == threadID {
					status = event.Turn.Status
					if event.Turn.Error != nil {
						return errors.New(cleanField(event.Turn.Error.Message, 1024))
					}
				}
			}
			return nil
		}
		schema := json.RawMessage(`{"type":"object","properties":{"replacement":{"type":"string"}},"required":["replacement"],"additionalProperties":false}`)
		if multi {
			schema = json.RawMessage(PatchSchema)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var startedTurn struct{ Turn struct{ ID string } }
		if err := r.call("turn/start", map[string]any{
			"threadId": threadID, "input": []any{map[string]any{"type": "text", "text": prompt, "text_elements": []any{}}},
			"outputSchema":   schema,
			"sandboxPolicy":  map[string]any{"type": "readOnly", "networkAccess": false},
			"approvalPolicy": "on-request", "approvalsReviewer": "user",
		}, &startedTurn, handle); err != nil {
			return err
		}
		stopInterrupt := context.AfterFunc(ctx, func() {
			if startedTurn.Turn.ID != "" {
				_ = r.write(map[string]any{"id": "nomo-interrupt", "method": "turn/interrupt",
					"params": map[string]string{"threadId": threadID, "turnId": startedTurn.Turn.ID}})
			}
		})
		defer stopInterrupt()
		for status == "" {
			msg, err := r.read()
			if err != nil {
				return err
			}
			if err := handle(msg); err != nil {
				return err
			}
		}
		if status != "completed" {
			return fmt.Errorf("Codex 会话状态：%s", status)
		}
		if multi {
			replacement = final
			return nil
		}
		var value struct{ Replacement *string }
		if err := json.Unmarshal([]byte(final), &value); err != nil || value.Replacement == nil {
			return errors.New("Codex 未返回有效的替换提议")
		}
		replacement = *value.Replacement
		return nil
	}
	err := run()
	return replacement, err
}
