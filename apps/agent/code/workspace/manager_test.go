package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeContainer struct {
	image   string
	hostDir string
	running bool
}

type fakeDocker struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	builds     []string
	execs      []string
	failExec   bool
}

func newFakeDocker() *fakeDocker { return &fakeDocker{containers: map[string]*fakeContainer{}} }

func (d *fakeDocker) Build(_ context.Context, dir, dockerfile, tag string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.builds = append(d.builds, dockerfile+"->"+tag)
	return nil
}

func (d *fakeDocker) Create(_ context.Context, spec ContainerSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.containers[spec.Name]; ok {
		return errors.New("name in use")
	}
	d.containers[spec.Name] = &fakeContainer{image: spec.Image, hostDir: spec.HostDir, running: true}
	return nil
}

func (d *fakeDocker) Start(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.containers[name]
	if !ok {
		return errors.New("no such container")
	}
	c.running = true
	return nil
}

func (d *fakeDocker) Stop(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.containers[name]; ok {
		c.running = false
	}
	return nil
}

func (d *fakeDocker) Remove(_ context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.containers, name)
	return nil
}

func (d *fakeDocker) Inspect(_ context.Context, name string) (bool, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.containers[name]
	if !ok {
		return false, false, nil
	}
	return true, c.running, nil
}

func (d *fakeDocker) Exec(_ context.Context, name, _ string, cmd ...string) (int, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.execs = append(d.execs, name+": "+strings.Join(cmd, " "))
	if d.failExec {
		return 1, "boom", nil
	}
	return 0, "", nil
}

func (d *fakeDocker) Ping(context.Context) error { return nil }

func (d *fakeDocker) Output(ctx context.Context, name, workdir string, cmd ...string) ([]byte, error) {
	code, out, err := d.Exec(ctx, name, workdir, cmd...)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d", code)
	}
	return []byte(out), err
}

func (d *fakeDocker) running(name string) bool {
	_, r, _ := d.Inspect(context.Background(), name)
	return r
}

// gitRepo creates a repository with one commit and the given files.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newManager(t *testing.T, d Docker, c *clock) *Manager {
	t.Helper()
	m, err := NewManager(Config{
		Root: t.TempDir(), Store: NewMemoryStore(), Docker: d,
		AllowLocalRepos: true, Now: c.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCreateClonesAndPicksDefaultImage(t *testing.T) {
	repo := gitRepo(t, map[string]string{"main.go": "package main\n"})
	m := newManager(t, newFakeDocker(), &clock{t: time.Now()})
	ws, err := m.Create(context.Background(), "alice", Spec{RepoURL: repo, BaseBranch: "main"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Image != DefaultImage || ws.State != StateStopped || ws.Owner != "alice" {
		t.Fatalf("workspace = %+v", ws)
	}
	if _, err := os.Stat(filepath.Join(ws.Dir, "main.go")); err != nil {
		t.Fatalf("clone missing main.go: %v", err)
	}
	exclude, _ := os.ReadFile(filepath.Join(ws.Dir, ".git", "info", "exclude"))
	if !strings.Contains(string(exclude), "/.quill/") {
		t.Fatalf("exclude = %q", exclude)
	}
}

func TestCreateBuildsProjectDockerfile(t *testing.T) {
	repo := gitRepo(t, map[string]string{ProjectDockerfile: "FROM alpine\n"})
	d := newFakeDocker()
	m := newManager(t, d, &clock{t: time.Now()})
	ws, err := m.Create(context.Background(), "", Spec{RepoURL: repo}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "quill-ws-" + ws.ID + ":latest"
	if ws.Image != want || len(d.builds) != 1 || d.builds[0] != ProjectDockerfile+"->"+want {
		t.Fatalf("image %q, builds %v", ws.Image, d.builds)
	}
}

func TestResolveImageReadsDevcontainer(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".devcontainer"), 0o755)
	os.WriteFile(filepath.Join(dir, ".devcontainer", "devcontainer.json"), []byte(`{
  // the dev image
  "image": "mcr.microsoft.com/devcontainers/go:1",
  "features": {},
}`), 0o644)
	src, err := resolveImage(dir, "fallback")
	if err != nil || src.Image != "mcr.microsoft.com/devcontainers/go:1" {
		t.Fatalf("src = %+v, err = %v", src, err)
	}
}

func TestCreateRejectsBadSpecs(t *testing.T) {
	m, err := NewManager(Config{Root: t.TempDir(), Store: NewMemoryStore(), Docker: newFakeDocker()})
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []Spec{
		{},
		{RepoURL: "git@github.com:a/b.git"},
		{RepoURL: "http://github.com/a/b"},
		{RepoURL: "https://user:pw@github.com/a/b"},
		{RepoURL: "/tmp/repo"},
		{RepoURL: "https://github.com/a/b", BaseBranch: "--upload-pack=x"},
		{RepoURL: "https://github.com/a/b", BaseBranch: "a..b"},
	} {
		if _, err := m.Create(context.Background(), "", spec, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("Create(%+v) err = %v, want ErrInvalid", spec, err)
		}
	}
}

func TestAcquireStartsOnceAndSetsUpGit(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	d := newFakeDocker()
	m := newManager(t, d, &clock{t: time.Now()})
	ctx := context.Background()
	ws, _ := m.Create(ctx, "", Spec{RepoURL: repo}, "")
	got, release, err := m.Acquire(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.InUse || got.State != StateRunning || !d.running(ContainerName(ws.ID)) {
		t.Fatalf("after Acquire: %+v", got)
	}
	if len(d.execs) != 2 || !strings.Contains(d.execs[0], "safe.directory") || !strings.Contains(d.execs[1], "update-index") {
		t.Fatalf("execs = %v", d.execs)
	}
	if err := m.Stop(ctx, ws.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("Stop in use err = %v", err)
	}
	if err := m.Delete(ctx, ws.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("Delete in use err = %v", err)
	}
	release()
	release()
	if _, release2, err := m.Acquire(ctx, ws.ID); err != nil {
		t.Fatal(err)
	} else {
		release2()
	}
	if len(d.execs) != 2 {
		t.Fatalf("a running container was set up again: %v", d.execs)
	}
	if cur, _ := m.Get(ctx, ws.ID); cur.InUse {
		t.Fatal("still in use after release")
	}
}

func TestAcquireRestartsAStoppedContainer(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	d := newFakeDocker()
	m := newManager(t, d, &clock{t: time.Now()})
	ctx := context.Background()
	ws, _ := m.Create(ctx, "", Spec{RepoURL: repo}, "")
	_, release, _ := m.Acquire(ctx, ws.ID)
	release()
	if err := m.Stop(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	if cur, _ := m.Get(ctx, ws.ID); cur.State != StateStopped || d.running(ContainerName(ws.ID)) {
		t.Fatalf("after Stop: %+v", cur)
	}
	_, release, err := m.Acquire(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !d.running(ContainerName(ws.ID)) {
		t.Fatal("container not restarted")
	}
}

func TestAcquireReportsSetupFailure(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	d := newFakeDocker()
	d.failExec = true
	m := newManager(t, d, &clock{t: time.Now()})
	ws, _ := m.Create(context.Background(), "", Spec{RepoURL: repo}, "")
	if _, _, err := m.Acquire(context.Background(), ws.ID); err == nil || !strings.Contains(err.Error(), "safe.directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestQuotaCountsOtherRunningWorkspaces(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	d := newFakeDocker()
	m, _ := NewManager(Config{Root: t.TempDir(), Store: NewMemoryStore(), Docker: d, AllowLocalRepos: true, MaxRunning: 2})
	ctx := context.Background()
	var ids []string
	for range 3 {
		ws, err := m.Create(ctx, "bob", Spec{RepoURL: repo}, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ws.ID)
	}
	other, _ := m.Create(ctx, "carol", Spec{RepoURL: repo}, "")
	for _, id := range ids[:2] {
		_, release, err := m.Acquire(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	if _, _, err := m.Acquire(ctx, ids[2]); !errors.Is(err, ErrQuota) {
		t.Fatalf("third workspace err = %v, want ErrQuota", err)
	}
	if _, release, err := m.Acquire(ctx, other.ID); err != nil {
		t.Fatalf("another owner's workspace: %v", err)
	} else {
		release()
	}
	if _, release, err := m.Acquire(ctx, ids[0]); err != nil {
		t.Fatalf("an already running workspace: %v", err)
	} else {
		release()
	}
}

func TestReapStopsIdleAndDeletesExpired(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	d := newFakeDocker()
	c := &clock{t: time.Now()}
	m := newManager(t, d, c)
	ctx := context.Background()
	idle, _ := m.Create(ctx, "", Spec{RepoURL: repo}, "")
	busy, _ := m.Create(ctx, "", Spec{RepoURL: repo}, "")
	_, r1, _ := m.Acquire(ctx, idle.ID)
	r1()
	_, r2, _ := m.Acquire(ctx, busy.ID)

	c.add(DefaultIdleTimeout + time.Minute)
	res, err := m.Reap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stopped) != 1 || res.Stopped[0] != idle.ID || len(res.Deleted) != 0 {
		t.Fatalf("first reap = %+v", res)
	}
	if !d.running(ContainerName(busy.ID)) {
		t.Fatal("reaped a workspace in use")
	}

	r2()
	c.add(DefaultRetention - 30*time.Minute)
	res, err = m.Reap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != idle.ID || len(res.Stopped) != 1 || res.Stopped[0] != busy.ID {
		t.Fatalf("second reap = %+v", res)
	}
	if _, err := os.Stat(idle.Dir); !os.IsNotExist(err) {
		t.Fatalf("workspace files still there: %v", err)
	}
	if _, err := m.Get(ctx, idle.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get deleted err = %v", err)
	}
}

func TestReconcileMarksVanishedContainersStopped(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	d := newFakeDocker()
	m := newManager(t, d, &clock{t: time.Now()})
	ctx := context.Background()
	ws, _ := m.Create(ctx, "", Spec{RepoURL: repo}, "")
	_, release, _ := m.Acquire(ctx, ws.ID)
	release()
	d.Remove(ctx, ContainerName(ws.ID))
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if cur, _ := m.Get(ctx, ws.ID); cur.State != StateStopped {
		t.Fatalf("state = %s", cur.State)
	}
}

func TestAuthEnvKeepsTokenOutOfArgs(t *testing.T) {
	env := AuthEnv("secret")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "secret") || !strings.Contains(joined, "Authorization: Basic ") {
		t.Fatalf("env = %v", env)
	}
}
