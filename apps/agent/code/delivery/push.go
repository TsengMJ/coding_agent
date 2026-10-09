package delivery

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
)

// ErrNothingToDeliver means the branch has no commits over its base and the
// working tree no changes.
var ErrNothingToDeliver = errors.New("delivery: no changes to deliver")

// BranchPrefix starts every branch quill pushes.
const BranchPrefix = "quill/"

var unsafeBranchChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// BranchName is the branch a thread's changes are pushed to.
func BranchName(threadID string) string {
	s := strings.Trim(unsafeBranchChars.ReplaceAllString(threadID, "-"), "-.")
	if s == "" {
		s = "thread"
	}
	return BranchPrefix + s
}

// Author is who commits are attributed to.
type Author struct{ Name, Email string }

// GitHubAuthor attributes commits to a GitHub login through its noreply
// address.
func GitHubAuthor(login, name string) Author {
	return Author{Name: cmp.Or(name, login), Email: login + "@users.noreply.github.com"}
}

// Commit is a branch ready to push: Head on top of Base.
type Commit struct {
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Head   string `json:"head"`
	// Created is false when the working tree was clean and Head is an
	// earlier commit of the branch.
	Created bool `json:"created"`
}

// CommitAll commits every change of the working tree to branch, which is
// created at HEAD when missing, and returns the commit to push. Project
// hooks do not run.
func CommitAll(ctx context.Context, g Git, branch, baseBranch, message string, author Author) (*Commit, error) {
	base, err := UpstreamBase(ctx, g, baseBranch)
	if err != nil {
		return nil, err
	}
	if _, err := g.Run(ctx, nil, "switch", "-q", "-C", branch); err != nil {
		return nil, err
	}
	if _, err := g.Run(ctx, nil, "add", "-A"); err != nil {
		return nil, err
	}
	staged, err := g.Run(ctx, nil, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	c := &Commit{Branch: branch, Base: base}
	if len(staged) > 0 {
		if _, err := g.Run(ctx, nil,
			"-c", "user.name="+author.Name, "-c", "user.email="+author.Email,
			"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false",
			"commit", "-q", "-m", message); err != nil {
			return nil, err
		}
		c.Created = true
	}
	if c.Head, err = trimmed(g.Run(ctx, nil, "rev-parse", "HEAD")); err != nil {
		return nil, err
	}
	if c.Head == base {
		return nil, ErrNothingToDeliver
	}
	return c, nil
}

// bundlePath is where WriteBundle leaves the bundle, relative to the
// workspace root; /.quill is excluded from git.
const bundlePath = ".quill/delivery.bundle"

// WriteBundle writes c's commits as a bundle in the workspace, whose root
// is dir on this host.
func WriteBundle(ctx context.Context, g Git, dir string, c *Commit) error {
	if err := os.Mkdir(filepath.Join(dir, ".quill"), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	_, err := g.Run(ctx, nil, "bundle", "create", "-q", bundlePath, c.Base+".."+"refs/heads/"+c.Branch)
	return err
}

// ReadBundle copies the bundle WriteBundle left in the workspace at dir
// (on this host) to dst. It refuses symlinks, so the agent cannot point the
// server at another file.
func ReadBundle(dir, dst string) error {
	quillDir := filepath.Join(dir, ".quill")
	if fi, err := os.Lstat(quillDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("delivery: %s is not a directory", quillDir)
	}
	src, err := os.OpenFile(filepath.Join(dir, bundlePath), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("delivery: open bundle: %w", err)
	}
	defer src.Close()
	if fi, err := src.Stat(); err != nil || !fi.Mode().IsRegular() {
		return errors.New("delivery: the bundle is not a regular file")
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, src)
	return errors.Join(err, out.Close())
}

// Push sends c from bundle (a file on this host) to branch c.Branch of
// remoteURL. It works in a new repository under tmpRoot with no system or
// user git config: the base commit comes from the remote (shallow), the
// rest from the bundle.
func Push(ctx context.Context, tmpRoot, bundle, remoteURL, token string, c *Commit) error {
	dir, err := os.MkdirTemp(tmpRoot, "quill-push-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	g := cleanGit{dir: dir, token: token}
	steps := [][]string{
		{"init", "-q", "--bare", "."},
		{"fetch", "-q", "--no-tags", "--depth=1", remoteURL, c.Base},
		{"fetch", "-q", "--no-tags", bundle, "refs/heads/" + c.Branch + ":refs/heads/" + c.Branch},
	}
	for _, args := range steps {
		if _, err := g.Run(ctx, nil, args...); err != nil {
			return err
		}
	}
	head, err := trimmed(g.Run(ctx, nil, "rev-parse", "refs/heads/"+c.Branch))
	if err != nil {
		return err
	}
	if head != c.Head {
		return fmt.Errorf("delivery: bundle has %s at %s, want %s", c.Branch, head, c.Head)
	}
	_, err = g.Run(ctx, nil, "push", "-q", remoteURL, "refs/heads/"+c.Branch+":refs/heads/"+c.Branch)
	return err
}

// cleanGit is HostGit in a repository quill created, with no system or
// global config and the token sent as an HTTP header.
type cleanGit struct{ dir, token string }

func (g cleanGit) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	env = append([]string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "HOME=" + g.dir}, env...)
	if g.token != "" {
		env = append(env, workspace.AuthEnv(g.token)...)
	}
	return HostGit{Dir: g.dir}.Run(ctx, env, args...)
}
