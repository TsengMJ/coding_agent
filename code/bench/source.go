package bench

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Pinned sources. Bumping TasksRepoCommit changes the benchmark: record it
// per run (bench_runs.tasks_repo_commit) and re-check smoke50.txt.
const (
	TasksRepoURL    = "https://github.com/scaleapi/SWE-bench_Pro-os"
	TasksRepoCommit = "66f92766bba642462d4bbe5479e83f91f9211862"
	HFDataset       = "ScaleAI/SWE-bench_Pro"
	HFConfig        = "default"
	// Benchmark names this benchmark and version in bench_runs.
	Benchmark = "swebench_pro_v2"
)

// DefaultHFRowsURL is the datasets-server rows endpoint.
const DefaultHFRowsURL = "https://datasets-server.huggingface.co/rows"

// DefaultHFInfoURL returns dataset info including the revision sha.
const DefaultHFInfoURL = "https://huggingface.co/api/datasets/" + HFDataset

// EnsureTasksRepo sparse-checks out v2/ of the task repository at commit
// into dir and returns the v2 directory. An existing checkout is moved to
// commit.
func EnsureTasksRepo(ctx context.Context, dir, commit string) (string, error) {
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", err
		}
		cmd := exec.CommandContext(ctx, "git", "clone", "-q", "--filter=blob:none", "--no-checkout", TasksRepoURL, dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("bench: clone %s: %w: %s", TasksRepoURL, err, strings.TrimSpace(string(out)))
		}
		if err := git("sparse-checkout", "set", "--no-cone", "/v2/"); err != nil {
			return "", fmt.Errorf("bench: %w", err)
		}
	}
	if err := git("cat-file", "-e", commit+"^{commit}"); err != nil {
		if err := git("fetch", "-q", "origin"); err != nil {
			return "", fmt.Errorf("bench: %w", err)
		}
	}
	if err := git("checkout", "-q", commit); err != nil {
		return "", fmt.Errorf("bench: %w", err)
	}
	return filepath.Join(dir, "v2"), nil
}

// VerifyChecksums checks every file listed in v2Dir/SHA256SUMS and returns
// how many were verified.
func VerifyChecksums(v2Dir string) (int, error) {
	f, err := os.Open(filepath.Join(v2Dir, "SHA256SUMS"))
	if err != nil {
		return 0, fmt.Errorf("bench: %w", err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		want, name, ok := strings.Cut(line, "  ")
		if !ok {
			return n, fmt.Errorf("bench: SHA256SUMS: malformed line %q", line)
		}
		got, err := fileSHA256(filepath.Join(v2Dir, filepath.FromSlash(name)))
		if err != nil {
			return n, fmt.Errorf("bench: checksum %s: %w", name, err)
		}
		if got != want {
			return n, fmt.Errorf("bench: checksum mismatch for %s", name)
		}
		n++
	}
	return n, sc.Err()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HFClient reads dataset metadata from the HuggingFace APIs.
type HFClient struct {
	HTTP    *http.Client
	RowsURL string
	InfoURL string
}

func (c HFClient) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c HFClient) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %s: %s", u, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Revision returns the dataset's current git sha on HuggingFace.
func (c HFClient) Revision(ctx context.Context) (string, error) {
	u := c.InfoURL
	if u == "" {
		u = DefaultHFInfoURL
	}
	var info struct {
		SHA string `json:"sha"`
	}
	if err := c.getJSON(ctx, u, &info); err != nil {
		return "", fmt.Errorf("bench: dataset revision: %w", err)
	}
	return info.SHA, nil
}

type hfRow struct {
	InstanceID       string `json:"instance_id"`
	Repo             string `json:"repo"`
	RepoLanguage     string `json:"repo_language"`
	Hard             bool   `json:"hard"`
	IssueCategories  string `json:"issue_categories"`
	IssueSpecificity string `json:"issue_specificity"`
	DockerImage      string `json:"docker_image"`
}

// hfPageSize is the datasets-server maximum page length.
const hfPageSize = 100

// Metadata pages through the test split and returns metadata by instance ID.
func (c HFClient) Metadata(ctx context.Context) (map[string]Metadata, error) {
	base := c.RowsURL
	if base == "" {
		base = DefaultHFRowsURL
	}
	out := map[string]Metadata{}
	for offset := 0; ; offset += hfPageSize {
		q := url.Values{"dataset": {HFDataset}, "config": {HFConfig}, "split": {"test"},
			"offset": {fmt.Sprint(offset)}, "length": {fmt.Sprint(hfPageSize)}}
		var page struct {
			Rows []struct {
				Row hfRow `json:"row"`
			} `json:"rows"`
			Total int `json:"num_rows_total"`
		}
		if err := c.getJSON(ctx, base+"?"+q.Encode(), &page); err != nil {
			return nil, fmt.Errorf("bench: metadata: %w", err)
		}
		for _, r := range page.Rows {
			m := Metadata{InstanceID: r.Row.InstanceID, Repo: r.Row.Repo, Language: r.Row.RepoLanguage,
				Hard: r.Row.Hard, Image: r.Row.DockerImage}
			var err error
			if m.IssueCategories, err = ParseTestList(json.RawMessage(quoteJSON(r.Row.IssueCategories))); err != nil {
				return nil, fmt.Errorf("bench: %s issue_categories: %w", m.InstanceID, err)
			}
			if m.IssueSpecificity, err = ParseTestList(json.RawMessage(quoteJSON(r.Row.IssueSpecificity))); err != nil {
				return nil, fmt.Errorf("bench: %s issue_specificity: %w", m.InstanceID, err)
			}
			out[m.InstanceID] = m
		}
		if len(page.Rows) == 0 || offset+hfPageSize >= page.Total {
			break
		}
	}
	return out, nil
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
