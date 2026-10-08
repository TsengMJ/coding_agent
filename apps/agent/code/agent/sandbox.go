package agent

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/backend/ossandbox"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/code/tools"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/tool"
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
	if policy.Network == ossandbox.NetworkAllowlist {
		p, err := allowlistProxy(cfg.AllowlistDomains())
		if err != nil {
			return nil, policy, err
		}
		policy.Proxy = p.Endpoint()
	}
	return sb, policy, nil
}

var (
	proxiesMu sync.Mutex
	proxies   = map[string]*ossandbox.Proxy{}
)

// allowlistProxy returns the process's proxy for domains, starting it on
// first use; agents with the same allowlist share it until exit.
func allowlistProxy(domains []string) (*ossandbox.Proxy, error) {
	key := strings.Join(domains, ",")
	proxiesMu.Lock()
	defer proxiesMu.Unlock()
	if p, ok := proxies[key]; ok {
		return p, nil
	}
	p, err := ossandbox.StartProxy(ossandbox.ProxyOptions{AllowedDomains: domains, Blocked: proxyBlocked})
	if err != nil {
		return nil, err
	}
	proxies[key] = p
	return p, nil
}

// fakeIPRange is where fake-IP proxies (Clash, Surge and similar in TUN
// mode) answer DNS queries; the proxy tool routes those addresses to the
// real host, so an allowed domain resolving there is still public.
var fakeIPRange = netip.MustParsePrefix("198.18.0.0/15")

// proxyBlocked is tools.BlockedIP except for fakeIPRange.
func proxyBlocked(ip netip.Addr) bool {
	return tools.BlockedIP(ip) && !fakeIPRange.Contains(ip.Unmap())
}

// shellSandboxed reports whether b's shell commands run in a sandbox that
// actually confines them (not one that degraded to warnings).
func shellSandboxed(b backend.Backend) bool {
	s, ok := b.(interface{ Sandboxed() bool })
	return ok && s.Sandboxed()
}

// reviewSandboxedShell drops review of commands the sandbox contains, as
// Claude Code's autoAllowBashIfSandboxed does: execute, run_tests and
// diagnostics run without asking, except destructive commands and
// requests to leave the sandbox.
func reviewSandboxedShell(cfg map[string]hitl.InterruptOnConfig) {
	cfg[executeToolName] = sandboxedExecuteReview()
	for name := range runnerTools {
		delete(cfg, name)
	}
}

// sandboxedExecuteReview reviews only destructive commands and sandbox
// escapes, also in auto-approved runs.
func sandboxedExecuteReview() hitl.InterruptOnConfig {
	return hitl.InterruptOnConfig{
		When:       func(llm.ToolCall) bool { return false },
		AlwaysWhen: func(call llm.ToolCall) bool { return isDestructiveCall(call) || isUnsandboxedCall(call) },
		Describe:   describeExecute,
	}
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
