package workspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
)

// Defaults for Config.
const (
	DefaultIdleTimeout = 30 * time.Minute
	DefaultRetention   = 14 * 24 * time.Hour
	DefaultMaxRunning  = 3
	reapInterval       = time.Minute
	setupTimeout       = 2 * time.Minute
)

// CloneFunc clones spec into dir. token, when set, authenticates HTTPS.
type CloneFunc func(ctx context.Context, spec Spec, dir, token string) error

// Config configures a Manager.
type Config struct {
	// Root holds one directory per workspace; required.
	Root  string
	Store Store
	// Docker defaults to CLIDocker.
	Docker Docker
	// Clone defaults to GitClone.
	Clone CloneFunc
	// DefaultImage runs repositories that name no image (DefaultImage).
	DefaultImage string
	// IdleTimeout stops a container unused for this long.
	IdleTimeout time.Duration
	// Retention deletes a workspace unused for this long.
	Retention time.Duration
	// MaxRunning caps each owner's running containers.
	MaxRunning int
	// AllowLocalRepos accepts absolute paths and file:// URLs (tests and
	// single-user setups); otherwise only https:// is allowed.
	AllowLocalRepos bool
	// CPUs and MemoryMB limit each container; zero is unlimited.
	CPUs     float64
	MemoryMB int
	// Network is the docker network containers join; empty is the default.
	Network string
	// Now defaults to time.Now.
	Now func() time.Time
}

// Manager creates, runs and reaps workspaces.
type Manager struct {
	cfg Config

	mu    sync.Mutex
	inUse map[string]int
	// locks serializes start / stop / delete per workspace.
	locks map[string]*sync.Mutex
}

// NewManager validates cfg and fills in defaults.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Root == "" || cfg.Store == nil {
		return nil, errors.New("workspace.NewManager: Root and Store are required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("workspace: create root: %w", err)
	}
	cfg.Root = root
	if cfg.Docker == nil {
		cfg.Docker = CLIDocker{}
	}
	if cfg.Clone == nil {
		cfg.Clone = GitClone
	}
	if cfg.DefaultImage == "" {
		cfg.DefaultImage = DefaultImage
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.Retention <= 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.MaxRunning <= 0 {
		cfg.MaxRunning = DefaultMaxRunning
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{cfg: cfg, inUse: map[string]int{}, locks: map[string]*sync.Mutex{}}, nil
}

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func (m *Manager) validate(spec *Spec) error {
	spec.RepoURL = strings.TrimSpace(spec.RepoURL)
	spec.BaseBranch = strings.TrimSpace(spec.BaseBranch)
	if spec.RepoURL == "" {
		return fmt.Errorf("%w: repo_url is required", ErrInvalid)
	}
	u, err := url.Parse(spec.RepoURL)
	switch {
	case err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil:
	case m.cfg.AllowLocalRepos && (filepath.IsAbs(spec.RepoURL) || (err == nil && u.Scheme == "file")):
	default:
		return fmt.Errorf("%w: repo_url must be an https:// URL without credentials, got %q", ErrInvalid, spec.RepoURL)
	}
	if spec.BaseBranch != "" && (!branchPattern.MatchString(spec.BaseBranch) ||
		strings.HasPrefix(spec.BaseBranch, "-") || strings.Contains(spec.BaseBranch, "..")) {
		return fmt.Errorf("%w: base_branch %q is not a valid branch name", ErrInvalid, spec.BaseBranch)
	}
	return nil
}

// Create clones spec for owner and picks the image. The container starts
// on the first Acquire.
func (m *Manager) Create(ctx context.Context, owner string, spec Spec, token string) (*Workspace, error) {
	if err := m.validate(&spec); err != nil {
		return nil, err
	}
	id := NewID()
	dir := filepath.Join(m.cfg.Root, id)
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := m.cfg.Clone(ctx, spec, dir, token); err != nil {
		cleanup()
		return nil, fmt.Errorf("clone %s: %w", spec.RepoURL, err)
	}
	if err := excludeArtifacts(dir); err != nil {
		cleanup()
		return nil, err
	}
	image, err := prepareImage(ctx, m.cfg.Docker, dir, id, m.cfg.DefaultImage)
	if err != nil {
		cleanup()
		return nil, err
	}
	now := m.cfg.Now()
	ws := Workspace{ID: id, Owner: owner, Spec: spec, Image: image, Dir: dir, State: StateStopped, CreatedAt: now, LastUsedAt: now}
	if err := m.cfg.Store.Put(ctx, ws); err != nil {
		cleanup()
		return nil, err
	}
	return &ws, nil
}

// Ping fails when the Docker daemon cannot be reached.
func (m *Manager) Ping(ctx context.Context) error { return m.cfg.Docker.Ping(ctx) }

// Get returns a workspace with its in-use status.
func (m *Manager) Get(ctx context.Context, id string) (*Workspace, error) {
	ws, err := m.cfg.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	ws.InUse = m.used(id)
	return ws, nil
}

// List returns owner's workspaces (every workspace for an empty owner).
func (m *Manager) List(ctx context.Context, owner string) ([]Workspace, error) {
	list, err := m.cfg.Store.List(ctx, owner)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].InUse = m.used(list[i].ID)
	}
	return list, nil
}

func (m *Manager) used(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inUse[id] > 0
}

func (m *Manager) lock(id string) func() {
	m.mu.Lock()
	l, ok := m.locks[id]
	if !ok {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// Acquire starts the workspace's container if needed and marks it in use
// until release is called; the reaper leaves it alone meanwhile.
func (m *Manager) Acquire(ctx context.Context, id string) (*Workspace, func(), error) {
	unlock := m.lock(id)
	defer unlock()
	ws, err := m.cfg.Store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if err := m.ensureRunning(ctx, ws); err != nil {
		return nil, nil, err
	}
	ws.LastUsedAt = m.cfg.Now()
	if err := m.cfg.Store.Put(ctx, *ws); err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	m.inUse[id]++
	m.mu.Unlock()
	ws.InUse = true
	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			m.inUse[id]--
			if m.inUse[id] <= 0 {
				delete(m.inUse, id)
			}
			m.mu.Unlock()
			unlock := m.lock(id)
			defer unlock()
			ctx := context.WithoutCancel(ctx)
			if cur, err := m.cfg.Store.Get(ctx, id); err == nil {
				cur.LastUsedAt = m.cfg.Now()
				_ = m.cfg.Store.Put(ctx, *cur)
			}
		})
	}
	return ws, release, nil
}

func (m *Manager) ensureRunning(ctx context.Context, ws *Workspace) error {
	name := ContainerName(ws.ID)
	exists, running, err := m.cfg.Docker.Inspect(ctx, name)
	if err != nil {
		return err
	}
	if running {
		if ws.State != StateRunning {
			ws.State = StateRunning
		}
		return nil
	}
	if err := m.checkQuota(ctx, ws); err != nil {
		return err
	}
	if exists {
		err = m.cfg.Docker.Start(ctx, name)
	} else {
		err = m.cfg.Docker.Create(ctx, ContainerSpec{
			Name: name, Image: ws.Image, HostDir: ws.Dir,
			Labels: map[string]string{"quill.workspace": ws.ID, "quill.owner": ws.Owner},
			CPUs:   m.cfg.CPUs, MemoryMB: m.cfg.MemoryMB, Network: m.cfg.Network,
		})
	}
	if err != nil {
		return fmt.Errorf("start workspace %s: %w", ws.ID, err)
	}
	ws.State = StateRunning
	setupCtx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	// The bind mount belongs to the host user, so git inside the container
	// would refuse it; the index refresh keeps later git status fast.
	for _, cmd := range [][]string{
		{"git", "config", "--global", "--add", "safe.directory", "*"},
		{"git", "update-index", "-q", "--refresh"},
	} {
		if code, out, err := m.cfg.Docker.Exec(setupCtx, name, ContainerDir, cmd...); err != nil || code != 0 {
			return fmt.Errorf("set up workspace %s: %s: exit %d: %v %s", ws.ID, strings.Join(cmd, " "), code, err, strings.TrimSpace(out))
		}
	}
	return nil
}

func (m *Manager) checkQuota(ctx context.Context, ws *Workspace) error {
	list, err := m.cfg.Store.List(ctx, ws.Owner)
	if err != nil {
		return err
	}
	n := 0
	for _, other := range list {
		if other.ID != ws.ID && other.State == StateRunning {
			n++
		}
	}
	if n >= m.cfg.MaxRunning {
		return fmt.Errorf("%w: %d of %d are running; stop one first", ErrQuota, n, m.cfg.MaxRunning)
	}
	return nil
}

// Stop stops the container and keeps the files.
func (m *Manager) Stop(ctx context.Context, id string) error {
	unlock := m.lock(id)
	defer unlock()
	if m.used(id) {
		return ErrBusy
	}
	return m.stopLocked(ctx, id)
}

func (m *Manager) stopLocked(ctx context.Context, id string) error {
	ws, err := m.cfg.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, running, err := m.cfg.Docker.Inspect(ctx, ContainerName(id)); err != nil {
		return err
	} else if running {
		if err := m.cfg.Docker.Stop(ctx, ContainerName(id)); err != nil {
			return err
		}
	}
	ws.State = StateStopped
	return m.cfg.Store.Put(ctx, *ws)
}

// Delete removes the container, the files and the record.
func (m *Manager) Delete(ctx context.Context, id string) error {
	unlock := m.lock(id)
	defer unlock()
	if m.used(id) {
		return ErrBusy
	}
	return m.deleteLocked(ctx, id)
}

func (m *Manager) deleteLocked(ctx context.Context, id string) error {
	ws, err := m.cfg.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := m.cfg.Docker.Remove(ctx, ContainerName(id)); err != nil {
		return err
	}
	if err := os.RemoveAll(ws.Dir); err != nil {
		return fmt.Errorf("workspace: remove %s: %w", ws.Dir, err)
	}
	return m.cfg.Store.Delete(ctx, id)
}

// ReapResult lists what one Reap pass did.
type ReapResult struct {
	Stopped []string
	Deleted []string
}

// Reap stops idle containers and deletes workspaces unused for longer than
// Retention; workspaces in use are skipped.
func (m *Manager) Reap(ctx context.Context) (ReapResult, error) {
	var res ReapResult
	list, err := m.cfg.Store.List(ctx, "")
	if err != nil {
		return res, err
	}
	now := m.cfg.Now()
	var errs []error
	for _, ws := range list {
		idle := now.Sub(ws.LastUsedAt)
		if idle < m.cfg.IdleTimeout {
			continue
		}
		unlock := m.lock(ws.ID)
		switch {
		case m.used(ws.ID):
		case idle >= m.cfg.Retention:
			if err := m.deleteLocked(ctx, ws.ID); err != nil {
				errs = append(errs, err)
			} else {
				res.Deleted = append(res.Deleted, ws.ID)
			}
		case ws.State == StateRunning:
			if err := m.stopLocked(ctx, ws.ID); err != nil {
				errs = append(errs, err)
			} else {
				res.Stopped = append(res.Stopped, ws.ID)
			}
		}
		unlock()
	}
	return res, errors.Join(errs...)
}

// Reconcile marks workspaces whose container is gone or stopped (after a
// host restart, say) as stopped.
func (m *Manager) Reconcile(ctx context.Context) error {
	list, err := m.cfg.Store.List(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, ws := range list {
		if ws.State != StateRunning {
			continue
		}
		_, running, err := m.cfg.Docker.Inspect(ctx, ContainerName(ws.ID))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !running {
			ws.State = StateStopped
			errs = append(errs, m.cfg.Store.Put(ctx, ws))
		}
	}
	return errors.Join(errs...)
}

// Run reconciles once, then reaps every minute until ctx is done.
func (m *Manager) Run(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	if err := m.Reconcile(ctx); err != nil {
		log.Warn("workspace reconcile", "err", err)
	}
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			res, err := m.Reap(ctx)
			if err != nil {
				log.Warn("workspace reap", "err", err)
			}
			if len(res.Stopped)+len(res.Deleted) > 0 {
				log.Info("workspace reap", "stopped", res.Stopped, "deleted", res.Deleted)
			}
		}
	}
}

// Backend serves file tools from the host clone and runs commands in the
// container.
func (m *Manager) Backend(ws *Workspace) *backend.DockerExecBackend {
	bin := "docker"
	if d, ok := m.cfg.Docker.(CLIDocker); ok && d.Binary != "" {
		bin = d.Binary
	}
	return backend.NewDockerExecBackend(backend.DockerExecOptions{
		FilesystemOptions: backend.FilesystemOptions{RootDir: ws.Dir, VirtualMode: true, RespectGitignore: true},
		Container:         ContainerName(ws.ID), Workdir: ContainerDir, Binary: bin,
	})
}

// WorkingDirSection replaces the system prompt's path section for agents
// running in a workspace.
func WorkingDirSection(ws *Workspace) string {
	return fmt.Sprintf("### Current Working Directory\n\n"+
		"The repository `%[2]s` is checked out at `%[1]s` inside a Linux container.\n\n"+
		"### File System and Paths\n\n"+
		"**IMPORTANT - Path Handling:**\n"+
		"- File tools (`ls`, `read_file`, `write_file`, `edit_file`, `delete`, `glob`, `grep`) use virtual absolute paths where `/` is `%[1]s`.\n"+
		"- Example: to edit `%[1]s/src/main.py`, call `edit_file(file_path=\"/src/main.py\", ...)`.\n"+
		"- `execute` runs shell commands in `%[1]s`, so use real paths there (e.g. `%[1]s/src/main.py` or `src/main.py`).\n"+
		"- The container has network access; install the dependencies the project needs.\n\n",
		ContainerDir, ws.RepoURL)
}

// GitClone runs `git clone` on the host. A token is sent as an HTTP header
// through the environment, so it is neither in the process list nor saved
// in the clone's config.
func GitClone(ctx context.Context, spec Spec, dir, token string) error {
	args := []string{"clone", "--quiet"}
	if spec.BaseBranch != "" {
		args = append(args, "--branch", spec.BaseBranch)
	}
	args = append(args, "--", spec.RepoURL, dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if token != "" {
		cmd.Env = append(cmd.Env, AuthEnv(token)...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// AuthEnv configures git to send token as GitHub HTTPS basic auth.
func AuthEnv(token string) []string {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + basic,
	}
}

// excludeArtifacts keeps the agent's /.quill files out of git.
func excludeArtifacts(dir string) error {
	path := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString("\n/.quill/\n")
	return errors.Join(err, f.Close())
}
