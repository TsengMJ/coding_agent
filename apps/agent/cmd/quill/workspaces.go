package main

import (
	"flag"
	"os"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
)

// workspaceFlags configure `quill serve` workspaces.
type workspaceFlags struct {
	root       string
	image      string
	idle       time.Duration
	retention  time.Duration
	maxRunning int
	cpus       float64
	memoryMB   int
	network    string
	localRepos bool
	// require refuses runs in the server's own directories; set by -public.
	require bool
}

func (w *workspaceFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&w.root, "workspaces", os.Getenv("QUILL_WORKSPACES_ROOT"),
		"directory for per-thread repository clones run in Docker containers ($QUILL_WORKSPACES_ROOT); empty disables workspaces")
	fs.StringVar(&w.image, "workspace-image", workspace.DefaultImage, "image for repositories without .quill/Dockerfile or devcontainer image")
	fs.DurationVar(&w.idle, "workspace-idle", workspace.DefaultIdleTimeout, "stop a workspace container unused for this long")
	fs.DurationVar(&w.retention, "workspace-retention", workspace.DefaultRetention, "delete a workspace unused for this long")
	fs.IntVar(&w.maxRunning, "workspace-max-running", workspace.DefaultMaxRunning, "running workspace containers allowed per user")
	fs.Float64Var(&w.cpus, "workspace-cpus", 0, "CPU limit per workspace container (0 = unlimited)")
	fs.IntVar(&w.memoryMB, "workspace-memory-mb", 0, "memory limit per workspace container in MB (0 = unlimited)")
	fs.StringVar(&w.network, "workspace-network", "", "docker network for workspace containers (empty = default bridge)")
	fs.BoolVar(&w.localRepos, "workspace-local-repos", false, "also accept server paths and file:// URLs as repo_url (single-user setups only)")
}

func (w *workspaceFlags) manager(store workspace.Store) (*workspace.Manager, error) {
	return workspace.NewManager(workspace.Config{
		Root: w.root, Store: store, DefaultImage: w.image,
		IdleTimeout: w.idle, Retention: w.retention, MaxRunning: w.maxRunning,
		CPUs: w.cpus, MemoryMB: w.memoryMB, Network: w.network,
		AllowLocalRepos: w.localRepos,
	})
}
