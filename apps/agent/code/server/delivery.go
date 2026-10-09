package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/code/audit"
	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/code/delivery"
	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// PRRequest is POST /threads/{id}/pr and the create_pr tool's arguments.
type PRRequest struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Draft bool   `json:"draft,omitempty"`
}

// PRResult is the pushed commit and its pull request.
type PRResult struct {
	Commit      *delivery.Commit      `json:"commit"`
	PullRequest *delivery.PullRequest `json:"pull_request"`
}

// GetDiff returns the thread's changes, untracked files included: a
// workspace thread against where its branch left the base branch, a local
// thread against HEAD.
func (s *Service) GetDiff(ctx context.Context, threadID string) (*delivery.Diff, error) {
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	info, err := s.sessions.Get(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("%w: %s", ErrThreadNotFound, threadID)
	}
	if info.WorkspaceID == "" {
		return delivery.WorkingDiff(ctx, delivery.HostGit{Dir: info.Cwd}, "HEAD")
	}
	ws, release, err := s.acquireWorkspace(ctx, info.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer release()
	g := s.containerGit(ws)
	base, err := delivery.UpstreamBase(ctx, g, ws.BaseBranch)
	if err != nil {
		return nil, err
	}
	return delivery.WorkingDiff(ctx, g, base)
}

// CreatePR commits the thread's workspace to its quill/ branch, pushes it
// with the signed-in user's GitHub token and opens (or finds) the pull
// request. The caller asking is the approval; the thread must be idle.
func (s *Service) CreatePR(ctx context.Context, threadID string, req PRRequest) (*PRResult, error) {
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	release, err := s.acquire(ctx, threadID)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.deliver(ctx, threadID, req)
}

// deliver is CreatePR for a caller that holds the thread (the create_pr
// tool inside a run).
func (s *Service) deliver(ctx context.Context, threadID string, req PRRequest) (*PRResult, error) {
	info, err := s.sessions.Get(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("%w: %s", ErrThreadNotFound, threadID)
	}
	if info.WorkspaceID == "" {
		return nil, fmt.Errorf("%w: only threads running in a workspace can open pull requests", ErrBadRequest)
	}
	u, ok := auth.UserFrom(ctx)
	if !ok || u.GitHubToken == "" {
		return nil, fmt.Errorf("%w: sign in with GitHub to open pull requests", ErrBadRequest)
	}
	ws, release, err := s.acquireWorkspace(ctx, info.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer release()
	owner, repo, err := delivery.ParseGitHubURL(ws.RepoURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	g := s.containerGit(ws)
	base := ws.BaseBranch
	if base == "" {
		if base, err = delivery.DefaultBranch(ctx, g); err != nil {
			return nil, err
		}
	}
	title := prTitle(req.Title, info)
	commit, err := delivery.CommitAll(ctx, g, delivery.BranchName(threadID), ws.BaseBranch, title, delivery.GitHubAuthor(u.ID, u.Name))
	if err != nil {
		if errors.Is(err, delivery.ErrNothingToDeliver) {
			return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
		return nil, err
	}
	if err := s.push(ctx, ws, commit, u.GitHubToken); err != nil {
		return nil, err
	}
	pr, err := s.github.CreatePullRequest(ctx, u.GitHubToken, delivery.NewPullRequest{
		Owner: owner, Repo: repo, Title: title, Body: prBody(req.Body, threadID),
		Head: commit.Branch, Base: base, Draft: req.Draft,
	})
	if err != nil {
		if errors.Is(err, delivery.ErrPermission) {
			return nil, fmt.Errorf("%w: %v", ErrForbidden, err)
		}
		return nil, err
	}
	s.record(ctx, audit.Event{Action: audit.ActionPullRequest, ThreadID: threadID, WorkspaceID: ws.ID,
		Detail: map[string]any{"url": pr.URL, "branch": commit.Branch, "head": commit.Head, "existing": pr.Existing}})
	return &PRResult{Commit: commit, PullRequest: pr}, nil
}

func (s *Service) push(ctx context.Context, ws *workspace.Workspace, c *delivery.Commit, token string) error {
	if err := delivery.WriteBundle(ctx, s.containerGit(ws), ws.Dir, c); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "quill-delivery-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bundle := filepath.Join(tmp, "delivery.bundle")
	if err := delivery.ReadBundle(ws.Dir, bundle); err != nil {
		return err
	}
	remote := ws.RepoURL
	if s.pushURL != nil {
		remote = s.pushURL(remote)
	}
	return delivery.Push(ctx, tmp, bundle, remote, token, c)
}

func (s *Service) containerGit(ws *workspace.Workspace) delivery.Git {
	return delivery.ContainerGit{Docker: s.workspaces.Docker(), Container: workspace.ContainerName(ws.ID)}
}

func prTitle(title string, info *sessions.Info) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	if info.Title != "" {
		return info.Title
	}
	return "quill: " + info.ThreadID
}

func prBody(body, threadID string) string {
	note := fmt.Sprintf("Opened by quill from thread `%s`.", threadID)
	if strings.TrimSpace(body) == "" {
		return note
	}
	return strings.TrimSpace(body) + "\n\n---\n" + note
}

// createPRToolName is the agent tool that opens a pull request; it always
// needs the user's approval, also in auto-approved runs.
const createPRToolName = "create_pr"

// createPRTool lets the agent deliver its work when the user asks for a PR.
func (s *Service) createPRTool() tool.Tool {
	return &tool.Func{
		ToolName: createPRToolName,
		ToolDescription: "Commit every change in the workspace to the thread's quill/ branch, push it with the user's " +
			"GitHub account and open a pull request against the base branch (an open one is updated instead). " +
			"Use it only when the user asks for a pull request; the user approves each call. Write the title as a " +
			"short summary and the body as what changed and how it was verified.",
		ToolSchema: tool.Schema{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string", "description": "Pull request title, one line."},
				"body":  map[string]any{"type": "string", "description": "Pull request description in Markdown."},
				"draft": map[string]any{"type": "boolean", "description": "Open as a draft."},
			},
			"required": []string{"title"},
		},
		Fn: func(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
			req := PRRequest{}
			req.Title, _ = args["title"].(string)
			req.Body, _ = args["body"].(string)
			req.Draft, _ = args["draft"].(bool)
			res, err := s.deliver(ctx, rt.Config.ThreadID, req)
			if err != nil {
				return &tool.Result{IsError: true, Content: []llm.ContentBlock{llm.TextBlock("Pull request not opened: " + err.Error())}}, nil
			}
			verb := "Opened"
			if res.PullRequest.Existing {
				verb = "Updated"
			}
			return &tool.Result{
				Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("%s pull request #%d: %s (branch %s at %s).",
					verb, res.PullRequest.Number, res.PullRequest.URL, res.Commit.Branch, res.Commit.Head[:min(12, len(res.Commit.Head))]))},
				Metadata: map[string]any{"pull_request": res.PullRequest, "commit": res.Commit},
			}, nil
		},
	}
}

// describeCreatePR is the approval prompt of a create_pr call.
func describeCreatePR(call llm.ToolCall) string {
	title, _ := call.Args["title"].(string)
	return fmt.Sprintf("Push this thread's changes with your GitHub account and open a pull request: %q", title)
}
