package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// procDir holds, inside the container, one pid file per start_process
// command.
const procDir = "/tmp/quill-proc"

// Each command records its pid, then becomes the user's command.
const startScript = `mkdir -p ` + procDir + ` && echo $$ > "$0" && exec sh -c "$1"`

// Stopped first so it cannot start children while the tree is walked.
const stopScript = `pid=$(cat "$0" 2>/dev/null) || exit 0
kill_tree() {
  kill -STOP "$1" 2>/dev/null
  for c in $(cat /proc/"$1"/task/*/children 2>/dev/null); do kill_tree "$c"; done
  kill -KILL "$1" 2>/dev/null
}
kill_tree "$pid"
rm -f "$0"`

// stopTimeout bounds the docker exec that kills a process.
const stopTimeout = 30 * time.Second

// ProcessShell starts start_process commands in a workspace container
// (process.Preparer and process.Stopper). Killing the local `docker exec`
// client leaves the command running in the container, so each command
// records its pid there and StopCommand kills its process tree.
type ProcessShell struct {
	docker    Docker
	binary    string
	container string

	mu   sync.Mutex
	next int
	pids map[*exec.Cmd]string
}

// Processes returns the start_process shell of ws.
func (m *Manager) Processes(ws *Workspace) *ProcessShell {
	return &ProcessShell{docker: m.cfg.Docker, binary: m.DockerBinary(), container: ContainerName(ws.ID), pids: map[*exec.Cmd]string{}}
}

func (p *ProcessShell) PrepareCommand(_ context.Context, command string) (*exec.Cmd, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	pidFile := fmt.Sprintf("%s/%d-%d", procDir, time.Now().UnixNano(), p.next)
	cmd := exec.Command(p.binary, "exec", "-w", ContainerDir, p.container, "sh", "-c", startScript, pidFile, command)
	p.pids[cmd] = pidFile
	return cmd, nil
}

func (p *ProcessShell) StopCommand(cmd *exec.Cmd) {
	p.mu.Lock()
	pidFile, ok := p.pids[cmd]
	delete(p.pids, cmd)
	p.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	_, _, _ = p.docker.Exec(ctx, p.container, "", "sh", "-c", stopScript, pidFile)
}
