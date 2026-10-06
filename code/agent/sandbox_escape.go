package agent

import (
	"context"
	"maps"
	"strings"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/backend/ossandbox"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/tool"
)

// sandboxArg is the execute argument that asks to run one command outside
// the sandbox (Claude Code's dangerouslyDisableSandbox, Codex's escalated
// permissions).
const sandboxArg = "sandbox"

const unsandboxedLabel = "unsandboxed"

// isUnsandboxedCall reports whether call asks to leave the sandbox.
func isUnsandboxedCall(call llm.ToolCall) bool {
	v, ok := call.Args[sandboxArg].(bool)
	return call.Name == executeToolName && ok && !v
}

// sandboxedShell runs with a sandboxed backend: it tags execute results
// with the sandbox mode, explains failures that look like sandbox denials,
// and handles `sandbox: false` escapes, which HITL has already put in
// front of the user. With nobody to approve (headless auto-approve) or
// sandbox.allow_unsandboxed = false, escapes are refused.
type sandboxedShell struct {
	mode             ossandbox.Mode
	network          ossandbox.Network
	allowUnsandboxed bool
	noReviewer       bool
}

func (sandboxedShell) Name() string { return "SandboxedShell" }

func (m sandboxedShell) WrapModelCall(ctx context.Context, req *core.ModelRequest, next core.ModelHandler) (*core.ModelResponse, error) {
	if !m.allowUnsandboxed {
		return next(ctx, req)
	}
	updated := *req
	updated.Tools = make([]tool.Tool, len(req.Tools))
	for i, t := range req.Tools {
		if t.Name() == executeToolName {
			t = escapableExecute{t}
		}
		updated.Tools[i] = t
	}
	return next(ctx, &updated)
}

func (m sandboxedShell) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	switch req.ToolCall.Name {
	case executeToolName:
	case codetools.RunTestsName, codetools.DiagnosticsName:
		res, err := next(ctx, req)
		if err == nil {
			tagSandbox(res, string(m.mode), false)
		}
		return res, err
	default:
		return next(ctx, req)
	}
	if isUnsandboxedCall(req.ToolCall) {
		switch {
		case !m.allowUnsandboxed:
			return refuse("Not run: running commands outside the sandbox is disabled (sandbox.allow_unsandboxed = false). " +
				"Find an approach that works inside the sandbox, or tell the user the exact command to run themselves."), nil
		case m.noReviewer:
			return refuse("Not run: a command outside the sandbox needs the user's approval, and nobody can approve in this run. " +
				"Find an approach that works inside the sandbox, or tell the user the exact command to run themselves."), nil
		}
		res, err := next(backend.WithoutSandbox(ctx), req)
		tagExecute(res, unsandboxedLabel, false)
		return res, err
	}
	res, err := next(ctx, req)
	if res == nil || err != nil {
		return res, err
	}
	blocked := looksSandboxBlocked(res)
	tagExecute(res, string(m.mode), blocked)
	if blocked {
		res.Content = append(append([]llm.ContentBlock(nil), res.Content...), llm.TextBlock(m.blockedHint()))
	}
	return res, nil
}

func (m sandboxedShell) blockedHint() string {
	network := "have no network access"
	switch m.network {
	case ossandbox.NetworkAllowlist:
		network = "reach the network only through a proxy that allows the domains in sandbox.allowed_domains"
	case ossandbox.NetworkFull:
		network = "have full network access"
	}
	hint := "\n[sandbox] This failure may come from the sandbox (mode " + string(m.mode) + "): " +
		"commands can write only inside the project, temp and build-cache directories, cannot read credential directories, and " + network + ". " +
		"Prefer an approach that stays inside those limits."
	if m.allowUnsandboxed && !m.noReviewer {
		hint += " If the command really must run unconfined, call execute again with \"sandbox\": false; the user will be asked to approve it."
	}
	return hint
}

// sandboxDenials are messages the sandbox makes commands print: Seatbelt
// returns EPERM, bubblewrap's read-only mounts EROFS, a missing network
// fails DNS first, and the allowlist proxy answers 403 to CONNECT.
var sandboxDenials = []string{
	"not in sandbox.allowed_domains",
	"tunnel failed",
	"tunnel connection failed",
	"from proxy after connect",
	"operation not permitted",
	"read-only file system",
	"could not resolve host",
	"temporary failure in name resolution",
	"name or service not known",
	"nodename nor servname provided",
	"network is unreachable",
	"no such host",
}

func looksSandboxBlocked(res *tool.Result) bool {
	a, ok := res.Artifact.(backend.ExecuteArtifact)
	if !ok || a.ExitCode == nil || *a.ExitCode == 0 {
		return false
	}
	var text strings.Builder
	for _, b := range res.Content {
		text.WriteString(b.Text)
	}
	out := strings.ToLower(text.String())
	for _, d := range sandboxDenials {
		if strings.Contains(out, d) {
			return true
		}
	}
	return false
}

func tagExecute(res *tool.Result, sandbox string, blocked bool) {
	if res == nil {
		return
	}
	tagSandbox(res, sandbox, blocked)
	a, ok := res.Artifact.(backend.ExecuteArtifact)
	if !ok {
		return
	}
	a.Sandbox, a.SandboxBlocked = sandbox, blocked
	res.Artifact = a
}

// tagSandbox records the sandbox mode in the tool message metadata, where
// the GUI reads it for every shell-backed tool.
func tagSandbox(res *tool.Result, sandbox string, blocked bool) {
	if res == nil {
		return
	}
	md := maps.Clone(res.Metadata)
	if md == nil {
		md = map[string]any{}
	}
	md["sandbox"] = sandbox
	if blocked {
		md["sandbox_blocked"] = true
	}
	res.Metadata = md
}

// escapableExecute shows the model execute's schema with the sandbox
// argument added.
type escapableExecute struct{ tool.Tool }

func (e escapableExecute) Description() string {
	return e.Tool.Description() + "\n\nCommands run in a sandbox: they can write only inside the project, temp and build-cache directories, " +
		"cannot read credential directories such as ~/.ssh, and have no network access. " +
		"Set \"sandbox\": false only when a command failed because of the sandbox and must run unconfined; the user has to approve it."
}

func (e escapableExecute) Schema() tool.Schema {
	s := maps.Clone(e.Tool.Schema())
	props, _ := s["properties"].(map[string]any)
	props = maps.Clone(props)
	if props == nil {
		props = map[string]any{}
	}
	props[sandboxArg] = map[string]any{
		"type":        "boolean",
		"description": "Leave unset to run in the sandbox. false runs this one command outside the sandbox after the user approves.",
	}
	s["properties"] = props
	return s
}
