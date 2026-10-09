package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessShellRunsInTheContainerAndStopsByPidFile(t *testing.T) {
	d := newFakeDocker()
	m, err := NewManager(Config{Root: t.TempDir(), Store: NewMemoryStore(), Docker: d})
	if err != nil {
		t.Fatal(err)
	}
	p := m.Processes(&Workspace{ID: "abc"})
	cmd, err := p.PrepareCommand(context.Background(), "npm run dev")
	if err != nil {
		t.Fatal(err)
	}
	args := cmd.Args
	if args[1] != "exec" || args[4] != ContainerName("abc") || args[len(args)-1] != "npm run dev" {
		t.Fatalf("command = %q", args)
	}
	pidFile := args[len(args)-2]
	if !strings.HasPrefix(pidFile, procDir+"/") {
		t.Fatalf("pid file = %q", pidFile)
	}

	p.StopCommand(cmd)
	p.StopCommand(cmd)
	if len(d.execs) != 1 || !strings.HasSuffix(d.execs[0], pidFile) || !strings.Contains(d.execs[0], "kill_tree") {
		t.Fatalf("stop execs = %q", d.execs)
	}
}

func TestStartScriptRecordsThePidOfTheCommand(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "p1")
	out, err := exec.Command("sh", "-c", startScript, pidFile, `echo "pid $$"`).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	body, _ := os.ReadFile(pidFile)
	if want := "pid " + strings.TrimSpace(string(body)); strings.TrimSpace(string(out)) != want {
		t.Fatalf("command printed %q, pid file says %q", out, body)
	}
}
