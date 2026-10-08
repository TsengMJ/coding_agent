package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseTestListFormats(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want []string
	}{
		"json array":          {`["a", "b"]`, []string{"a", "b"}},
		"json string of json": {`"[\"a | x\", \"b\"]"`, []string{"a | x", "b"}},
		"python literal":      {`"['a::t[1]', \"it's\"]"`, []string{"a::t[1]", "it's"}},
		"python escapes":      {`"['a\\\\b', 'c\\'d', 'e\\x41']"`, []string{`a\b`, "c'd", "eA"}},
		"empty python list":   {`"[]"`, nil},
		"null":                {`null`, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseTestList(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseTestListRejectsGarbage(t *testing.T) {
	for _, raw := range []string{`"not a list"`, `"['unterminated]"`, `"['a' 'b']"`} {
		if _, err := ParseTestList(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: expected error", raw)
		}
	}
}

const samplePatch = `diff --git a/src/a.js b/src/a.js
index 1..2 100644
--- a/src/a.js
+++ b/src/a.js
@@ -1,3 +1,4 @@
 keep
-old
+new
+added
diff --git a/test/a.js b/test/a.js
--- a/test/a.js
+++ b/test/a.js
@@ -1 +1 @@
-x
+y
`

func TestParsePatch(t *testing.T) {
	st := ParsePatch(samplePatch)
	if !slices.Equal(st.Files, []string{"src/a.js", "test/a.js"}) {
		t.Fatalf("files %q", st.Files)
	}
	if st.Added != 3 || st.Removed != 2 {
		t.Fatalf("added %d removed %d", st.Added, st.Removed)
	}
	if !slices.Equal(st.TestFiles, []string{"test/a.js"}) {
		t.Fatalf("test files %q", st.TestFiles)
	}
}

func TestIsTestPathMatchesVerifier(t *testing.T) {
	yes := []string{"test/user.js", "pkg/foo_test.go", "lib/test_x.py", "conftest.py", "src/a.spec.ts",
		"a/__tests__/b.js", "pkg/testdata/x.json", "src/__snapshots__/a.snap"}
	no := []string{"src/user.js", "pkg/foo.go", "lib/testing.py", "src/contest.py"}
	for _, p := range yes {
		if !IsTestPath(p) {
			t.Errorf("%s should be a test path", p)
		}
	}
	for _, p := range no {
		if IsTestPath(p) {
			t.Errorf("%s should not be a test path", p)
		}
	}
}

func TestFileOverlap(t *testing.T) {
	p, r, ok := FileOverlap([]string{"a.go", "b.go", "a_test.go"}, []string{"a.go", "c.go"})
	if !ok || p != 0.5 || r != 0.5 {
		t.Fatalf("p=%v r=%v ok=%v", p, r, ok)
	}
	if _, _, ok := FileOverlap([]string{"x_test.go"}, []string{"a.go"}); ok {
		t.Fatal("test-only prediction should be undefined")
	}
}

// writeTask creates a minimal Harbor task directory.
func writeTask(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, "tasks", id)
	files := map[string]string{
		"instruction.md":           "fix it",
		"tests/config.json":        `{"fail_to_pass": "['t1']", "pass_to_pass": ["t2", "t3"], "selected_test_files_to_run": "[\"f.py\"]"}`,
		"solution/gold_patch.diff": samplePatch,
		"task.toml": `[verifier]
network_mode = "public"
timeout_sec = 3000.0
[agent]
network_mode = "no-network"
timeout_sec = 3000.0
[environment]
docker_image = "ghcr.io/x:` + id + `"
cpus = 1
memory_mb = 4096
`,
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadTasksMergesMetadata(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "instance_b")
	writeTask(t, root, "instance_a")
	meta := map[string]Metadata{
		"instance_a": {Repo: "o/a", Language: "go", Hard: true, IssueCategories: []string{"x"}, Image: "ghcr.io/x:instance_a"},
		"instance_b": {Repo: "o/b", Language: "ts"},
	}
	tasks, err := LoadTasks(root, meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].InstanceID != "instance_a" {
		t.Fatalf("tasks %+v", tasks)
	}
	a := tasks[0]
	if a.Repo != "o/a" || !a.Hard || a.Image != "ghcr.io/x:instance_a" || a.MemoryMB != 4096 || a.AgentTimeout != 3000 {
		t.Fatalf("task a %+v", a)
	}
	if !slices.Equal(a.FailToPass, []string{"t1"}) || len(a.PassToPass) != 2 || !slices.Equal(a.SelectedTests, []string{"f.py"}) {
		t.Fatalf("tests %+v", a)
	}
	if !slices.Equal(a.GoldPaths, []string{"src/a.js", "test/a.js"}) || a.GoldAdded != 3 {
		t.Fatalf("gold %+v", a)
	}
	if !a.VerifierOnline || tasks[1].Bucket() != LangJS {
		t.Fatalf("verifier online %v bucket %s", a.VerifierOnline, tasks[1].Bucket())
	}
}

func TestLoadTasksRequiresMetadata(t *testing.T) {
	root := t.TempDir()
	writeTask(t, root, "instance_a")
	if _, err := LoadTasks(root, map[string]Metadata{}); err == nil || !strings.Contains(err.Error(), "no metadata") {
		t.Fatalf("err %v", err)
	}
}

func TestVerifyChecksums(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sum+"  a.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := VerifyChecksums(dir); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyChecksums(dir); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestHFMetadataPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		var rows []string
		if offset == "0" {
			for i := range 100 {
				rows = append(rows, fmt.Sprintf(`{"row":{"instance_id":"i%d","repo":"o/r","repo_language":"go","hard":false,"issue_categories":"[\"a\"]","issue_specificity":"[]","docker_image":"img"}}`, i))
			}
		} else {
			rows = append(rows, `{"row":{"instance_id":"last","repo":"o/r","repo_language":"python","hard":true,"issue_categories":"[\"b\",\"c\"]","issue_specificity":"[\"s\"]","docker_image":"img"}}`)
		}
		fmt.Fprintf(w, `{"rows":[%s],"num_rows_total":101}`, strings.Join(rows, ","))
	}))
	defer srv.Close()
	meta, err := HFClient{RowsURL: srv.URL}.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 101 {
		t.Fatalf("got %d rows", len(meta))
	}
	last := meta["last"]
	if !last.Hard || last.Language != "python" || !slices.Equal(last.IssueCategories, []string{"b", "c"}) {
		t.Fatalf("last %+v", last)
	}
}
