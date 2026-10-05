package codetools

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/backend"
)

func TestParsePorcelainV2(t *testing.T) {
	out := strings.Join([]string{
		"# branch.oid 1234",
		"# branch.head main",
		"# branch.upstream origin/main",
		"# branch.ab +2 -1",
		"1 .M N... 100644 100644 100644 aaa bbb sub/internal/ws/client.go",
		"1 A. N... 000000 100644 100644 000 ccc sub/new file.go",
		"1 D. N... 100644 000000 000000 ddd 000 sub/gone.go",
		"1 .D N... 100644 100644 000000 eee eee sub/gone2.go",
		"2 R. N... 100644 100644 100644 fff fff R100 sub/b.go",
		"sub/a.go",
		"u UU N... 100644 100644 100644 100644 g1 g2 g3 sub/conflict.go",
		"? sub/tmp/",
		"",
	}, "\x00")
	got := parsePorcelainV2(out, "sub/")
	want := GitStatus{
		Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 1,
		Modified:   []string{"internal/ws/client.go"},
		Added:      []string{"new file.go"},
		Deleted:    []string{"gone.go", "gone2.go"},
		Renamed:    []Renamed{{From: "a.go", To: "b.go"}},
		Untracked:  []string{"tmp/"},
		Conflicted: []string{"conflict.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParsePorcelainV2CleanUsesEmptyLists(t *testing.T) {
	got := parsePorcelainV2("# branch.head (detached)\x00", "")
	if got.Branch != "(detached)" || got.Modified == nil || got.Untracked == nil || got.Renamed == nil {
		t.Fatalf("got %+v", got)
	}
}

func TestParsePorcelainV2CapsLists(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxStatusPaths+5; i++ {
		b.WriteString("? f\x00")
	}
	got := parsePorcelainV2(b.String(), "")
	if len(got.Untracked) != maxStatusPaths || !got.Truncated {
		t.Fatalf("len=%d truncated=%v", len(got.Untracked), got.Truncated)
	}
}

func TestParseNumstat(t *testing.T) {
	out := "8\t2\tinternal/ws/client.go\x00-\t-\tlogo.png\x000\t0\t\x00old name.go\x00new name.go\x00"
	got := parseNumstat(out)
	want := []numstat{
		{Path: "internal/ws/client.go", Additions: 8, Deletions: 2},
		{Path: "logo.png", Binary: true},
		{Path: "new name.go", OldPath: "old name.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestSplitUnifiedDiff(t *testing.T) {
	diff := `diff --git a/client.go b/client.go
index 1..2 100644
--- a/client.go
+++ b/client.go
@@ -1,3 +1,3 @@
 package ws
-var a = 1
+var a = 2
diff --git a/new file.go b/new file.go
new file mode 100644
index 0000000..3
--- /dev/null
+++ b/new file.go
@@ -0,0 +1 @@
+package ws
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-package ws
diff --git a/old.go b/renamed.go
similarity index 100%
rename from old.go
rename to renamed.go
diff --git a/logo.png b/logo.png
index 1..2 100644
Binary files a/logo.png and b/logo.png differ
diff --git "a/\346\227\245.go" "b/\346\227\245.go"
--- "a/\346\227\245.go"
+++ "b/\346\227\245.go"
@@ -1 +1 @@
-a
+b
`
	chunks := splitUnifiedDiff(diff)
	type row struct {
		Path, OldPath, Status string
		Binary                bool
	}
	var got []row
	for _, c := range chunks {
		got = append(got, row{c.Path, c.OldPath, c.Status, c.Binary})
	}
	want := []row{
		{"client.go", "", "modified", false},
		{"new file.go", "", "added", false},
		{"gone.go", "", "deleted", false},
		{"renamed.go", "old.go", "renamed", false},
		{"logo.png", "", "modified", true},
		{"日.go", "", "modified", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if a, d := countDiffLines(chunks[0].Text); a != 1 || d != 1 {
		t.Fatalf("counts = %d/%d", a, d)
	}
	if !strings.HasPrefix(chunks[1].Text, "diff --git a/new file.go") || strings.Contains(chunks[1].Text, "gone.go") {
		t.Fatalf("chunk text = %q", chunks[1].Text)
	}
}

func TestSplitExecOutput(t *testing.T) {
	code := func(n int) *int { return &n }
	tests := []struct {
		name string
		resp backend.ExecuteResponse
		want execResult
	}{
		{"stdout only", backend.ExecuteResponse{Output: "a\x00b\x00", ExitCode: code(0)}, execResult{Stdout: "a\x00b\x00"}},
		{"no output", backend.ExecuteResponse{Output: "<no output>", ExitCode: code(0)}, execResult{}},
		{"stderr and exit code", backend.ExecuteResponse{
			Output: "partial\n[stderr] fatal: not a git repository\n[stderr] hint\n\nExit code: 128", ExitCode: code(128)},
			execResult{Stdout: "partial", Stderr: "fatal: not a git repository\nhint", ExitCode: 128}},
		{"truncated", backend.ExecuteResponse{Output: "abc\n\n... Output truncated at 3 bytes.", ExitCode: code(0), Truncated: true},
			execResult{Stdout: "abc", Truncated: true}},
		{"head and tail", backend.ExecuteResponse{Output: "a\n[... output omitted ...]\nz\n[stderr] boom\n\n... Output truncated at 3 bytes: 9 bytes were omitted where marked \"[... output omitted ...]\"; rerun through grep, head or tail to see them.\n\nExit code: 1",
			ExitCode: code(1), Truncated: true},
			execResult{Stdout: "a\n[... output omitted ...]\nz", Stderr: "boom", ExitCode: 1, Truncated: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitExecOutput(tt.resp); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestProjectPath(t *testing.T) {
	for in, want := range map[string]string{"/a/b.go": "a/b.go", "a/b/": "a/b", "./x": "x", "/": ".", "": "."} {
		if got, err := projectPath(in); err != nil || got != want {
			t.Errorf("projectPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"../x", "/a/../../b", "a\nb"} {
		if _, err := projectPath(bad); err == nil {
			t.Errorf("projectPath(%q) should fail", bad)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("got %s", got)
	}
}
