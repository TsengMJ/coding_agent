package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDockerBinary writes a "docker" that runs the exec'd command on the
// host after checking the container and workdir it was given.
func fakeDockerBinary(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
[ "$1" = exec ] || { echo "unexpected $1" >&2; exit 99; }
shift
while [ $# -gt 0 ]; do
  case "$1" in
    -w) cd "$2" || exit 98; shift 2 ;;
    -e) export "$2"; shift 2 ;;
    *) break ;;
  esac
done
[ "$1" = box ] || { echo "wrong container $1" >&2; exit 97; }
shift
exec "$@"
`
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newDockerExec(t *testing.T) (*DockerExecBackend, string) {
	root := t.TempDir()
	return NewDockerExecBackend(DockerExecOptions{
		FilesystemOptions: FilesystemOptions{RootDir: root, VirtualMode: true},
		Container:         "box",
		Workdir:           root,
		Binary:            fakeDockerBinary(t),
		Env:               map[string]string{"GREETING": "hi"},
	}), root
}

func TestDockerExecRunsInWorkdirWithEnv(t *testing.T) {
	b, root := newDockerExec(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := b.Execute(context.Background(), "ls; echo $GREETING; echo oops >&2; exit 3", nil)
	if *res.ExitCode != 3 {
		t.Fatalf("exit %d: %s", *res.ExitCode, res.Output)
	}
	for _, want := range []string{"f.txt", "hi", "[stderr] oops", "Exit code: 3"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output %q lacks %q", res.Output, want)
		}
	}
}

func TestDockerExecSharesFilesWithFileTools(t *testing.T) {
	b, _ := newDockerExec(t)
	if r := b.Write(context.Background(), "/a.txt", "from tool"); r.Error != "" {
		t.Fatal(r.Error)
	}
	res := b.Execute(context.Background(), "cat a.txt", nil)
	if strings.TrimSpace(res.Output) != "from tool" {
		t.Fatalf("output %q", res.Output)
	}
}

func TestDockerExecTimeout(t *testing.T) {
	old := dockerExecGrace
	dockerExecGrace = 200 * time.Millisecond
	defer func() { dockerExecGrace = old }()
	b, _ := newDockerExec(t)
	d := time.Second
	res := b.Execute(context.Background(), "sleep 10", &d)
	if *res.ExitCode != timeoutExitCode || !strings.Contains(res.Output, "timed out after 1s") {
		t.Fatalf("exit %d: %s", *res.ExitCode, res.Output)
	}
}

func TestDockerExecCapsOutput(t *testing.T) {
	b, _ := newDockerExec(t)
	b.opts.MaxOutputBytes = 100
	res := b.Execute(context.Background(), "yes | head -c 5000", nil)
	if !res.Truncated || !strings.Contains(res.Output, "truncated at 100 bytes") {
		t.Fatalf("output %q", res.Output)
	}
}

func TestDockerExecRejectsEmptyCommand(t *testing.T) {
	b, _ := newDockerExec(t)
	if res := b.Execute(context.Background(), "  ", nil); *res.ExitCode != 1 {
		t.Fatalf("exit %d", *res.ExitCode)
	}
}
