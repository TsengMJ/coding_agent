package backend

import (
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "foo/bar", want: "/foo/bar"},
		{in: "/./foo//bar", want: "/foo/bar"},
		{in: "foo..bar.txt", want: "/foo..bar.txt"},
		{in: "../etc/passwd", wantErr: "Path traversal not allowed"},
		{in: "/a/../../b", wantErr: "Path traversal not allowed"},
		{in: "~/x", wantErr: "Path traversal not allowed"},
		{in: `C:\Users\f.txt`, wantErr: "Windows absolute paths"},
	}
	for _, tt := range tests {
		got, err := ValidatePath(tt.in, nil)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidatePath(%q) error = %v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ValidatePath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestValidatePathAllowedPrefixes(t *testing.T) {
	if _, err := ValidatePath("/data/f.txt", []string{"/data/"}); err != nil {
		t.Fatalf("allowed prefix rejected: %v", err)
	}
	if _, err := ValidatePath("/etc/f.txt", []string{"/data/"}); err == nil {
		t.Fatal("path outside allowed prefixes accepted")
	}
}

func TestFileTypeForPath(t *testing.T) {
	cases := map[string]FileType{"a.go": FileTypeText, "x/IMG.PNG": FileTypeImage, "doc.pdf": FileTypePDF, "a.mp3": FileTypeAudio}
	for p, want := range cases {
		if got := FileTypeForPath(p); got != want {
			t.Errorf("FileTypeForPath(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestFormatContentWithLineNumbers(t *testing.T) {
	got, _ := FormatContentWithLineNumbers("a\n\nc\n", 9)
	want := " 9  a\n10  \n11  c"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatContentWithLineNumbersSplitsLongLines(t *testing.T) {
	got, _ := FormatContentWithLineNumbers(strings.Repeat("x", MaxLineLength+1), 1)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "1.1  x") {
		t.Fatalf("unexpected continuation rows: %q", lines)
	}
}

func TestCheckEmptyContent(t *testing.T) {
	if CheckEmptyContent("  \n") != EmptyContentWarning || CheckEmptyContent("x") != "" {
		t.Fatal("CheckEmptyContent misclassified content")
	}
}

func TestSliceReadResponse(t *testing.T) {
	fd, _ := CreateFileData("l1\r\nl2\nl3\n", "")
	res, err := SliceReadResponse(fd, 1, 1)
	if err != nil || res.Error != "" {
		t.Fatalf("unexpected error: %v %q", err, res.Error)
	}
	if res.FileData.Content != "l2\n" || *res.TotalLines != 3 || *res.StartLine != 2 || *res.EndLine != 2 || *res.NextOffset != 2 {
		t.Fatalf("unexpected window: %+v content=%q", res, res.FileData.Content)
	}
}

func TestSliceReadResponseLastWindowHasNoNextOffset(t *testing.T) {
	fd, _ := CreateFileData("l1\r\nl2", "")
	res, _ := SliceReadResponse(fd, 0, 10)
	if res.NextOffset != nil || res.FileData.Content != "l1\nl2" {
		t.Fatalf("unexpected result: %+v content=%q", res, res.FileData.Content)
	}
}

func TestSliceReadResponseOffsetPastEnd(t *testing.T) {
	fd, _ := CreateFileData("a\n", "")
	res, _ := SliceReadResponse(fd, 5, 10)
	if !strings.Contains(res.Error, "exceeds file length (1 lines)") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

func TestSliceReadResponseZeroLimit(t *testing.T) {
	fd, _ := CreateFileData("a\n", "")
	res, _ := SliceReadResponse(fd, 0, -3)
	if !res.NoLinesRequested || res.TotalLines != nil {
		t.Fatalf("zero limit must be flagged: %+v", res)
	}
}

func TestPerformStringReplacement(t *testing.T) {
	tests := []struct {
		name, content, old, new string
		all                     bool
		want                    string
		wantN                   int
		wantErr                 string
	}{
		{name: "unique", content: "a b c", old: "b", new: "X", want: "a X c", wantN: 1},
		{name: "replace all", content: "b b", old: "b", new: "X", all: true, want: "X X", wantN: 2},
		{name: "ambiguous", content: "b b", old: "b", new: "X", wantErr: "appears 2 times"},
		{name: "missing", content: "abc", old: "z", new: "X", wantErr: "String not found"},
		{name: "empty old", content: "abc", old: "", new: "X", wantErr: "old_string cannot be empty"},
		{name: "eof newline", content: "x\nlast", old: "last\n", new: "y", wantErr: "does not end with a newline. Retry"},
	}
	for _, tt := range tests {
		got, n, err := PerformStringReplacement(tt.content, tt.old, tt.new, tt.all)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%s: error = %v, want %q", tt.name, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want || n != tt.wantN {
			t.Errorf("%s: got %q, %d, %v", tt.name, got, n, err)
		}
	}
}

func TestTruncateIfTooLong(t *testing.T) {
	long := strings.Repeat("x", ToolResultTokenLimit*4+10)
	got := TruncateIfTooLong(long)
	if len(got) != ToolResultTokenLimit*4 || !strings.HasSuffix(got, TruncationGuidance) {
		t.Fatalf("unexpected truncation: len=%d", len(got))
	}
	if TruncateIfTooLong("short") != "short" {
		t.Fatal("short result must be unchanged")
	}
}

func TestSanitizeToolCallID(t *testing.T) {
	if got := SanitizeToolCallID("a.b/c"); got != "a_b_c" {
		t.Fatalf("got %q", got)
	}
	if got := SanitizeToolCallID(strings.Repeat("x", 200)); !strings.HasPrefix(got, "call-") {
		t.Fatalf("long id must be hashed, got %q", got)
	}
}

func TestCompileGrepIncludeGlob(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"*.py", "src/app/main.py", true},
		{"src/**/*.py", "src/app/main.py", true},
		{"/*.py", "top.py", true},
		{"/*.py", "src/app/main.py", false},
		{"*.{go,md}", "a/README.md", true},
	}
	for _, tt := range tests {
		m, err := CompileGrepIncludeGlob(tt.pattern)
		if err != nil {
			t.Fatalf("compile %q: %v", tt.pattern, err)
		}
		if got := m(tt.path); got != tt.want {
			t.Errorf("%q matches %q = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
	if _, err := CompileGrepIncludeGlob("../*.py"); err == nil {
		t.Fatal("traversal pattern accepted")
	}
}

func TestGrepMatchesFromFilesMaxCount(t *testing.T) {
	files := map[string]FileData{"/a.txt": {Content: "x\nx\nx"}}
	res, _ := GrepMatchesFromFiles(files, "x", GrepOptions{MaxCount: 2})
	if len(res.Matches) != 2 || !res.Truncated {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestGlobSearchFiles(t *testing.T) {
	files := map[string]FileData{"/src/main.go": {}, "/README.md": {}}
	res, err := GlobSearchFiles(files, "**/*.go", "/")
	if err != nil || len(res.Matches) != 1 || res.Matches[0].Path != "/src/main.go" {
		t.Fatalf("unexpected result: %+v %v", res, err)
	}
}

func TestFormatGrepMatches(t *testing.T) {
	matches := []GrepMatch{{Path: "/b", Line: 2, Text: "hit"}, {Path: "/a", Line: 1, Text: "hit"}, {Path: "/a", Line: 5, Text: "hit"}}
	tests := map[GrepOutputMode]string{
		GrepFilesWithMatches: "/a\n/b",
		GrepCount:            "/a: 2\n/b: 1",
		GrepContent:          "/a:\n  1: hit\n  5: hit\n/b:\n  2: hit",
	}
	for mode, want := range tests {
		if got, _ := FormatGrepMatches(matches, mode); got != want {
			t.Errorf("mode %s: got %q, want %q", mode, got, want)
		}
	}
	if got, _ := FormatGrepMatches(nil, GrepContent); got != "No matches found" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestFormatGrepMatchesWithContext(t *testing.T) {
	matches := []GrepMatch{
		{Path: "/a", Line: 2, Text: "hit", ContextBefore: []ContextLine{{1, "pre"}}},
		{Path: "/a", Line: 9, Text: "hit"},
	}
	got, _ := FormatGrepMatches(matches, GrepContent)
	want := "/a:\n  1- pre\n  2: hit\n  --\n  9: hit"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRegexLiteralHint(t *testing.T) {
	if RegexLiteralHint("foo|bar") == "" || RegexLiteralHint("self.tools") != "" {
		t.Fatal("RegexLiteralHint misclassified pattern")
	}
}
