package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/tool"
)

const (
	// checkpointMarker starts the note that records a saved checkpoint,
	// followed by a JSON checkpointNote.
	checkpointMarker = "[checkpoint]"
	// RollbackToolName restores a checkpoint.
	RollbackToolName    = "rollback"
	checkpointRefPrefix = "refs/quill/checkpoints/"
	checkpointTimeout   = 60 * time.Second
	maxShownCheckpoints = 3
)

var (
	refUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	commitSHA = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

// checkpointNote is what a checkpoint note records.
type checkpointNote struct {
	ID     string `json:"id"`
	Commit string `json:"commit"`
	Reason string `json:"reason"`
}

// CheckpointManager saves the workspace as a git commit before the first
// edit of a turn and after each check that an edit fixed or improved, and
// offers the rollback tool to return to one. Checkpoints live under
// refs/quill/checkpoints/<thread>/; HEAD, the index and the branch are
// untouched. Outside a git repository no checkpoint is saved.
type CheckpointManager struct {
	Workspace backend.SandboxBackend
}

func (CheckpointManager) Name() string { return "CheckpointMiddleware" }

func (m CheckpointManager) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	var history []llm.Message
	if req.State != nil {
		history = req.State.Messages
	}
	thread := ""
	if req.Runtime != nil {
		thread = req.Runtime.Config.ThreadID
	}
	var note string
	if mutatingTools[req.ToolCall.Name] && firstEditOfTurn(history) {
		note = m.save(ctx, history, thread, "before the first edit of this turn")
	}
	res, err := next(ctx, req)
	if err != nil || res == nil {
		return res, err
	}
	if note == "" {
		note = m.afterCheck(ctx, history, req.ToolCall, res, thread)
	}
	if note != "" {
		res.Content = append(slices.Clone(res.Content), llm.TextBlock(note))
	}
	return res, nil
}

// afterCheck saves a checkpoint when res is a check that an edit fixed or
// improved.
func (m CheckpointManager) afterCheck(ctx context.Context, history []llm.Message, call llm.ToolCall, res *tool.Result, thread string) string {
	if _, ok := ClassifyFailure(call, resultText(res)); !ok {
		return ""
	}
	msgs := append(history[:len(history):len(history)], llm.NewToolMessage(call.ID, call.Name, resultText(res)))
	v := deriveRepair(msgs)
	if len(v.Attempts) == 0 {
		return ""
	}
	last := v.Attempts[len(v.Attempts)-1]
	if last.at != len(msgs)-1 || len(last.Files) == 0 || !madeProgress(last) {
		return ""
	}
	kind := "passed"
	if last.Delta != nil {
		kind = string(last.Delta.Kind)
	}
	reason := fmt.Sprintf("attempt %d: %s", len(v.Attempts), kind)
	if last.Result.Category != FailurePassed {
		reason += ", still failing: " + failureLabel(*last.Result)
	}
	return m.save(ctx, history, thread, reason)
}

// save saves a checkpoint and returns its note, or "" when it could not.
func (m CheckpointManager) save(ctx context.Context, history []llm.Message, thread, reason string) string {
	if m.Workspace == nil {
		return ""
	}
	n := len(checkpoints(history, 0)) + 1
	commit, err := saveCheckpoint(ctx, m.Workspace, checkpointRef(thread, n))
	if err != nil {
		return ""
	}
	return checkpointNoteText(checkpointNote{ID: fmt.Sprintf("cp%d", n), Commit: commit, Reason: reason})
}

func checkpointNoteText(n checkpointNote) string {
	raw, _ := json.Marshal(n)
	return fmt.Sprintf("\n\n%s %s\nSaved the workspace as %s; the %s tool restores it.", checkpointMarker, raw, n.ID, RollbackToolName)
}

// firstEditOfTurn reports whether the current turn has neither changed
// files nor saved a checkpoint yet.
func firstEditOfTurn(msgs []llm.Message) bool {
	start := userTurnStart(msgs)
	if len(checkpoints(msgs, start)) > 0 {
		return false
	}
	calls := map[string]llm.ToolCall{}
	for _, m := range msgs[start:] {
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		c, ok := calls[m.ToolCallID]
		if m.Role == llm.RoleTool && ok && changesFiles(c.Name) && m.Status != llm.ToolStatusError && !strings.HasPrefix(m.Text(), "Error") {
			return false
		}
	}
	return true
}

// changesFiles reports whether a successful call of the named tool changed
// files in the workspace.
func changesFiles(name string) bool {
	return mutatingTools[name] || name == RollbackToolName
}

// checkpoints lists the checkpoint notes in msgs[start:].
func checkpoints(msgs []llm.Message, start int) []checkpointNote {
	var out []checkpointNote
	marker := "\n\n" + checkpointMarker + " "
	for _, m := range msgs[start:] {
		if m.Role != llm.RoleTool {
			continue
		}
		text := m.Text()
		for {
			i := strings.Index(text, marker)
			if i < 0 {
				break
			}
			text = text[i+len(marker):]
			line, _, _ := strings.Cut(text, "\n")
			var n checkpointNote
			if json.Unmarshal([]byte(line), &n) == nil && n.ID != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

func checkpointRef(thread string, n int) string {
	thread = strings.TrimLeft(refUnsafe.ReplaceAllString(thread, "-"), ".-")
	if thread == "" {
		thread = "run"
	}
	return fmt.Sprintf("%s%s/%d", checkpointRefPrefix, thread, n)
}

// saveCheckpoint commits the working tree, untracked files included, with
// a temporary index and points ref at the commit.
func saveCheckpoint(ctx context.Context, sb backend.SandboxBackend, ref string) (string, error) {
	script := `set -e
cd "$(git rev-parse --show-toplevel)"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
idx=$(git rev-parse --git-path index)
if [ -f "$idx" ]; then cp "$idx" "$tmp/index"; fi
export GIT_INDEX_FILE="$tmp/index"
git add -A
tree=$(git write-tree)
if head=$(git rev-parse -q --verify HEAD); then parent="-p $head"; else parent=""; fi
commit=$(git -c user.name=quill -c user.email=quill@localhost commit-tree "$tree" $parent -m "quill checkpoint")
git update-ref ` + ref + ` "$commit"
echo "$commit"`
	out, err := runScript(ctx, sb, script)
	if err != nil {
		return "", err
	}
	lines := strings.Fields(out)
	if len(lines) == 0 || !commitSHA.MatchString(lines[len(lines)-1]) {
		return "", fmt.Errorf("checkpoint: unexpected output %q", clipLine(out, 200))
	}
	return lines[len(lines)-1], nil
}

// restoreCheckpoint makes the working tree match commit: changed and
// deleted files are restored, files created since are removed. HEAD and
// the index are untouched.
func restoreCheckpoint(ctx context.Context, sb backend.SandboxBackend, commit string) (restored, removed []string, err error) {
	if !commitSHA.MatchString(commit) {
		return nil, nil, fmt.Errorf("invalid checkpoint commit %q", commit)
	}
	script := `set -e
cd "$(git rev-parse --show-toplevel)"
target=` + commit + `
git cat-file -e "$target^{commit}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
idx=$(git rev-parse --git-path index)
if [ -f "$idx" ]; then cp "$idx" "$tmp/now"; fi
GIT_INDEX_FILE="$tmp/now" git add -A
now=$(GIT_INDEX_FILE="$tmp/now" git write-tree)
changes=$(git -c core.quotepath=off diff-tree -r --no-renames --name-status "$target" "$now")
GIT_INDEX_FILE="$tmp/cp" git read-tree "$target"
GIT_INDEX_FILE="$tmp/cp" git checkout-index -a -f
printf '%s\n' "$changes" | while IFS="$(printf '\t')" read -r status file; do
  if [ "$status" = A ]; then rm -f -- "$file"; fi
done
printf '%s\n' "$changes"`
	out, err := runScript(ctx, sb, script)
	if err != nil {
		return nil, nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		status, file, ok := strings.Cut(line, "\t")
		switch {
		case !ok:
		case status == "A":
			removed = append(removed, file)
		case status == "M" || status == "D" || status == "T":
			restored = append(restored, file)
		}
	}
	return restored, removed, nil
}

func runScript(ctx context.Context, sb backend.SandboxBackend, script string) (string, error) {
	t := checkpointTimeout
	resp := sb.Execute(ctx, script, &t)
	if resp.ExitCode == nil || *resp.ExitCode != 0 {
		return "", fmt.Errorf("git: %s", clipLine(strings.TrimSpace(resp.Output), 300))
	}
	return resp.Output, nil
}

const rollbackToolDescription = `Restore the workspace to a checkpoint. Checkpoints are saved automatically before the first edit of a turn and after each check that an edit fixed or improved; their ids (cp1, cp2, ...) appear in [checkpoint] notes and in <repair-state>.

Every file returns to its content at the checkpoint and files created since are deleted. Use it when later edits made things worse or you want to try another approach from a state that was better. Read files again before editing them.`

func (m CheckpointManager) Tools() []tool.Tool {
	return []tool.Tool{&tool.Func{
		ToolName:        RollbackToolName,
		ToolDescription: rollbackToolDescription,
		ToolSchema: tool.Schema{
			"type": "object",
			"properties": map[string]any{
				"checkpoint": map[string]any{"type": "string", "description": "The checkpoint id, e.g. cp2."},
			},
			"required":             []string{"checkpoint"},
			"additionalProperties": false,
		},
		Fn: m.rollback,
	}}
}

func (m CheckpointManager) rollback(ctx context.Context, r *tool.Runtime, args map[string]any) (*tool.Result, error) {
	id, _ := args["checkpoint"].(string)
	var saved []checkpointNote
	if r != nil && r.State != nil {
		saved = checkpoints(r.State.Messages, 0)
	}
	i := slices.IndexFunc(saved, func(n checkpointNote) bool { return n.ID == strings.TrimSpace(id) })
	if i < 0 {
		if len(saved) == 0 {
			return nil, errors.New("no checkpoint was saved in this conversation")
		}
		var ids []string
		for _, n := range saved {
			ids = append(ids, fmt.Sprintf("%s (%s)", n.ID, n.Reason))
		}
		return nil, fmt.Errorf("unknown checkpoint %q; saved: %s", id, strings.Join(ids, ", "))
	}
	if m.Workspace == nil {
		return nil, errors.New("this workspace cannot run git")
	}
	cp := saved[i]
	restored, removed, err := restoreCheckpoint(ctx, m.Workspace, cp.Commit)
	if err != nil {
		return nil, fmt.Errorf("rollback to %s failed: %w", cp.ID, err)
	}
	if len(restored) == 0 && len(removed) == 0 {
		return textResult(fmt.Sprintf("The workspace already matches %s (%s).", cp.ID, cp.Reason)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Rolled back to %s (%s).", cp.ID, cp.Reason)
	if len(restored) > 0 {
		fmt.Fprintf(&b, " Restored: %s.", strings.Join(clipList(restored, 20), ", "))
	}
	if len(removed) > 0 {
		fmt.Fprintf(&b, " Removed (created after the checkpoint): %s.", strings.Join(clipList(removed, 20), ", "))
	}
	b.WriteString(" The files changed on disk: read a file again before editing it.")
	return textResult(b.String()), nil
}

func textResult(text string) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}}
}
