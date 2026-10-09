package workspace

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/process"
)

// TestDockerWorkspace runs a real container: QUILL_DOCKER_IT=1, and
// QUILL_DOCKER_IT_IMAGE names an image with git and sh (default alpine/git).
func TestDockerWorkspace(t *testing.T) {
	if os.Getenv("QUILL_DOCKER_IT") != "1" {
		t.Skip("set QUILL_DOCKER_IT=1 to run against a real docker daemon")
	}
	image := os.Getenv("QUILL_DOCKER_IT_IMAGE")
	if image == "" {
		image = "alpine/git:latest"
	}
	t.Setenv("QUILL_SECRET_FOR_TEST", "do-not-leak")
	repo := gitRepo(t, map[string]string{"hello.txt": "hi\n"})
	// Docker Desktop shares the home directory but not every temp dir.
	root, err := os.MkdirTemp(os.Getenv("HOME"), ".quill-ws-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	m, err := NewManager(Config{Root: root, Store: NewMemoryStore(), DefaultImage: image, AllowLocalRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a, err := m.Create(ctx, "it", Spec{RepoURL: repo}, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(ctx, "it", Spec{RepoURL: repo}, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Delete(context.Background(), a.ID); m.Delete(context.Background(), b.ID) })

	wsA, releaseA, err := m.Acquire(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	wsB, releaseB, err := m.Acquire(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()

	ba, bb := m.Backend(wsA), m.Backend(wsB)
	if res := ba.Execute(ctx, "echo changed > hello.txt && git status --porcelain", nil); res.ExitCode == nil || *res.ExitCode != 0 || !strings.Contains(res.Output, "hello.txt") {
		t.Fatalf("exec in A: %+v", res)
	}
	if res := bb.Execute(ctx, "cat hello.txt", nil); strings.TrimSpace(res.Output) != "hi" {
		t.Fatalf("B sees A's change: %q", res.Output)
	}
	if res := ba.Execute(ctx, "env", nil); strings.Contains(res.Output, "do-not-leak") {
		t.Fatalf("container sees the server environment:\n%s", res.Output)
	}
	if body, _ := os.ReadFile(wsA.Dir + "/hello.txt"); strings.TrimSpace(string(body)) != "changed" {
		t.Fatalf("host copy = %q", body)
	}

	procs := process.NewManager()
	defer procs.Close()
	p, err := procs.Start("t1", m.Processes(wsA), "sleep 301 & sleep 302")
	if err != nil {
		t.Fatal(err)
	}
	countSleeps := `for f in /proc/[0-9]*/cmdline; do tr '\0' ' ' < $f; echo; done | grep -c 'sleep 30[12]' || true`
	waitFor(t, func() bool { return strings.TrimSpace(ba.Execute(ctx, countSleeps, nil).Output) == "2" })
	if err := procs.Stop("t1", p.ID); err != nil {
		t.Fatal(err)
	}
	if n := strings.TrimSpace(ba.Execute(ctx, countSleeps, nil).Output); n != "0" {
		t.Fatalf("%s sleep processes outlived stop_process", n)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 50 {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met in 5s")
}
