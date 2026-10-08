package agent

import (
	"context"
	"fmt"
	"regexp"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// destructive lists shell commands that cannot be undone and destroy work
// outside the change at hand, with the reason shown to the model. Each
// pattern is anchored to the start of a command segment.
var destructive = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\brm\s+(?:-[a-zA-Z]*[rR][a-zA-Z]*\s+(?:-[a-zA-Z]+\s+)*|-[a-zA-Z]+\s+-[a-zA-Z]*[rR][a-zA-Z]*\s+)(?:--\s+)?(?:/|/\*|~|~/|~/\*|\$HOME|\$\{HOME\}|\.|\./|\*|\./\*|\.\.|\.\./?\*?)(?:\s|;|&|\||$)`),
		"it recursively deletes the filesystem root, the home directory or the whole project"},
	{regexp.MustCompile(`\bgit\s+(?:-C\s+\S+\s+)?reset\s+(?:\S+\s+)*--hard\b`), "it discards all uncommitted changes"},
	{regexp.MustCompile(`\bgit\s+(?:-C\s+\S+\s+)?clean\s+(?:\S+\s+)*-[a-zA-Z]*f`), "it deletes untracked files"},
	{regexp.MustCompile(`\bgit\s+(?:-C\s+\S+\s+)?(?:checkout\s+(?:--\s+)?|restore\s+(?:--\S+\s+)*)\.(?:\s|;|&|\||$)`), "it discards uncommitted changes to every file"},
	{regexp.MustCompile(`\bgit\s+(?:-C\s+\S+\s+)?stash\s+(?:drop|clear)\b`), "it deletes stashed changes"},
	{regexp.MustCompile(`\bgit\s+(?:-C\s+\S+\s+)?push\s+(?:\S+\s+)*(?:--force\b|--force-with-lease\b|-[a-zA-Z]*f\b)`), "it overwrites history on the remote"},
	{regexp.MustCompile(`\b(?:curl|wget)\b[^|;&]*\|\s*(?:sudo\s+)?(?:ba|z|da)?sh\b`), "it runs a script downloaded from the network"},
	{regexp.MustCompile(`\bmkfs(?:\.\w+)?\b|\bdd\s+[^;&|]*\bof=/dev/`), "it overwrites a disk"},
	{regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}`), "it is a fork bomb"},
	{regexp.MustCompile(`\b(?:shutdown|reboot|halt)\b(?:\s|;|&|\||$)`), "it shuts the machine down"},
}

// DestructiveCommand returns why command is destructive, or "".
func DestructiveCommand(command string) string {
	for _, d := range destructive {
		if d.re.MatchString(command) {
			return d.reason
		}
	}
	return ""
}

func isDestructiveCall(call llm.ToolCall) bool {
	command, _ := call.Args["command"].(string)
	return DestructiveCommand(command) != ""
}

// describeExecute is the approval text for a shell command; destructive
// ones say why they need a closer look.
func describeExecute(call llm.ToolCall) string {
	command, _ := call.Args["command"].(string)
	outside := ""
	if isUnsandboxedCall(call) {
		outside = " OUTSIDE the sandbox (full access to this machine and the network)"
	}
	if reason := DestructiveCommand(command); reason != "" {
		return fmt.Sprintf("Destructive command%s: %s.\n\n%s", outside, reason, command)
	}
	return "Run shell command" + outside + "\n\n" + command
}

// destructiveOnly reviews only destructive shell commands, also in
// auto-approved runs.
func destructiveOnly() hitl.InterruptOnConfig {
	return hitl.InterruptOnConfig{
		When:       func(llm.ToolCall) bool { return false },
		AlwaysWhen: isDestructiveCall,
		Describe:   describeExecute,
	}
}

// destructiveGuard refuses destructive shell commands in headless runs
// that approve every tool call, where nobody could confirm them.
type destructiveGuard struct{}

func (destructiveGuard) Name() string { return "DestructiveCommandGuard" }

func (destructiveGuard) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	if req.ToolCall.Name != executeToolName {
		return next(ctx, req)
	}
	command, _ := req.ToolCall.Args["command"].(string)
	reason := DestructiveCommand(command)
	if reason == "" {
		return next(ctx, req)
	}
	return &tool.Result{IsError: true, Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf(
		"Not run: %s, and tool calls are auto-approved, so nobody confirmed it. "+
			"Find a narrower command that only touches what your change needs (for example name the files to delete or restore). "+
			"If the user really wants this, tell them the exact command so they can run it themselves.", reason))}}, nil
}
