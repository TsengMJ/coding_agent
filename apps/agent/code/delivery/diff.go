package delivery

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// File change statuses.
const (
	StatusAdded    = "added"
	StatusModified = "modified"
	StatusDeleted  = "deleted"
	StatusRenamed  = "renamed"
	StatusCopied   = "copied"
	StatusChanged  = "type_changed"
)

// FileChange is one file of a Diff.
type FileChange struct {
	Path string `json:"path"`
	// OldPath is set for renames and copies.
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

// Diff is the working tree, untracked files included, against Base.
type Diff struct {
	// Base is the commit the changes are measured from.
	Base  string       `json:"base"`
	Files []FileChange `json:"files"`
	// Patch is the unified diff, cut at MaxPatchBytes.
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated,omitempty"`
}

// MaxPatchBytes caps Diff.Patch.
const MaxPatchBytes = 2 << 20

// diffIndex is a throwaway index, relative to the repository root, so
// staging untracked files for the diff never touches the index the agent
// and the user work with. git's index lock makes overlapping diffs of one
// repository fail rather than mix.
const diffIndex = ".git/quill-diff-index"

// WorkingDiff diffs the working tree against base. Files ignored by
// .gitignore (and the agent's /.quill) are left out.
func WorkingDiff(ctx context.Context, g Git, base string) (*Diff, error) {
	index := []string{"GIT_INDEX_FILE=" + diffIndex}
	if _, err := g.Run(ctx, index, "read-tree", base); err != nil {
		return nil, err
	}
	if _, err := g.Run(ctx, index, "add", "-A"); err != nil {
		return nil, err
	}
	diff := []string{"-c", "core.quotepath=off", "diff", "--cached", "-M", "--no-color", "--no-ext-diff"}
	status, err := g.Run(ctx, index, append(diff, "--name-status", "-z", base)...)
	if err != nil {
		return nil, err
	}
	numstat, err := g.Run(ctx, index, append(diff, "--numstat", "-z", base)...)
	if err != nil {
		return nil, err
	}
	patch, err := g.Run(ctx, index, append(diff, base)...)
	if err != nil {
		return nil, err
	}
	files, err := parseNameStatus(status)
	if err != nil {
		return nil, err
	}
	counts, err := parseNumstat(numstat)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if c, ok := counts[files[i].Path]; ok {
			files[i].Additions, files[i].Deletions, files[i].Binary = c.add, c.del, c.binary
		}
	}
	d := &Diff{Base: base, Files: files, Patch: string(patch)}
	if len(d.Patch) > MaxPatchBytes {
		d.Patch, d.Truncated = d.Patch[:MaxPatchBytes], true
	}
	return d, nil
}

// parseNameStatus reads `git diff --name-status -z`: a status field, then
// one path, or two for renames and copies.
func parseNameStatus(out []byte) ([]FileChange, error) {
	fields := splitZ(out)
	files := []FileChange{}
	for i := 0; i < len(fields); {
		code := fields[i]
		if code == "" {
			return nil, fmt.Errorf("delivery: empty status in name-status output")
		}
		var f FileChange
		switch code[0] {
		case 'R', 'C':
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("delivery: truncated name-status output")
			}
			f = FileChange{OldPath: fields[i+1], Path: fields[i+2], Status: StatusRenamed}
			if code[0] == 'C' {
				f.Status = StatusCopied
			}
			i += 3
		default:
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("delivery: truncated name-status output")
			}
			f = FileChange{Path: fields[i+1], Status: statusName(code[0])}
			i += 2
		}
		files = append(files, f)
	}
	return files, nil
}

func statusName(c byte) string {
	switch c {
	case 'A':
		return StatusAdded
	case 'D':
		return StatusDeleted
	case 'T':
		return StatusChanged
	default:
		return StatusModified
	}
}

type lineCounts struct {
	add, del int
	binary   bool
}

// parseNumstat reads `git diff --numstat -z`, keyed by the new path:
// "add\tdel\tpath\0", or "add\tdel\t\0old\0new\0" for renames; binary files
// count "-".
func parseNumstat(out []byte) (map[string]lineCounts, error) {
	fields := splitZ(out)
	counts := map[string]lineCounts{}
	for i := 0; i < len(fields); i++ {
		add, rest, ok1 := strings.Cut(fields[i], "\t")
		del, path, ok2 := strings.Cut(rest, "\t")
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("delivery: bad numstat line %q", fields[i])
		}
		if path == "" {
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("delivery: truncated numstat output")
			}
			path = fields[i+2]
			i += 2
		}
		var c lineCounts
		if add == "-" || del == "-" {
			c.binary = true
		} else {
			var err1, err2 error
			c.add, err1 = strconv.Atoi(add)
			c.del, err2 = strconv.Atoi(del)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("delivery: bad numstat counts %q", fields[i])
			}
		}
		counts[path] = c
	}
	return counts, nil
}

func splitZ(out []byte) []string {
	out = bytes.TrimSuffix(out, []byte{0})
	if len(out) == 0 {
		return nil
	}
	return strings.Split(string(out), "\x00")
}

// UpstreamBase is where the workspace's branch left the base branch it was
// cloned from (the remote default branch when baseBranch is empty).
func UpstreamBase(ctx context.Context, g Git, baseBranch string) (string, error) {
	ref := "refs/remotes/origin/HEAD"
	if baseBranch != "" {
		ref = "refs/remotes/origin/" + baseBranch
	}
	return trimmed(g.Run(ctx, nil, "merge-base", "HEAD", ref))
}

// DefaultBranch is the remote's default branch as recorded at clone time.
func DefaultBranch(ctx context.Context, g Git) (string, error) {
	ref, err := trimmed(g.Run(ctx, nil, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"))
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(ref, "origin/"), nil
}
