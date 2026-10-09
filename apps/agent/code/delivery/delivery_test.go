package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// workspaceClone returns a bare remote and a clone of it, set up like a
// workspace (/.quill excluded).
func workspaceClone(t *testing.T) (remote, dir string) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "a.txt"), "one\ntwo\n")
	write(t, filepath.Join(src, "b.txt"), "bye\n")
	write(t, filepath.Join(src, "c.txt"), strings.Repeat("stable line\n", 20))
	write(t, filepath.Join(src, "img.bin"), "\x00\x01\x02")
	git(t, src, "init", "-q", "-b", "main")
	git(t, src, "add", "-A")
	git(t, src, "commit", "-q", "-m", "init")
	remote = filepath.Join(root, "remote.git")
	git(t, root, "clone", "-q", "--bare", src, remote)
	dir = filepath.Join(root, "ws")
	git(t, root, "clone", "-q", "file://"+remote, dir)
	write(t, filepath.Join(dir, ".git", "info", "exclude"), "/.quill/\n")
	return "file://" + remote, dir
}

func TestWorkingDiffIncludesUntrackedFilesAndLeavesTheIndexAlone(t *testing.T) {
	_, dir := workspaceClone(t)
	write(t, filepath.Join(dir, "a.txt"), "one\n2\nthree\n")
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "c.txt"), filepath.Join(dir, "moved.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "new.txt"), "fresh\n")
	write(t, filepath.Join(dir, "img.bin"), "\x00\x09")
	write(t, filepath.Join(dir, ".quill", "artifact.txt"), "agent scratch\n")

	g := HostGit{Dir: dir}
	ctx := context.Background()
	base, err := UpstreamBase(ctx, g, "")
	if err != nil {
		t.Fatal(err)
	}
	d, err := WorkingDiff(ctx, g, base)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]FileChange{}
	for _, f := range d.Files {
		got[f.Path] = f
	}
	want := map[string]FileChange{
		"a.txt":     {Path: "a.txt", Status: StatusModified, Additions: 2, Deletions: 1},
		"b.txt":     {Path: "b.txt", Status: StatusDeleted, Deletions: 1},
		"moved.txt": {Path: "moved.txt", OldPath: "c.txt", Status: StatusRenamed},
		"new.txt":   {Path: "new.txt", Status: StatusAdded, Additions: 1},
		"img.bin":   {Path: "img.bin", Status: StatusModified, Binary: true},
	}
	if len(got) != len(want) {
		t.Fatalf("files = %+v", d.Files)
	}
	for path, w := range want {
		if got[path] != w {
			t.Errorf("%s = %+v, want %+v", path, got[path], w)
		}
	}
	if !strings.Contains(d.Patch, "+fresh") || strings.Contains(d.Patch, "agent scratch") {
		t.Fatalf("patch = %s", d.Patch)
	}
	if staged := git(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("the real index changed: %q", staged)
	}
	if status := git(t, dir, "status", "--porcelain", "--", "new.txt"); status != "?? new.txt" {
		t.Fatalf("new.txt status = %q, want untracked", status)
	}
}

func deliver(t *testing.T, remote, dir, branch string) *Commit {
	t.Helper()
	ctx := context.Background()
	g := HostGit{Dir: dir}
	c, err := CommitAll(ctx, g, branch, "", "quill: change", GitHubAuthor("alice", "Alice"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(ctx, g, dir, c); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	bundle := filepath.Join(tmp, "b.bundle")
	if err := ReadBundle(dir, bundle); err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, tmp, bundle, remote, "", c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCommitAndPushToTheRemote(t *testing.T) {
	remote, dir := workspaceClone(t)
	write(t, filepath.Join(dir, "a.txt"), "changed\n")
	write(t, filepath.Join(dir, "new.txt"), "fresh\n")
	branch := BranchName("t/1 x")
	if branch != "quill/t-1-x" {
		t.Fatalf("branch = %q", branch)
	}
	c := deliver(t, remote, dir, branch)
	if !c.Created || c.Head == c.Base {
		t.Fatalf("commit = %+v", c)
	}
	bare := strings.TrimPrefix(remote, "file://")
	if got := git(t, bare, "rev-parse", "refs/heads/"+branch); got != c.Head {
		t.Fatalf("remote %s = %s, want %s", branch, got, c.Head)
	}
	if got := git(t, bare, "log", "-1", "--format=%an <%ae>|%s", branch); got != "Alice <alice@users.noreply.github.com>|quill: change" {
		t.Fatalf("remote commit = %q", got)
	}
	if got := git(t, bare, "show", branch+":new.txt"); got != "fresh" {
		t.Fatalf("new.txt on the remote = %q", got)
	}

	write(t, filepath.Join(dir, "more.txt"), "more\n")
	second := deliver(t, remote, dir, branch)
	if second.Base != c.Base || git(t, bare, "rev-parse", branch+"~1") != c.Head {
		t.Fatalf("second delivery = %+v, not on top of %s", second, c.Head)
	}

	again, err := CommitAll(context.Background(), HostGit{Dir: dir}, branch, "", "noop", GitHubAuthor("alice", ""))
	if err != nil || again.Created || again.Head != second.Head {
		t.Fatalf("clean re-delivery = %+v, %v", again, err)
	}
}

func TestCommitAllWithoutChanges(t *testing.T) {
	_, dir := workspaceClone(t)
	_, err := CommitAll(context.Background(), HostGit{Dir: dir}, "quill/t", "", "m", GitHubAuthor("a", ""))
	if !errors.Is(err, ErrNothingToDeliver) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadBundleRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, secret, "server secret")
	if err := os.MkdirAll(filepath.Join(dir, ".quill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, bundlePath)); err != nil {
		t.Fatal(err)
	}
	if err := ReadBundle(dir, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("a symlinked bundle was read")
	}
	other := t.TempDir()
	if err := os.Symlink(filepath.Dir(secret), filepath.Join(other, ".quill")); err != nil {
		t.Fatal(err)
	}
	if err := ReadBundle(other, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("a symlinked .quill directory was followed")
	}
}

func TestParseNumstatRenames(t *testing.T) {
	counts, err := parseNumstat([]byte("1\t2\ta.txt\x00-\t-\timg.bin\x003\t0\t\x00old.txt\x00new.txt\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if counts["a.txt"] != (lineCounts{1, 2, false}) || !counts["img.bin"].binary || counts["new.txt"].add != 3 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestParseGitHubURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/acme/app":      "acme/app",
		"https://github.com/acme/app.git":  "acme/app",
		"https://GitHub.com/acme/app/":     "acme/app",
		"https://gitlab.com/acme/app":      "",
		"http://github.com/acme/app":       "",
		"https://github.com/acme":          "",
		"https://github.com/acme/app/tree": "",
	} {
		owner, repo, err := ParseGitHubURL(raw)
		if want == "" {
			if !errors.Is(err, ErrNotGitHub) {
				t.Errorf("%s: err = %v, want ErrNotGitHub", raw, err)
			}
			continue
		}
		if err != nil || owner+"/"+repo != want {
			t.Errorf("%s = %s/%s, %v", raw, owner, repo, err)
		}
	}
}

func TestCreatePullRequest(t *testing.T) {
	var created map[string]any
	existing := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/repos/acme/app/pulls":
			if existing {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:quill/t1."}]}`))
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/acme/app/pull/7","title":"T","state":"open","draft":true}`))
		case r.Method == "GET" && r.URL.Path == "/repos/acme/app/pulls" && r.URL.Query().Get("head") == "acme:quill/t1":
			_, _ = w.Write([]byte(`[{"number":5,"html_url":"https://github.com/acme/app/pull/5","title":"old","state":"open"}]`))
		case r.URL.Path == "/repos/acme/locked/pulls":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	gh := &GitHub{APIURL: srv.URL}
	ctx := context.Background()
	req := NewPullRequest{Owner: "acme", Repo: "app", Title: "T", Body: "B", Head: "quill/t1", Base: "main", Draft: true}

	pr, err := gh.CreatePullRequest(ctx, "tok", req)
	if err != nil || pr.Number != 7 || pr.Existing || !pr.Draft {
		t.Fatalf("created = %+v, %v", pr, err)
	}
	if created["head"] != "quill/t1" || created["base"] != "main" || created["draft"] != true {
		t.Fatalf("request body = %v", created)
	}

	existing = true
	pr, err = gh.CreatePullRequest(ctx, "tok", req)
	if err != nil || pr.Number != 5 || !pr.Existing {
		t.Fatalf("existing = %+v, %v", pr, err)
	}

	locked := req
	locked.Repo = "locked"
	if _, err := gh.CreatePullRequest(ctx, "tok", locked); !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("forbidden err = %v", err)
	}
	if _, err := gh.CreatePullRequest(ctx, "wrong", req); !errors.Is(err, ErrPermission) {
		t.Fatalf("bad token err = %v", err)
	}
}
