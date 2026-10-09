// Package process runs long-lived commands, such as dev servers, for the
// agent. `execute` kills everything a command leaves behind when it
// returns; processes started here keep running, with their output
// buffered, until the agent stops them or the service shuts down. They run
// through the shell backend, so they get the same directory, environment
// and sandbox as `execute`.
package process

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
)

// MaxRunning caps the running processes of one thread.
const MaxRunning = 4

// bufferBytes is how much unread output a process keeps.
const bufferBytes = 64 * 1024

// Preparer builds a command ready to start (backend.LocalShellBackend).
type Preparer interface {
	PrepareCommand(ctx context.Context, command string) (*exec.Cmd, error)
}

// Stopper is a Preparer whose commands outlive the local process, such as
// `docker exec` into a container: StopCommand ends what cmd started there.
type Stopper interface {
	StopCommand(cmd *exec.Cmd)
}

// Manager holds the processes of every thread.
type Manager struct {
	mu      sync.Mutex
	threads map[string]map[string]*Proc
	nextID  map[string]int
	closed  bool
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{threads: map[string]map[string]*Proc{}, nextID: map[string]int{}}
}

// Start runs command for thread in the background.
func (m *Manager) Start(thread string, p Preparer, command string) (*Proc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("process manager is shut down")
	}
	running := 0
	for _, proc := range m.threads[thread] {
		if r, _ := proc.Status(); r {
			running++
		}
	}
	if running >= MaxRunning {
		return nil, fmt.Errorf("%d processes are already running in this thread; stop one with stop_process first", running)
	}
	cmd, err := p.PrepareCommand(context.Background(), command)
	if err != nil {
		return nil, err
	}
	m.nextID[thread]++
	proc := &Proc{ID: fmt.Sprintf("p%d", m.nextID[thread]), Command: command, cmd: cmd, done: make(chan struct{})}
	proc.out.limit = bufferBytes
	if s, ok := p.(Stopper); ok {
		proc.stop = func() { s.StopCommand(cmd) }
	}
	cmd.Stdout, cmd.Stderr = &proc.out, &proc.out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go proc.wait()
	if m.threads[thread] == nil {
		m.threads[thread] = map[string]*Proc{}
	}
	m.threads[thread][proc.ID] = proc
	return proc, nil
}

// Get returns thread's process id.
func (m *Manager) Get(thread, id string) (*Proc, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.threads[thread][id]
	return p, ok
}

// Stop kills thread's process id and everything it started.
func (m *Manager) Stop(thread, id string) error {
	p, ok := m.Get(thread, id)
	if !ok {
		return fmt.Errorf("no process %s in this thread", id)
	}
	p.kill()
	return nil
}

// StopThread kills every process of thread.
func (m *Manager) StopThread(thread string) {
	m.mu.Lock()
	procs := m.threads[thread]
	delete(m.threads, thread)
	m.mu.Unlock()
	for _, p := range procs {
		p.kill()
	}
}

// Close kills every process and refuses new ones.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	threads := m.threads
	m.threads = map[string]map[string]*Proc{}
	m.mu.Unlock()
	for _, procs := range threads {
		for _, p := range procs {
			p.kill()
		}
	}
}

// Proc is one background process.
type Proc struct {
	ID      string
	Command string

	cmd      *exec.Cmd
	stop     func()
	out      buffer
	done     chan struct{}
	exitCode int
}

func (p *Proc) wait() {
	err := p.cmd.Wait()
	// Children the shell left behind would keep running unseen.
	_ = backend.KillProcessGroup(p.cmd)
	p.exitCode = 0
	if err != nil {
		p.exitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			p.exitCode = exitErr.ExitCode()
		}
	}
	close(p.done)
}

func (p *Proc) kill() {
	if p.stop != nil {
		p.stop()
	}
	_ = backend.KillProcessGroup(p.cmd)
	_ = p.cmd.Process.Kill()
	<-p.done
}

// Wait blocks until the process exits or d passes.
func (p *Proc) Wait(d time.Duration) {
	select {
	case <-p.done:
	case <-time.After(d):
	}
}

// Status reports whether the process runs and, once it exited, its exit
// code (-1 when killed by a signal).
func (p *Proc) Status() (running bool, exitCode int) {
	select {
	case <-p.done:
		return false, p.exitCode
	default:
		return true, 0
	}
}

// Read returns the output since the last Read and how many bytes of it
// did not fit in the buffer.
func (p *Proc) Read() (string, int64) { return p.out.read() }

// buffer keeps the last limit bytes of unread output.
type buffer struct {
	mu      sync.Mutex
	data    []byte
	limit   int
	omitted int64
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if over := len(b.data) - b.limit; over > 0 {
		b.omitted += int64(over)
		b.data = append(b.data[:0], b.data[over:]...)
	}
	return len(p), nil
}

func (b *buffer) read() (string, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out, omitted := string(b.data), b.omitted
	b.data, b.omitted = b.data[:0], 0
	return out, omitted
}
