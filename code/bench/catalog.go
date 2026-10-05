package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Layout of the bench data directory (~/.quill/bench).
const (
	tasksRepoDir = "SWE-bench_Pro-os"
	metadataFile = "metadata.json"
)

// Catalog is the prepared task set: pinned task directories plus cached
// HuggingFace metadata.
type Catalog struct {
	DataDir         string
	V2Dir           string
	TasksRepoCommit string
	DatasetRevision string
	Tasks           []Task
	byID            map[string]Task
}

// Task returns the task with the given instance ID.
func (c *Catalog) Task(id string) (Task, bool) {
	t, ok := c.byID[id]
	return t, ok
}

type metadataCache struct {
	Revision  string              `json:"revision"`
	FetchedAt time.Time           `json:"fetched_at"`
	Tasks     map[string]Metadata `json:"tasks"`
}

// PrepareResult reports what Prepare did.
type PrepareResult struct {
	Catalog  *Catalog
	Verified int
}

// Prepare checks out the pinned task repository, verifies SHA256SUMS,
// fetches HuggingFace metadata and caches it under dataDir.
func Prepare(ctx context.Context, dataDir string, hf HFClient) (*PrepareResult, error) {
	v2, err := EnsureTasksRepo(ctx, filepath.Join(dataDir, tasksRepoDir), TasksRepoCommit)
	if err != nil {
		return nil, err
	}
	n, err := VerifyChecksums(v2)
	if err != nil {
		return nil, err
	}
	meta, err := hf.Metadata(ctx)
	if err != nil {
		return nil, err
	}
	rev, err := hf.Revision(ctx)
	if err != nil {
		return nil, err
	}
	cache := metadataCache{Revision: rev, FetchedAt: time.Now().UTC(), Tasks: meta}
	data, err := json.MarshalIndent(cache, "", " ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dataDir, metadataFile), data, 0o644); err != nil {
		return nil, fmt.Errorf("bench: write metadata cache: %w", err)
	}
	c, err := LoadCatalog(dataDir)
	if err != nil {
		return nil, err
	}
	return &PrepareResult{Catalog: c, Verified: n}, nil
}

// ErrNotPrepared means `quillbench prepare` has not been run.
var ErrNotPrepared = errors.New("bench: data not prepared; run `quillbench prepare`")

// LoadCatalog reads the prepared task directories and metadata cache.
func LoadCatalog(dataDir string) (*Catalog, error) {
	v2 := filepath.Join(dataDir, tasksRepoDir, "v2")
	data, err := os.ReadFile(filepath.Join(dataDir, metadataFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotPrepared
	}
	if err != nil {
		return nil, err
	}
	var cache metadataCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("bench: metadata cache: %w", err)
	}
	if _, err := os.Stat(v2); err != nil {
		return nil, ErrNotPrepared
	}
	tasks, err := LoadTasks(v2, cache.Tasks)
	if err != nil {
		return nil, err
	}
	c := &Catalog{DataDir: dataDir, V2Dir: v2, TasksRepoCommit: TasksRepoCommit, DatasetRevision: cache.Revision,
		Tasks: tasks, byID: map[string]Task{}}
	for _, t := range tasks {
		c.byID[t.InstanceID] = t
	}
	return c, nil
}
