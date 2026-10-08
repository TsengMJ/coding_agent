package codetools

import (
	"strconv"
	"strings"
)

// GitStatus is the git_status result. Paths are relative to the project root.
type GitStatus struct {
	Branch     string    `json:"branch,omitempty"`
	Upstream   string    `json:"upstream,omitempty"`
	Ahead      int       `json:"ahead,omitempty"`
	Behind     int       `json:"behind,omitempty"`
	Modified   []string  `json:"modified"`
	Added      []string  `json:"added"`
	Deleted    []string  `json:"deleted"`
	Renamed    []Renamed `json:"renamed"`
	Untracked  []string  `json:"untracked"`
	Conflicted []string  `json:"conflicted"`
	Truncated  bool      `json:"truncated,omitempty"`
}

// Renamed is a rename or copy detected by git.
type Renamed struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// maxStatusPaths caps each git_status list.
const maxStatusPaths = 200

// parsePorcelainV2 parses `git status --porcelain=v2 -z --branch`. prefix is
// `git rev-parse --show-prefix` and is stripped so paths are relative to the
// directory git ran in.
func parsePorcelainV2(out, prefix string) GitStatus {
	st := GitStatus{Modified: []string{}, Added: []string{}, Deleted: []string{}, Renamed: []Renamed{}, Untracked: []string{}, Conflicted: []string{}}
	rel := func(p string) string {
		if prefix != "" && strings.HasPrefix(p, prefix) {
			p = strings.TrimPrefix(p, prefix)
		}
		if p == "" {
			return "./"
		}
		return p
	}
	add := func(list *[]string, p string) {
		if len(*list) >= maxStatusPaths {
			st.Truncated = true
			return
		}
		*list = append(*list, rel(p))
	}
	tokens := strings.Split(out, "\x00")
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "" {
			continue
		}
		switch tok[0] {
		case '#':
			parseBranchHeader(&st, tok)
		case '1':
			f := strings.SplitN(tok, " ", 9)
			if len(f) < 9 {
				continue
			}
			switch xy := f[1]; {
			case xy[0] == 'A':
				add(&st.Added, f[8])
			case xy[0] == 'D' || xy[1] == 'D':
				add(&st.Deleted, f[8])
			default:
				add(&st.Modified, f[8])
			}
		case '2':
			f := strings.SplitN(tok, " ", 10)
			if len(f) < 10 || i+1 >= len(tokens) {
				continue
			}
			i++
			if len(st.Renamed) >= maxStatusPaths {
				st.Truncated = true
				continue
			}
			st.Renamed = append(st.Renamed, Renamed{From: rel(tokens[i]), To: rel(f[9])})
		case 'u':
			f := strings.SplitN(tok, " ", 11)
			if len(f) == 11 {
				add(&st.Conflicted, f[10])
			}
		case '?':
			add(&st.Untracked, strings.TrimPrefix(tok, "? "))
		}
	}
	return st
}

func parseBranchHeader(st *GitStatus, tok string) {
	f := strings.Fields(tok)
	if len(f) < 3 {
		return
	}
	switch f[1] {
	case "branch.head":
		if f[2] != "(detached)" {
			st.Branch = f[2]
		} else {
			st.Branch = "(detached)"
		}
	case "branch.upstream":
		st.Upstream = f[2]
	case "branch.ab":
		if len(f) >= 4 {
			st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[2], "+"))
			st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[3], "-"))
		}
	}
}

// numstat is one `git diff --numstat -z` entry. Binary files report -1 counts.
type numstat struct {
	Path      string
	OldPath   string
	Additions int
	Deletions int
	Binary    bool
}

// parseNumstat parses `git diff --numstat -z`. Renames are
// "A\tD\t\0old\0new\0"; other entries are "A\tD\tpath\0".
func parseNumstat(out string) []numstat {
	var entries []numstat
	tokens := strings.Split(out, "\x00")
	for i := 0; i < len(tokens); i++ {
		tok := strings.TrimLeft(tokens[i], "\n")
		if tok == "" {
			continue
		}
		f := strings.SplitN(tok, "\t", 3)
		if len(f) != 3 {
			continue
		}
		e := numstat{Path: f[2]}
		if f[0] == "-" && f[1] == "-" {
			e.Binary = true
		} else {
			e.Additions, _ = strconv.Atoi(f[0])
			e.Deletions, _ = strconv.Atoi(f[1])
		}
		if f[2] == "" && i+2 < len(tokens) {
			e.OldPath, e.Path = tokens[i+1], tokens[i+2]
			i += 2
		}
		entries = append(entries, e)
	}
	return entries
}

// diffChunk is one file's section of a unified git diff.
type diffChunk struct {
	Path    string
	OldPath string
	Status  string
	Binary  bool
	Text    string
}

// splitUnifiedDiff splits a git diff at each "diff --git" header and reads
// the file status from the extended headers.
func splitUnifiedDiff(out string) []diffChunk {
	var chunks []diffChunk
	var cur *diffChunk
	var body []string
	flush := func() {
		if cur != nil {
			cur.Text = strings.Join(body, "\n")
			chunks = append(chunks, *cur)
		}
	}
	inHunk := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			cur = &diffChunk{Status: "modified"}
			cur.OldPath, cur.Path = headerPaths(line)
			body = []string{line}
			inHunk = false
			continue
		}
		if cur == nil {
			continue
		}
		body = append(body, line)
		if inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case strings.HasPrefix(line, "rename from "):
			cur.Status = "renamed"
			cur.OldPath = unquoteGitPath(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			cur.Path = unquoteGitPath(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "copy from "):
			cur.Status = "copied"
			cur.OldPath = unquoteGitPath(strings.TrimPrefix(line, "copy from "))
		case strings.HasPrefix(line, "copy to "):
			cur.Path = unquoteGitPath(strings.TrimPrefix(line, "copy to "))
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
			cur.Binary = true
		case strings.HasPrefix(line, "+++ ") && !strings.HasSuffix(line, "/dev/null"):
			cur.Path = stripDiffPrefix(unquoteGitPath(strings.TrimPrefix(line, "+++ ")))
		case strings.HasPrefix(line, "--- ") && !strings.HasSuffix(line, "/dev/null"):
			cur.OldPath = stripDiffPrefix(unquoteGitPath(strings.TrimPrefix(line, "--- ")))
		}
	}
	flush()
	for i := range chunks {
		if chunks[i].OldPath == chunks[i].Path {
			chunks[i].OldPath = ""
		}
		chunks[i].Text = strings.TrimRight(chunks[i].Text, "\n")
	}
	return chunks
}

// headerPaths reads "diff --git a/X b/Y" for the unambiguous case where
// both sides are the same length; other cases are fixed by later headers.
func headerPaths(line string) (oldPath, newPath string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if strings.HasPrefix(rest, `"`) {
		return "", ""
	}
	if n := len(rest); n%2 == 1 {
		half := (n - 1) / 2
		a, b := rest[:half], rest[half+1:]
		if stripDiffPrefix(a) == stripDiffPrefix(b) {
			return stripDiffPrefix(a), stripDiffPrefix(b)
		}
	}
	return "", ""
}

func stripDiffPrefix(p string) string {
	p = strings.TrimSuffix(p, "\t")
	for _, pre := range []string{"a/", "b/"} {
		if strings.HasPrefix(p, pre) {
			return strings.TrimPrefix(p, pre)
		}
	}
	return p
}

// unquoteGitPath undoes git's C-style quoting of unusual path names.
func unquoteGitPath(p string) string {
	if len(p) < 2 || p[0] != '"' || p[len(p)-1] != '"' {
		return p
	}
	if s, err := strconv.Unquote(p); err == nil {
		return s
	}
	return p[1 : len(p)-1]
}

// countDiffLines counts added and removed lines inside hunks.
func countDiffLines(text string) (additions, deletions int) {
	inHunk := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk:
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}
