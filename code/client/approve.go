package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
)

// previewMaxLines caps how many lines of file content or diff a preview shows.
const previewMaxLines = 12

// Approver decides tool calls that paused for review, one decision per request.
type Approver interface {
	Decide(ctx context.Context, reqs []hitl.ActionRequest) ([]hitl.Decision, error)
}

// RejectApprover rejects every call, telling the model why.
type RejectApprover struct {
	Reason string
}

func (r RejectApprover) Decide(_ context.Context, reqs []hitl.ActionRequest) ([]hitl.Decision, error) {
	out := make([]hitl.Decision, len(reqs))
	for i := range reqs {
		out[i] = hitl.Decision{Type: hitl.DecisionReject, Message: r.Reason}
	}
	return out, nil
}

// NoReviewerReason is sent to the model when a headless run has nobody to approve.
const NoReviewerReason = "No reviewer is available to approve this tool call (non-interactive run without -y). Do not retry it; report what you would have done instead."

// PromptApprover asks on out and reads answers from in, one line per call:
// y approves, a approves this and the rest of the batch, empty or n rejects,
// any other text rejects with that text as the reason. With Cwd set, file
// changes are previewed as a diff against the files there.
type PromptApprover struct {
	In  *bufio.Reader
	Out io.Writer
	Cwd string
}

func (p PromptApprover) Decide(_ context.Context, reqs []hitl.ActionRequest) ([]hitl.Decision, error) {
	out := make([]hitl.Decision, 0, len(reqs))
	for i, req := range reqs {
		fmt.Fprintf(p.Out, "\n[approve %d/%d] %s\n", i+1, len(reqs), ActionTitle(req))
		if preview := ActionPreviewIn(req, p.Cwd); preview != "" {
			fmt.Fprintln(p.Out, indent(preview, "  "))
		}
		fmt.Fprint(p.Out, "approve? [y]es / [a]ll / [N]o / or type a reason to reject: ")
		line, err := p.In.ReadString('\n')
		if err != nil && line == "" {
			return nil, fmt.Errorf("read approval: %w", err)
		}
		answer := strings.TrimSpace(line)
		switch strings.ToLower(answer) {
		case "y", "yes":
			out = append(out, hitl.Decision{Type: hitl.DecisionApprove})
		case "a", "all":
			for range reqs[i:] {
				out = append(out, hitl.Decision{Type: hitl.DecisionApprove})
			}
			return out, nil
		case "", "n", "no":
			out = append(out, hitl.Decision{Type: hitl.DecisionReject})
		default:
			out = append(out, hitl.Decision{Type: hitl.DecisionReject, Message: answer})
		}
	}
	return out, nil
}

// ActionTitle is a one-line summary: tool name plus its main argument.
func ActionTitle(req hitl.ActionRequest) string {
	title := req.Name
	for _, key := range toolCallArgKeys {
		if v, ok := req.Args[key].(string); ok && v != "" && key != "command" {
			return title + " " + v
		}
	}
	return title
}

// ActionPreview shows what the call will do: the command, the new file
// content, or the replaced text.
func ActionPreview(req hitl.ActionRequest) string {
	str := func(k string) string { s, _ := req.Args[k].(string); return s }
	switch req.Name {
	case "execute":
		return "$ " + str("command")
	case "write_file":
		return clip(str("content"))
	case "edit_file":
		return prefixLines(clip(str("old_string")), "- ") + "\n" + prefixLines(clip(str("new_string")), "+ ")
	case "delete":
		return "rm -r " + str("file_path") + "  (directories are removed with everything inside)"
	default:
		return ""
	}
}

func clip(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= previewMaxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:previewMaxLines], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-previewMaxLines)
}

func prefixLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func indent(s, prefix string) string { return prefixLines(s, prefix) }
