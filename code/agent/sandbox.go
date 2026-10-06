package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend/ossandbox"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/code/config"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/tool"
)

// NewShellSandbox returns the sandbox and policy shell commands in cwd run
// under; the sandbox is nil when s leaves it off. When the platform
// sandbox cannot run, sandbox.on_unavailable decides between an error and
// unconfined commands that warn in every result.
func NewShellSandbox(s *config.Settings, cwd string) (ossandbox.Sandbox, ossandbox.Policy, error) {
	if s == nil || !s.Sandbox.Enabled() {
		return nil, ossandbox.Policy{}, nil
	}
	cfg := s.Sandbox
	policy := ossandbox.DefaultPolicy(cfg.Mode, cwd)
	policy.Network = cfg.Network
	if cfg.Mode == ossandbox.WorkspaceWrite {
		policy.WritableRoots = append(policy.WritableRoots, cfg.WritableRoots...)
	}
	policy.DenyRead = append(policy.DenyRead, cfg.DenyRead...)
	sb, err := ossandbox.Native()
	if err != nil {
		if cfg.OnUnavailable == config.SandboxUnavailableWarn {
			return ossandbox.Unconfined(err.Error()), policy, nil
		}
		return nil, policy, err
	}
	return sb, policy, nil
}

// fileWriteTools change files through the backend rather than the shell.
var fileWriteTools = []string{"write_file", "edit_file", "delete", codetools.ApplyPatchName}

// sandboxFileGuard keeps file tools in step with the shell sandbox:
// read-only mode refuses every write, and workspace-write refuses the
// project files the sandbox protects (.git/hooks, .git/config), since a
// hook written there runs outside any sandbox on the user's next commit.
type sandboxFileGuard struct{ mode ossandbox.Mode }

func (sandboxFileGuard) Name() string { return "SandboxFileGuard" }

func (g sandboxFileGuard) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	if !slices.Contains(fileWriteTools, req.ToolCall.Name) {
		return next(ctx, req)
	}
	if g.mode == ossandbox.ReadOnly {
		return refuse("Not run: the sandbox is in read-only mode (sandbox.mode = \"read-only\"), so files cannot be changed. " +
			"Describe the change instead, or ask the user to switch to workspace-write."), nil
	}
	for _, p := range codetools.MutatedPaths(req.ToolCall) {
		if protectedPath(p) {
			return refuse(fmt.Sprintf("Not run: %s is protected by the sandbox because changing it could run code outside the sandbox. "+
				"Leave git hooks and git config to the user.", p)), nil
		}
	}
	return next(ctx, req)
}

func protectedPath(p string) bool {
	return p == "/.git/config" || p == "/.git/hooks" || strings.HasPrefix(p, "/.git/hooks/")
}

func refuse(msg string) *tool.Result {
	return &tool.Result{IsError: true, Content: []llm.ContentBlock{llm.TextBlock(msg)}}
}
