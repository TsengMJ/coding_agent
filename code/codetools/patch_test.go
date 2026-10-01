package codetools

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePatchOps(t *testing.T) {
	patch := `*** Begin Patch
*** Add File: /docs/new.md
+# Title
+
+body
*** Delete File: old.txt
*** Update File: src/app.go
*** Move to: src/main.go
@@ func main() {
 	a := 1
-	b := 2
+	b := 3
@@
-x
+y
*** End of File
*** End Patch
`
	ops, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileOp{
		{Kind: OpAdd, Path: "/docs/new.md", Content: "# Title\n\nbody\n"},
		{Kind: OpDelete, Path: "old.txt"},
		{Kind: OpUpdate, Path: "src/app.go", MoveTo: "src/main.go", Chunks: []Chunk{
			{Context: "func main() {", Old: []string{"\ta := 1", "\tb := 2"}, New: []string{"\ta := 1", "\tb := 3"}, Line: 9},
			{Old: []string{"x"}, New: []string{"y"}, EOF: true, Line: 13},
		}},
	}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("got  %#v\nwant %#v", ops, want)
	}
}

func TestParsePatchFirstHunkWithoutMarkerAndHeredoc(t *testing.T) {
	patch := "<<'EOF'\n*** Begin Patch\n*** Update File: a.go\n-old\n+new\n*** End Patch\nEOF\n"
	ops, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || len(ops[0].Chunks) != 1 || ops[0].Chunks[0].Old[0] != "old" {
		t.Fatalf("got %#v", ops)
	}
}

func TestParsePatchErrors(t *testing.T) {
	tests := []struct {
		name, patch, want string
	}{
		{"no begin", "*** Update File: a\n-x\n+y\n*** End Patch", "must start with"},
		{"no end", "*** Begin Patch\n*** Update File: a\n-x\n+y", "must end with"},
		{"empty", "*** Begin Patch\n*** End Patch", "no file operations"},
		{"bad header", "*** Begin Patch\n*** Rename File: a\n*** End Patch", "line 2: expected"},
		{"add without plus", "*** Begin Patch\n*** Add File: a\nplain\n*** End Patch", "must start with '+'"},
		{"bad hunk line", "*** Begin Patch\n*** Update File: a\n-x\n?y\n*** End Patch", "line 4: hunk lines must start"},
		{"context only", "*** Begin Patch\n*** Update File: a\n@@\n ctx\n*** End Patch", "no '-' or '+'"},
		{"no hunks", "*** Begin Patch\n*** Update File: a\n*** End Patch", "has no hunks"},
		{"missing path", "*** Begin Patch\n*** Delete File: \n*** End Patch", "missing file path"},
		{"duplicate", "*** Begin Patch\n*** Delete File: a\n*** Add File: a\n+x\n*** End Patch", "more than once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePatch(tt.patch)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func chunk(old, repl []string) Chunk { return Chunk{Old: old, New: repl, Line: 1} }

func TestApplyChunks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		chunks  []Chunk
		want    string
	}{
		{"exact", "a\nb\nc\n", []Chunk{chunk([]string{"a", "b"}, []string{"a", "B"})}, "a\nB\nc\n"},
		{"trailing whitespace ignored", "a  \nb\n", []Chunk{chunk([]string{"a", "b"}, []string{"a", "x"})}, "a\nx\n"},
		{"indentation ignored", "\t\tfoo()\nbar\n", []Chunk{chunk([]string{"foo()"}, []string{"\t\tbaz()"})}, "\t\tbaz()\nbar\n"},
		{"unicode punctuation", "say(\u201chi\u201d) \u2014 ok\n", []Chunk{chunk([]string{`say("hi") - ok`}, []string{`say("bye")`})}, "say(\"bye\")\n"},
		{"two hunks in order", "x\n1\nx\n2\n", []Chunk{
			{Context: "1", Old: []string{"x"}, New: []string{"y"}, Line: 1},
		}, "x\n1\ny\n2\n"},
		{"context anchors repeated lines", "func a() {\n\treturn 1\n}\nfunc b() {\n\treturn 1\n}\n", []Chunk{
			{Context: "func b() {", Old: []string{"\treturn 1"}, New: []string{"\treturn 2"}, Line: 1},
		}, "func a() {\n\treturn 1\n}\nfunc b() {\n\treturn 2\n}\n"},
		{"end of file", "x\nmid\nx\n", []Chunk{{Old: []string{"x"}, New: []string{"z"}, EOF: true, Line: 1}}, "x\nmid\nz\n"},
		{"pure addition appends", "a\n", []Chunk{chunk(nil, []string{"b"})}, "a\nb\n"},
		{"crlf preserved", "a\r\nb\r\n", []Chunk{chunk([]string{"b"}, []string{"c"})}, "a\r\nc\r\n"},
		{"missing trailing newline gets one", "a\nb", []Chunk{chunk([]string{"b"}, []string{"c"})}, "a\nc\n"},
		{"trailing empty context line", "a\nb\n", []Chunk{chunk([]string{"b", ""}, []string{"c", ""})}, "a\nc\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyChunks(tt.content, tt.chunks)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyChunksErrors(t *testing.T) {
	if _, err := applyChunks("a\nb\n", []Chunk{chunk([]string{"zzz"}, []string{"y"})}); err == nil || !strings.Contains(err.Error(), "could not find") || !strings.Contains(err.Error(), "zzz") {
		t.Errorf("missing lines: %v", err)
	}
	if _, err := applyChunks("x\ny\nx\n", []Chunk{chunk([]string{"x"}, []string{"z"})}); err == nil || !strings.Contains(err.Error(), "match 2 places") {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := applyChunks("a\n", []Chunk{{Context: "nope", Old: []string{"a"}, New: []string{"b"}, Line: 3}}); err == nil || !strings.Contains(err.Error(), "context line") {
		t.Errorf("missing context: %v", err)
	}
}

func TestApplyChunksMismatchHints(t *testing.T) {
	content := "def total(items):\n    subtotal = sum(items)\n    return subtotal\n"
	stale := chunk([]string{"    subtotal = sum(items)", "    return subtotal * 1"}, []string{"x"})
	if _, err := applyChunks(content, []Chunk{stale}); err == nil || !strings.Contains(err.Error(), "first line matches file line 2") {
		t.Errorf("first line matches: %v", err)
	}
	similar := chunk([]string{"    subtotal = sum(prices)"}, []string{"x"})
	if _, err := applyChunks(content, []Chunk{similar}); err == nil || !strings.Contains(err.Error(), "closest file line is 2") {
		t.Errorf("closest line: %v", err)
	}
	unrelated := chunk([]string{"import os"}, []string{"x"})
	if _, err := applyChunks(content, []Chunk{unrelated}); err == nil || !strings.Contains(err.Error(), "No similar line") {
		t.Errorf("no similar line: %v", err)
	}
}

func TestParsePatchContextOnlyHunkExplainsPrefixes(t *testing.T) {
	_, err := ParsePatch("*** Begin Patch\n*** Update File: a\n@@\n ctx\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "' ' (unchanged context), '-' (remove) or '+' (add)") || !strings.Contains(err.Error(), "+    return round(subtotal, 2)") {
		t.Errorf("err = %v", err)
	}
}

func TestSeekSequencePrefersStrictestLevel(t *testing.T) {
	lines := []string{"  foo", "foo"}
	if idx, n := seekSequence(lines, []string{"foo"}, 0, false); idx != 1 || n != 1 {
		t.Fatalf("idx=%d n=%d", idx, n)
	}
}
