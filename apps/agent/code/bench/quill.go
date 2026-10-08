package bench

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	codeagent "github.com/tsengmingchun/quill/apps/agent/code/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/code/cost"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tracing"
)

// ErrBudgetExceeded stops an agent that spent its -max-cost.
var ErrBudgetExceeded = errors.New("bench: cost budget exceeded")

// captureCommand is the official patch capture: everything the agent
// changed, new files included, relative to the image's commit.
const captureCommand = "git add -A && git diff --cached; rc=$?; git reset -q; exit $rc"

// captureTimeout bounds setup and patch capture commands.
const captureTimeout = 5 * time.Minute

// QuillSolver runs the quill agent offline against a copy of the task
// repository mounted into the task image.
type QuillSolver struct {
	Settings *config.Settings
	// NewModel builds a fresh model per attempt.
	NewModel func() (llm.ChatModel, error)
	Prices   cost.Table
	// MaxSteps caps main-agent model calls (0 = runtime default).
	MaxSteps int
	// MaxCostUSD stops the agent once spent (0 = no limit).
	MaxCostUSD float64
	// Timeout overrides the task's agent timeout.
	Timeout time.Duration
	// KeepWorkspace leaves the host copy of the repository for inspection.
	KeepWorkspace bool
	// DockerBinary runs the agent's shell commands (default "docker").
	DockerBinary string
	// Traces records each attempt's trace under its own thread; required.
	Traces trace.Log
	// Tracer also sends run trees to LangSmith; nil is off.
	Tracer tracing.Tracer
	// Disable turns agent features off for ablation (codeagent.Features).
	Disable []string
}

// TracePrefix starts SolveResult.TracePath; the rest is the trace thread ID.
const TracePrefix = "quill_trace_records:"

// traceThread names one attempt's trace.
func traceThread(env SolveEnv, task Task) string {
	return fmt.Sprintf("bench/%s/%s/%d", env.RunID, task.InstanceID, env.Trial)
}

func (*QuillSolver) Name() string { return SolverQuill }

func (s *QuillSolver) Solve(ctx context.Context, task Task, env SolveEnv) (SolveResult, error) {
	d := env.Docker
	ws := filepath.Join(env.WorkDir, "workspace")
	if err := os.RemoveAll(env.WorkDir); err != nil {
		return SolveResult{}, err
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return SolveResult{}, err
	}
	if !s.KeepWorkspace {
		defer os.RemoveAll(ws)
	}
	repoDir := "/app"
	if err := d.CopyOut(ctx, task.Image, "/app/.", ws); err != nil {
		repoDir = "/testbed"
		if err2 := d.CopyOut(ctx, task.Image, "/testbed/.", ws); err2 != nil {
			return SolveResult{}, fmt.Errorf("copy repository out of image: %w", errors.Join(err, err2))
		}
	}
	if err := excludeArtifacts(ws); err != nil {
		return SolveResult{}, err
	}
	id, err := d.Run(ctx, ContainerSpec{
		Image: task.Image, Name: containerName("agent", env.RunID, task.InstanceID, env.Trial),
		NoNetwork: true, CPUs: task.CPUs, MemoryMB: task.MemoryMB,
		Mounts: map[string]string{ws: repoDir}, Workdir: repoDir,
	})
	if err != nil {
		return SolveResult{}, fmt.Errorf("start agent container: %w", err)
	}
	defer d.Remove(context.WithoutCancel(ctx), id)
	if res, err := d.Exec(ctx, id, ExecSpec{Cmd: []string{"git", "config", "--global", "--add", "safe.directory", "*"}, Timeout: captureTimeout}); err != nil || res.ExitCode != 0 {
		return SolveResult{}, fmt.Errorf("configure git: %v %s", err, res.Stderr)
	}
	// The copy out of the image changed every file's stat data, and the git
	// tools never write the index (GIT_OPTIONAL_LOCKS=0); without this one
	// refresh every git status / diff re-hashes the whole tree.
	if res, err := d.Exec(ctx, id, ExecSpec{Cmd: []string{"git", "update-index", "-q", "--refresh"}, Workdir: repoDir, Timeout: captureTimeout}); err != nil || res.ExitCode != 0 {
		return SolveResult{}, fmt.Errorf("refresh git index: %v %s", err, res.Stderr)
	}

	res := s.runAgent(ctx, task, id, ws, repoDir, traceThread(env, task), env.LogDir)
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return SolveResult{}, ctx.Err()
	}
	out, err := d.Exec(context.WithoutCancel(ctx), id, ExecSpec{Cmd: []string{"sh", "-c", captureCommand}, Workdir: repoDir, Timeout: captureTimeout})
	if err != nil || out.ExitCode != 0 {
		return SolveResult{}, fmt.Errorf("capture patch: %v %s", err, clipTail(string(out.Stderr), 500))
	}
	res.Patch = string(out.Stdout)
	return res, nil
}

// runAgent runs quill until it finishes or hits a budget; the outcome is
// described by Category and Error rather than returned as an error.
func (s *QuillSolver) runAgent(ctx context.Context, task Task, container, ws, repoDir, threadID, logDir string) SolveResult {
	var res SolveResult
	if s.Traces == nil {
		res.Category, res.Error = CategoryAgentError, "bench.QuillSolver: Traces is required"
		return res
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = time.Duration(task.AgentTimeout * float64(time.Second))
	}
	if timeout <= 0 {
		timeout = 50 * time.Minute
	}
	inner, err := s.NewModel()
	if err != nil {
		res.Category, res.Error = CategoryModelError, err.Error()
		return res
	}
	model := &meteredModel{ChatModel: inner, prices: s.Prices, limit: s.MaxCostUSD}
	traces := trace.NewStore(s.Traces)
	defer traces.Close()
	rec, err := traces.Recorder(threadID)
	if err != nil {
		res.Category, res.Error = CategoryAgentError, err.Error()
		return res
	}
	res.TracePath = TracePrefix + threadID

	b := backend.NewDockerExecBackend(backend.DockerExecOptions{
		FilesystemOptions: backend.FilesystemOptions{RootDir: ws, VirtualMode: true, RespectGitignore: true},
		Container:         container, Workdir: repoDir, Binary: s.DockerBinary,
	})
	var verdicts []BaselineVerdict
	var reviews []ReviewVerdict
	var verdictsMu sync.Mutex
	a, err := codeagent.CreateCLIAgent(ctx, codeagent.Options{
		Settings: s.Settings, Model: trace.WrapModel(model), ReviewModel: trace.WrapModel(model), Cwd: ws, Backend: b,
		Checkpointer: rt.NewMemoryCheckpointer(), AutoApprove: true, NoWebTools: true,
		ProjectContext:    projectContext(task, repoDir),
		WorkingDirSection: workingDirSection(repoDir),
		Tracer:            s.Tracer,
		Disable:           s.Disable,
		OnBaseline: func(verdict string, tests []string) {
			verdictsMu.Lock()
			defer verdictsMu.Unlock()
			verdicts = append(verdicts, BaselineVerdict{Verdict: verdict, Tests: tests})
		},
		OnReview: func(r codeagent.Review, err error) {
			verdictsMu.Lock()
			defer verdictsMu.Unlock()
			reviews = append(reviews, reviewVerdict(r, err))
		},
	})
	if err != nil {
		res.Category, res.Error = CategoryAgentError, err.Error()
		return res
	}
	instruction, err := task.Instruction()
	if err != nil {
		res.Category, res.Error = CategoryAgentError, err.Error()
		return res
	}
	runCtx, cancel := context.WithTimeout(trace.WithRecorder(ctx, rec), timeout)
	defer cancel()
	start := time.Now()
	_, runErr := a.Invoke(runCtx, core.Input{Messages: []llm.Message{llm.NewUserMessage(instruction)}},
		rt.RunConfig{ThreadID: "bench", RecursionLimit: s.MaxSteps,
			Metadata: map[string]any{"bench_attempt": threadID, "instance_id": task.InstanceID}})
	agentMS := time.Since(start).Milliseconds()
	if runErr != nil {
		res.Category, res.Error = classifyAgentError(runErr, runCtx, model), runErr.Error()
	}
	if calls, err := traces.Details(threadID); err == nil {
		res.Metrics = MetricsFromTrace(calls, s.Prices)
		res.Metrics.Process = ProcessFromTrace(calls, task.GoldPaths)
	}
	if res.Metrics.Process == nil {
		res.Metrics.Process = &Process{}
	}
	verdictsMu.Lock()
	res.Metrics.Process.BaselineVerdicts = verdicts
	res.Metrics.Process.Reviews = reviews
	verdictsMu.Unlock()
	if err := writeBaselineLog(logDir, verdicts); err != nil {
		res.Error = strings.TrimSpace(res.Error + "\n" + err.Error())
	}
	res.Metrics.AgentMS = agentMS
	return res
}

func reviewVerdict(r codeagent.Review, err error) ReviewVerdict {
	if err != nil {
		return ReviewVerdict{Error: err.Error()}
	}
	v := ReviewVerdict{Verdict: r.Verdict}
	for _, is := range r.Issues {
		switch is.Severity {
		case "high":
			v.High++
		case "medium":
			v.Medium++
		}
	}
	for _, req := range r.Requirements {
		if !req.Met {
			v.Unmet++
		}
	}
	return v
}

func writeBaselineLog(logDir string, verdicts []BaselineVerdict) error {
	if logDir == "" || len(verdicts) == 0 {
		return nil
	}
	var b strings.Builder
	for _, v := range verdicts {
		fmt.Fprintf(&b, "%s\t%s\n", v.Verdict, strings.Join(v.Tests, ", "))
	}
	return os.WriteFile(filepath.Join(logDir, "baseline.log"), []byte(b.String()), 0o644)
}

func classifyAgentError(err error, runCtx context.Context, m *meteredModel) string {
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return CategoryAgentTimeout
	case errors.Is(err, ErrBudgetExceeded), errors.Is(err, core.ErrRecursionLimit):
		return CategoryBudget
	case m.failed():
		return CategoryModelError
	}
	return CategoryAgentError
}

// excludeArtifacts keeps the agent's /.quill files out of the patch.
func excludeArtifacts(ws string) error {
	path := filepath.Join(ws, ".git", "info", "exclude")
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

func containerName(kind, runID, instance string, trial int) string {
	name := fmt.Sprintf("quillbench-%s-%s-%s-t%d", kind, runID, instance, trial)
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, name)
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

func workingDirSection(repoDir string) string {
	return fmt.Sprintf("### Current Working Directory\n\n"+
		"The repository is at `%[1]s` inside a Linux container with no network access.\n\n"+
		"### File System and Paths\n\n"+
		"**IMPORTANT - Path Handling:**\n"+
		"- File tools (`ls`, `read_file`, `write_file`, `edit_file`, `delete`, `glob`, `grep`) use virtual absolute paths where `/` is `%[1]s`.\n"+
		"- Example: to edit `%[1]s/src/main.py`, call `edit_file(file_path=\"/src/main.py\", ...)`.\n"+
		"- `execute` runs shell commands in `%[1]s`, so use real paths there (e.g. `%[1]s/src/main.py` or `src/main.py`).\n"+
		"- Packages cannot be installed: the network is disabled. Use the tools and dependencies already present.\n\n",
		repoDir)
}

func projectContext(task Task, repoDir string) string {
	return fmt.Sprintf("### Project Context\n\nRepository `%s` (%s), checked out at `%s`. "+
		"Run the relevant existing tests to check your change before finishing.\n\n", task.Repo, task.Language, repoDir)
}

// meteredModel enforces the cost budget and remembers model failures.
type meteredModel struct {
	llm.ChatModel
	prices cost.Table
	limit  float64

	mu          sync.Mutex
	spent       float64
	modelFailed bool
}

func (m *meteredModel) check() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.limit > 0 && m.spent >= m.limit {
		return fmt.Errorf("%w: spent $%.2f of $%.2f", ErrBudgetExceeded, m.spent, m.limit)
	}
	return nil
}

func (m *meteredModel) account(msg *llm.Message, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, notimpl.ErrNotImplemented) {
		m.modelFailed = true
	}
	if msg == nil || msg.Usage == nil {
		return
	}
	info := m.Info()
	spec := info.Model
	if info.Provider != "" {
		spec = info.Provider + ":" + spec
	}
	if usd, err := m.prices.Estimate(spec, *msg.Usage); err == nil {
		m.spent += usd
	}
}

func (m *meteredModel) failed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modelFailed
}

func (m *meteredModel) Generate(ctx context.Context, req llm.ChatRequest) (*llm.Message, error) {
	if err := m.check(); err != nil {
		return nil, err
	}
	msg, err := m.ChatModel.Generate(ctx, req)
	m.account(msg, err)
	return msg, err
}

func (m *meteredModel) Stream(ctx context.Context, req llm.ChatRequest) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		if err := m.check(); err != nil {
			yield(llm.Chunk{}, err)
			return
		}
		for c, err := range m.ChatModel.Stream(ctx, req) {
			if err != nil {
				m.account(nil, err)
			} else if c.Done {
				m.account(&c.Delta, nil)
			}
			if !yield(c, err) || err != nil {
				return
			}
		}
	}
}
