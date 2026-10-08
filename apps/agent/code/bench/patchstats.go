package bench

import (
	"regexp"
	"slices"
	"strings"
)

// PatchStats summarizes a unified git diff.
type PatchStats struct {
	// Files are the changed paths (new path for renames), sorted.
	Files   []string
	Added   int
	Removed int
	// TestFiles are changed paths the verifier resets before grading.
	TestFiles []string
}

var diffHeader = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)

// ParsePatch counts files and changed lines in a git diff.
func ParsePatch(patch string) PatchStats {
	var st PatchStats
	seen := map[string]bool{}
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		if m := diffHeader.FindStringSubmatch(line); m != nil {
			inHunk = false
			if !seen[m[2]] {
				seen[m[2]] = true
				st.Files = append(st.Files, m[2])
			}
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			st.Added++
		case strings.HasPrefix(line, "-"):
			st.Removed++
		}
	}
	slices.Sort(st.Files)
	for _, f := range st.Files {
		if IsTestPath(f) {
			st.TestFiles = append(st.TestFiles, f)
		}
	}
	return st
}

var (
	testCodeDirs = map[string]bool{"tests": true, "test": true, "__tests__": true, "spec": true, "specs": true}
	testDataDirs = map[string]bool{"testdata": true, "test_data": true, "fixtures": true, "fixture": true,
		"mocks": true, "mock": true, "__snapshots__": true}
	testSuffix = regexp.MustCompile(`(_test\.(go|py|js|jsx|ts|tsx)$)|(^test_.*\.py$)|(\.(test|spec)\.(js|jsx|ts|tsx)$)`)
)

// IsTestPath mirrors is_test_code in the V2 verifier (tests/test.sh) plus
// its fixture/snapshot revert: paths the verifier restores before grading.
func IsTestPath(p string) bool {
	segs := strings.Split(p, "/")
	parents, base := segs[:len(segs)-1], segs[len(segs)-1]
	for _, s := range parents {
		if testDataDirs[s] || testCodeDirs[s] {
			return true
		}
	}
	return base == "conftest.py" || testSuffix.MatchString(base)
}

// FileOverlap returns file-level precision (share of predicted files that
// the gold patch also changes) and recall (share of gold files predicted).
// Test paths are ignored on both sides because the verifier resets them.
// ok is false when either side has no non-test files.
func FileOverlap(predicted, gold []string) (precision, recall float64, ok bool) {
	p := nonTest(predicted)
	g := nonTest(gold)
	if len(p) == 0 || len(g) == 0 {
		return 0, 0, false
	}
	hit := 0
	for f := range p {
		if g[f] {
			hit++
		}
	}
	return float64(hit) / float64(len(p)), float64(hit) / float64(len(g)), true
}

func nonTest(files []string) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		if !IsTestPath(f) {
			out[f] = true
		}
	}
	return out
}
