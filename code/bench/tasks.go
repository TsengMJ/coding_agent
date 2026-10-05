// Package bench runs SWE-bench Pro V2 against the quill agent: it prepares
// the task catalog, runs each task under the locked protocol (offline agent
// phase, pristine re-grade with the official verifier), and stores results,
// per-test outcomes and process metrics in PostgreSQL.
package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Language buckets used for stratified sampling; ts is folded into js
// because V2 has only four ts tasks.
const (
	LangGo     = "go"
	LangPython = "python"
	LangJS     = "js"
	LangTS     = "ts"
)

// Task is one SWE-bench Pro V2 instance: the task directory contents plus
// the HuggingFace metadata the directory lacks.
type Task struct {
	InstanceID       string
	Repo             string
	Language         string
	Hard             bool
	IssueCategories  []string
	IssueSpecificity []string
	Image            string
	// Dir is the Harbor task directory (instruction.md, tests/, solution/).
	Dir            string
	FailToPass     []string
	PassToPass     []string
	SelectedTests  []string
	GoldPaths      []string
	GoldAdded      int
	GoldRemoved    int
	AgentTimeout   float64
	VerifyTimeout  float64
	CPUs           float64
	MemoryMB       int
	VerifierOnline bool
}

// Bucket is the stratification bucket for the task's language.
func (t Task) Bucket() string {
	if t.Language == LangTS {
		return LangJS
	}
	return t.Language
}

// Instruction returns instruction.md, the exact text the official agents see.
func (t Task) Instruction() (string, error) {
	data, err := os.ReadFile(filepath.Join(t.Dir, "instruction.md"))
	if err != nil {
		return "", fmt.Errorf("bench: instruction for %s: %w", t.InstanceID, err)
	}
	return string(data), nil
}

// GoldPatch returns solution/gold_patch.diff.
func (t Task) GoldPatch() (string, error) {
	data, err := os.ReadFile(filepath.Join(t.Dir, "solution", "gold_patch.diff"))
	if err != nil {
		return "", fmt.Errorf("bench: gold patch for %s: %w", t.InstanceID, err)
	}
	return string(data), nil
}

// TestsDir is the verifier directory copied to /tests before grading.
func (t Task) TestsDir() string { return filepath.Join(t.Dir, "tests") }

// Metadata is the subset of HuggingFace columns the task directory lacks.
type Metadata struct {
	InstanceID       string
	Repo             string
	Language         string
	Hard             bool
	IssueCategories  []string
	IssueSpecificity []string
	Image            string
}

type taskConfig struct {
	FailToPass    json.RawMessage `json:"fail_to_pass"`
	PassToPass    json.RawMessage `json:"pass_to_pass"`
	SelectedTests json.RawMessage `json:"selected_test_files_to_run"`
}

type taskTOML struct {
	Verifier struct {
		NetworkMode string  `toml:"network_mode"`
		TimeoutSec  float64 `toml:"timeout_sec"`
	} `toml:"verifier"`
	Agent struct {
		TimeoutSec float64 `toml:"timeout_sec"`
	} `toml:"agent"`
	Environment struct {
		DockerImage string  `toml:"docker_image"`
		CPUs        float64 `toml:"cpus"`
		MemoryMB    int     `toml:"memory_mb"`
	} `toml:"environment"`
}

// LoadTask reads one Harbor task directory.
func LoadTask(dir string) (Task, error) {
	t := Task{InstanceID: filepath.Base(dir), Dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, "tests", "config.json"))
	if err != nil {
		return t, fmt.Errorf("bench: %s: %w", t.InstanceID, err)
	}
	var cfg taskConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return t, fmt.Errorf("bench: %s config.json: %w", t.InstanceID, err)
	}
	if t.FailToPass, err = ParseTestList(cfg.FailToPass); err != nil {
		return t, fmt.Errorf("bench: %s fail_to_pass: %w", t.InstanceID, err)
	}
	if t.PassToPass, err = ParseTestList(cfg.PassToPass); err != nil {
		return t, fmt.Errorf("bench: %s pass_to_pass: %w", t.InstanceID, err)
	}
	if t.SelectedTests, err = ParseTestList(cfg.SelectedTests); err != nil {
		return t, fmt.Errorf("bench: %s selected_test_files_to_run: %w", t.InstanceID, err)
	}
	var tt taskTOML
	if _, err := toml.DecodeFile(filepath.Join(dir, "task.toml"), &tt); err != nil {
		return t, fmt.Errorf("bench: %s task.toml: %w", t.InstanceID, err)
	}
	t.Image = tt.Environment.DockerImage
	t.CPUs = tt.Environment.CPUs
	t.MemoryMB = tt.Environment.MemoryMB
	t.AgentTimeout = tt.Agent.TimeoutSec
	t.VerifyTimeout = tt.Verifier.TimeoutSec
	t.VerifierOnline = tt.Verifier.NetworkMode != "no-network"
	gold, err := t.GoldPatch()
	if err != nil {
		return t, err
	}
	st := ParsePatch(gold)
	t.GoldPaths, t.GoldAdded, t.GoldRemoved = st.Files, st.Added, st.Removed
	return t, nil
}

// LoadTasks reads every task under v2Dir/tasks and merges meta by instance
// ID. A task without metadata is an error: the catalog must be complete.
func LoadTasks(v2Dir string, meta map[string]Metadata) ([]Task, error) {
	entries, err := os.ReadDir(filepath.Join(v2Dir, "tasks"))
	if err != nil {
		return nil, fmt.Errorf("bench: read tasks: %w", err)
	}
	var out []Task
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := LoadTask(filepath.Join(v2Dir, "tasks", e.Name()))
		if err != nil {
			return nil, err
		}
		if meta != nil {
			m, ok := meta[t.InstanceID]
			if !ok {
				return nil, fmt.Errorf("bench: no metadata for %s", t.InstanceID)
			}
			t.Repo, t.Language, t.Hard = m.Repo, m.Language, m.Hard
			t.IssueCategories, t.IssueSpecificity = m.IssueCategories, m.IssueSpecificity
			if m.Image != "" && m.Image != t.Image {
				return nil, fmt.Errorf("bench: %s image mismatch: task.toml %q, metadata %q", t.InstanceID, t.Image, m.Image)
			}
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out, nil
}

// ReadIDList reads one instance ID per line, skipping blanks and # comments.
func ReadIDList(data string) []string {
	var out []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// ParseTestList decodes a test list stored as a JSON array, a JSON string
// holding a JSON array, or a JSON string holding a Python list literal
// (tests/config.json uses all three).
func ParseTestList(raw json.RawMessage) ([]string, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, nil
	}
	var list []string
	if strings.HasPrefix(s, "[") {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(str), &list); err == nil {
		return list, nil
	}
	return parsePythonStringList(str)
}

// parsePythonStringList parses a Python list literal of string literals,
// e.g. ['a', "b's"].
func parsePythonStringList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, errors.New("not a list literal")
	}
	body := []rune(s[1 : len(s)-1])
	var out []string
	i := 0
	skipSpace := func() {
		for i < len(body) && (body[i] == ' ' || body[i] == '\n' || body[i] == '\t' || body[i] == '\r') {
			i++
		}
	}
	for {
		skipSpace()
		if i >= len(body) {
			return out, nil
		}
		q := body[i]
		if q != '\'' && q != '"' {
			return nil, fmt.Errorf("expected string literal at offset %d", i)
		}
		i++
		var b strings.Builder
		closed := false
		for i < len(body) {
			c := body[i]
			if c == q {
				i++
				closed = true
				break
			}
			if c == '\\' && i+1 < len(body) {
				n, adv, err := pythonEscape(body[i+1:])
				if err != nil {
					return nil, err
				}
				b.WriteString(n)
				i += 1 + adv
				continue
			}
			b.WriteRune(c)
			i++
		}
		if !closed {
			return nil, errors.New("unterminated string literal")
		}
		out = append(out, b.String())
		skipSpace()
		if i < len(body) {
			if body[i] != ',' {
				return nil, fmt.Errorf("expected ',' at offset %d", i)
			}
			i++
		}
	}
}

// pythonEscape decodes the escape after a backslash and reports how many
// runes it consumed.
func pythonEscape(r []rune) (string, int, error) {
	switch r[0] {
	case '\\', '\'', '"':
		return string(r[0]), 1, nil
	case 'n':
		return "\n", 1, nil
	case 't':
		return "\t", 1, nil
	case 'r':
		return "\r", 1, nil
	case 'x', 'u', 'U':
		width := map[rune]int{'x': 2, 'u': 4, 'U': 8}[r[0]]
		if len(r) < 1+width {
			return "", 0, errors.New("truncated escape")
		}
		var v rune
		for _, c := range r[1 : 1+width] {
			d := strings.IndexRune("0123456789abcdef", toLower(c))
			if d < 0 {
				return "", 0, fmt.Errorf("bad hex digit %q", c)
			}
			v = v*16 + rune(d)
		}
		return string(v), 1 + width, nil
	default:
		return "\\" + string(r[0]), 1, nil
	}
}

func toLower(c rune) rune {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
