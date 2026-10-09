package delivery

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrPermission means GitHub refused the user (no push access, or the
// token lacks the repo scope).
var ErrPermission = errors.New("delivery: GitHub refused the request")

// ErrNotGitHub means the repository is not on GitHub.
var ErrNotGitHub = errors.New("delivery: pull requests need a https://github.com/<owner>/<repo> repository")

// GitHub opens pull requests through the REST API.
type GitHub struct {
	// APIURL defaults to https://api.github.com.
	APIURL string
	HTTP   *http.Client
}

// PullRequest is the part of GitHub's pull request the client shows.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	// Existing is true when the branch already had an open pull request,
	// which the push updated.
	Existing bool `json:"existing,omitempty"`
}

// NewPullRequest is what CreatePullRequest opens.
type NewPullRequest struct {
	Owner, Repo string
	Title, Body string
	// Head is the pushed branch, Base the branch to merge into.
	Head, Base string
	Draft      bool
}

// ParseGitHubURL returns the owner and repository of a
// https://github.com/<owner>/<repo>(.git) URL.
func ParseGitHubURL(raw string) (owner, repo string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return "", "", fmt.Errorf("%w: %s", ErrNotGitHub, raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%w: %s", ErrNotGitHub, raw)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

// CreatePullRequest opens a pull request as the token's user. When the
// branch already has an open one, that one is returned with Existing set.
func (g *GitHub) CreatePullRequest(ctx context.Context, token string, pr NewPullRequest) (*PullRequest, error) {
	body, _ := json.Marshal(map[string]any{"title": pr.Title, "body": pr.Body, "head": pr.Head, "base": pr.Base, "draft": pr.Draft})
	var out PullRequest
	status, msg, err := g.do(ctx, token, http.MethodPost, fmt.Sprintf("/repos/%s/%s/pulls", pr.Owner, pr.Repo), body, &out)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusCreated:
		return &out, nil
	case status == http.StatusUnprocessableEntity && strings.Contains(msg, "already exists"):
		return g.openPullRequest(ctx, token, pr)
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound:
		return nil, fmt.Errorf("%w: %d %s (does the user have push access to %s/%s?)", ErrPermission, status, msg, pr.Owner, pr.Repo)
	default:
		return nil, fmt.Errorf("delivery: create pull request: %d %s", status, msg)
	}
}

func (g *GitHub) openPullRequest(ctx context.Context, token string, pr NewPullRequest) (*PullRequest, error) {
	q := url.Values{"head": {pr.Owner + ":" + pr.Head}, "state": {"open"}}
	var list []PullRequest
	status, msg, err := g.do(ctx, token, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?%s", pr.Owner, pr.Repo, q.Encode()), nil, &list)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || len(list) == 0 {
		return nil, fmt.Errorf("delivery: find the open pull request for %s: %d %s", pr.Head, status, msg)
	}
	list[0].Existing = true
	return &list[0], nil
}

// do sends one API request; a 2xx body is decoded into out, any other
// status returns GitHub's message.
func (g *GitHub) do(ctx context.Context, token, method, path string, body []byte, out any) (int, string, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cmp.Or(g.APIURL, "https://api.github.com"), "/")+path, rd)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("delivery: GitHub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, "", err
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, githubMessage(raw), nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, "", fmt.Errorf("delivery: decode GitHub %s %s: %w", method, path, err)
	}
	return resp.StatusCode, "", nil
}

// githubMessage joins GitHub's error message and its validation errors.
func githubMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return strings.TrimSpace(string(raw))
	}
	parts := []string{e.Message}
	for _, x := range e.Errors {
		if x.Message != "" {
			parts = append(parts, x.Message)
		}
	}
	return strings.Join(parts, ": ")
}
