package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	codeagent "github.com/tsengmingchun/quill/apps/agent/code/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/audit"
	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/code/browser"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/code/cost"
	"github.com/tsengmingchun/quill/apps/agent/code/delivery"
	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/code/process"
	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/middleware/planmode"
	"github.com/tsengmingchun/quill/apps/agent/middleware/summarization"
	"github.com/tsengmingchun/quill/apps/agent/middleware/todolist"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
	"github.com/tsengmingchun/quill/apps/agent/tracing"
)

// AutoApproveKey is the RunConfig.Configurable key that disables review.
const AutoApproveKey = hitl.AutoApproveKey

// Config configures a Service.
type Config struct {
	Settings *config.Settings
	Paths    config.Paths
	// ModelSpec ("provider:model") takes precedence over QUILL_MODEL and
	// Settings.Model.
	ModelSpec string
	// Model overrides ModelSpec with a constructed model.
	Model llm.ChatModel
	// Storage keeps threads, plans, traces and artifacts; required.
	// Service.Close closes it.
	Storage *storage.Storage
	// NoVerifyGate lets headless runs finish without the agent's
	// verify-before-done check (see codeagent.VerifyGate).
	NoVerifyGate bool
	// Tracer records run trees (codeagent.NewLangSmith); nil is off.
	// Service.Close closes it when it has a Close method.
	Tracer tracing.Tracer
	// MCP supplies tools from MCP servers; Service.Close closes it.
	MCP mcp.Session
	// ConnectMCP starts the browser MCP servers; nil uses mcp.Connect.
	ConnectMCP func(context.Context, []mcp.ServerConfig) (mcp.Session, error)
	// Workspaces runs threads in cloned repositories inside containers;
	// nil disables RunRequest.WorkspaceID.
	Workspaces *workspace.Manager
	// RequireWorkspace refuses runs in a server directory (RunRequest.Cwd),
	// so remote users cannot run commands on the server host.
	RequireWorkspace bool
	// DailyLimitUSD refuses new runs of a signed-in user who has spent this
	// much today (UTC); zero is no limit.
	DailyLimitUSD float64
	// Metrics, when set, counts runs and their spend.
	Metrics *Metrics
	// GitHub opens pull requests; nil uses api.github.com.
	GitHub *delivery.GitHub
	// PushURL, when set, maps a workspace's repo_url to the URL its
	// branches are pushed to (tests point it at a local remote).
	PushURL func(repoURL string) string
	// WorkspaceBrowser gives workspace runs a headless browser inside their
	// container (Playwright MCP); the image must provide Chromium at
	// browser.ContainerChromium.
	WorkspaceBrowser bool
}

// ResumeTraceTag marks the run of a turn that resumes after an approval, so
// LangSmith views can fold a turn's rounds back together (Python
// `RESUME_TRACE_TAG`; an external contract, do not rename).
const ResumeTraceTag = "quill:resume"

// Service runs the coding agent for many threads and working directories.
type Service struct {
	settings *config.Settings
	paths    config.Paths
	// model runs each call on the run's model (see switchModel).
	model        llm.ChatModel
	defaultModel runModel
	newModel     func(spec string) (llm.ChatModel, error)
	// webModel reads fetched pages and runs web research; nil uses model.
	webModel llm.ChatModel
	// models caches traced models by spec, guarded by mu.
	models       map[string]llm.ChatModel
	prices       cost.Table
	checkpointer runtime.Checkpointer
	sessions     sessions.Store
	store        *storage.Storage
	artifacts    *codeagent.Artifacts
	traces       *trace.Store
	noVerifyGate bool
	tracer       tracing.Tracer
	mcp          mcp.Session
	mcpTools     []tool.Tool
	browsers     *browserManager
	wsBrowsers   *workspaceBrowsers
	processes    *process.Manager
	workspaces   *workspace.Manager
	requireWS    bool
	audit        audit.Log
	dailyLimit   float64
	metrics      *Metrics
	github       *delivery.GitHub
	pushURL      func(string) string

	mu     sync.Mutex
	agents map[agentKey]*agentEntry
	busy   map[string]bool
}

type agentKey struct {
	cwd         string
	workspace   string
	interactive bool
	// browser is browserTools.key; empty without the panel.
	browser string
}

type agentEntry struct {
	once  sync.Once
	agent agent.Agent
	err   error
}

// New builds the model and wires cfg.Storage.
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.Storage == nil {
		return nil, errors.New("server.New: Storage is required")
	}
	newModel := func(spec string) (llm.ChatModel, error) { return llm.InitChatModel(spec, modelOptions(cfg)) }
	model, spec := cfg.Model, ""
	if model == nil {
		spec = resolveModelSpec(cfg)
		m, err := newModel(spec)
		if err != nil {
			return nil, fmt.Errorf("init model %q: %w", spec, err)
		}
		model = m
	} else {
		spec = specOf(model)
	}
	def := runModel{spec: spec, model: trace.WrapModel(model)}
	pricesPath := ""
	if cfg.Paths.UserDir != "" {
		pricesPath = filepath.Join(cfg.Paths.UserDir, PricesFile)
	}
	prices, err := cost.LoadTable(pricesPath)
	if err != nil {
		return nil, err
	}
	var webModel llm.ChatModel
	if cfg.Settings != nil && cfg.Settings.WebModel != "" {
		m, err := newModel(cfg.Settings.WebModel)
		if err != nil {
			return nil, fmt.Errorf("init models.web %q: %w", cfg.Settings.WebModel, err)
		}
		webModel = trace.WrapModel(m)
	}
	var mcpTools []tool.Tool
	if cfg.MCP != nil {
		if mcpTools, err = cfg.MCP.Tools(ctx); err != nil {
			return nil, err
		}
	}
	connect := cfg.ConnectMCP
	if connect == nil {
		connect = mcp.Connect
	}
	browserKind := browser.Playwright
	if cfg.Settings != nil && cfg.Settings.BrowserMCP != "" {
		if browserKind, err = browser.ParseKind(cfg.Settings.BrowserMCP); err != nil {
			return nil, err
		}
	}
	s := &Service{
		browsers:     newBrowserManager(browserKind, connect),
		processes:    process.NewManager(),
		workspaces:   cfg.Workspaces,
		requireWS:    cfg.RequireWorkspace,
		audit:        cfg.Storage.Audit,
		dailyLimit:   cfg.DailyLimitUSD,
		metrics:      cfg.Metrics,
		github:       cmp.Or(cfg.GitHub, &delivery.GitHub{}),
		pushURL:      cfg.PushURL,
		mcp:          cfg.MCP,
		mcpTools:     mcpTools,
		webModel:     webModel,
		prices:       prices,
		settings:     cfg.Settings,
		paths:        cfg.Paths,
		model:        &switchModel{def: def},
		defaultModel: def,
		newModel:     newModel,
		models:       map[string]llm.ChatModel{},
		agents:       map[agentKey]*agentEntry{},
		busy:         map[string]bool{},
		noVerifyGate: cfg.NoVerifyGate,
		tracer:       cfg.Tracer,
		store:        cfg.Storage,
		checkpointer: cfg.Storage.Checkpointer,
		sessions:     cfg.Storage.Sessions,
		traces:       trace.NewStore(cfg.Storage.Traces),
		artifacts: &codeagent.Artifacts{
			Results: cfg.Storage.Expiring(resultsTTL(cfg.Settings)),
			Web:     cfg.Storage.Expiring(WebTTL),
			History: cfg.Storage.Durable,
		},
	}
	if cfg.WorkspaceBrowser && cfg.Workspaces != nil {
		m := cfg.Workspaces
		s.wsBrowsers = newWorkspaceBrowsers(connect, func(id string) mcp.ServerConfig {
			return browser.ContainerPreset(m.DockerBinary(), workspace.ContainerName(id))
		})
		m.OnStop(s.dropWorkspaceBrowser)
	}
	return s, nil
}

// How long offloaded artifacts are kept after their last write.
const (
	DefaultResultsTTL = 7 * 24 * time.Hour
	WebTTL            = 24 * time.Hour
)

func resultsTTL(s *config.Settings) time.Duration {
	if s != nil && s.ResultsTTL > 0 {
		return s.ResultsTTL
	}
	return DefaultResultsTTL
}

// SetBrowserEndpoint registers the desktop browser panel's DevTools
// endpoint (empty unregisters it) and drops agents built on the old one.
func (s *Service) SetBrowserEndpoint(cdpURL string) error {
	err := s.browsers.SetEndpoint(cdpURL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.agents {
		if k.browser != "" {
			delete(s.agents, k)
		}
	}
	return err
}

// BrowserStatus reports whether a panel is registered and the MCP choices.
func (s *Service) BrowserStatus() BrowserStatus { return s.browsers.Status() }

// Storage reports where threads are kept: "postgres" or "memory".
func (s *Service) Storage() string { return s.store.Kind }

// Users is where the team server keeps logins and sessions.
func (s *Service) Users() auth.Store { return s.store.Users }

// Audit is the service's audit log; nil when the storage has none.
func (s *Service) Audit() audit.Log { return s.audit }

// ModelInfo identifies the service's default model.
func (s *Service) ModelInfo() llm.ModelInfo { return s.defaultModel.model.Info() }

// DefaultModel is the "provider:model" spec runs use unless the request or
// the thread names another.
func (s *Service) DefaultModel() string { return s.defaultModel.spec }

func (s *Service) Close() error {
	s.processes.Close()
	err := s.traces.Close()
	if c, ok := s.tracer.(interface{ Close() error }); ok {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close tracer: %w", cerr)
		}
	}
	s.store.Close()
	if cerr := s.browsers.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if s.wsBrowsers != nil {
		s.wsBrowsers.Close()
	}
	if s.mcp != nil {
		if cerr := s.mcp.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close mcp: %w", cerr)
		}
	}
	return err
}

func (s *Service) GetTrace(ctx context.Context, threadID string) (*trace.Trace, error) {
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	return s.traces.Load(threadID)
}

func (s *Service) GetTraceSummary(ctx context.Context, threadID string) (*trace.Trace, error) {
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	return s.traces.Summary(threadID)
}

func (s *Service) GetLLMCall(ctx context.Context, threadID string, seq int) (*trace.LLMCallDetail, error) {
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	return s.traces.LLMCall(threadID, seq)
}

// ListThreads lists threads; a signed-in user only sees their own.
func (s *Service) ListThreads(ctx context.Context, f sessions.Filter) ([]sessions.Info, error) {
	if u, ok := auth.UserFrom(ctx); ok {
		f.Owner = u.ID
	}
	list, err := s.sessions.List(ctx, f)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range list {
		list[i].Running = s.busy[list[i].ThreadID]
	}
	return list, nil
}

// RenameThread gives a saved thread a user-chosen title.
func (s *Service) RenameThread(ctx context.Context, threadID, title string) (*sessions.Info, error) {
	title = strings.TrimSpace(title)
	if threadID == "" || title == "" || len([]rune(title)) > 120 || strings.ContainsAny(title, "\r\n") {
		return nil, fmt.Errorf("%w: title must be a single line of 1–120 characters", ErrBadRequest)
	}
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	info, err := s.sessions.SetTitle(ctx, threadID, title)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("%w: %s", ErrThreadNotFound, threadID)
	}
	return info, nil
}

func (s *Service) GetThread(ctx context.Context, threadID string) (*ThreadState, error) {
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	cp, err := s.checkpointer.Latest(ctx, threadID)
	if err != nil || cp == nil {
		return nil, err
	}
	ts := &ThreadState{ThreadID: threadID, Messages: cp.State.Messages, Model: s.defaultModel.spec}
	if info, err := s.sessions.Get(ctx, threadID); err == nil && info != nil && info.Model != "" {
		ts.Model = info.Model
	}
	for _, in := range cp.PendingInterrupts {
		ts.Interrupts = append(ts.Interrupts, toInterrupt(in))
	}
	if v, ok := cp.State.Values[todolist.TodosStateKey]; ok && v != nil {
		if todos, err := todolist.ParseTodos(v); err == nil {
			ts.Todos = todos
		}
	}
	ts.Plan = planmode.PlanOf(&cp.State)
	return ts, nil
}

// GetHistory is the lightweight read path for the GUI. It leaves GetThread
// unchanged for API callers that require the complete resumable state.
func (s *Service) GetHistory(ctx context.Context, threadID string, before, limit int) (*HistoryPage, error) {
	if threadID == "" {
		return nil, fmt.Errorf("%w: thread_id is required", ErrBadRequest)
	}
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	p, err := runtime.LoadHistoryPage(ctx, s.checkpointer, threadID, before, limit)
	if err != nil || p == nil {
		return nil, err
	}
	info, err := s.sessions.Get(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("%w: %s", ErrThreadNotFound, threadID)
	}
	out := &HistoryPage{
		ThreadID: p.ThreadID, CheckpointSeq: p.CheckpointSeq, Messages: p.Messages,
		Interrupts: make([]Interrupt, 0, len(p.PendingInterrupts)), Model: info.Model,
		HasMore: p.HasMore, NextBefore: p.NextBefore,
	}
	if out.Model == "" {
		out.Model = s.defaultModel.spec
	}
	for _, in := range p.PendingInterrupts {
		out.Interrupts = append(out.Interrupts, toInterrupt(in))
	}
	if v, ok := p.Values[todolist.TodosStateKey]; ok && v != nil {
		out.Todos, _ = todolist.ParseTodos(v)
	}
	state := &runtime.State{Values: p.Values}
	out.Plan = planmode.PlanOf(state)
	return out, nil
}

// Compact summarizes the thread's older messages now and saves the result
// as a new checkpoint. The messages stay in state; the summary replaces
// them only in what the model is sent.
func (s *Service) Compact(ctx context.Context, threadID string) (*CompactResult, error) {
	if threadID == "" {
		return nil, fmt.Errorf("%w: thread_id is required", ErrBadRequest)
	}
	release, err := s.acquire(ctx, threadID)
	if err != nil {
		return nil, err
	}
	defer release()

	info, err := s.sessions.Get(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if err := checkOwner(ctx, info); err != nil {
		return nil, err
	}
	cp, err := s.checkpointer.Latest(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if info == nil || cp == nil {
		return nil, fmt.Errorf("%w: %s", ErrThreadNotFound, threadID)
	}
	if len(cp.PendingInterrupts) > 0 {
		return nil, ErrPendingApproval
	}
	if rm, err := s.modelFor(info.Model); err == nil {
		ctx = withRunModel(ctx, rm)
	}
	summ, err := codeagent.NewSummarization(s.model, codeagent.NewProjectBackend(info.Cwd, s.artifacts))
	if err != nil {
		return nil, err
	}
	upd, err := summ.Compact(ctx, &cp.State, s.model)
	if errors.Is(err, summarization.ErrNothingToCompact) {
		return nil, ErrNothingToCompact
	}
	if err != nil {
		return nil, err
	}
	next := *cp
	next.State = *cp.State.Clone()
	if err := next.State.Apply(*upd, summ.StateSchema()); err != nil {
		return nil, err
	}
	next.ID, next.ParentID, next.CreatedAt = "", cp.ID, time.Time{}
	if err := s.checkpointer.Put(ctx, next); err != nil {
		return nil, err
	}
	event, _ := upd.Values[summarization.EventStateKey].(summarization.Event)
	return &CompactResult{ThreadID: threadID, Summary: event.SummaryMessage.Text(), FilePath: event.FilePath}, nil
}

// Run executes one run and yields the messages it adds. A run paused for
// review ends with an EventInterrupt.
func (s *Service) Run(ctx context.Context, req RunRequest) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if err := s.run(ctx, req, yield); err != nil {
			yield(Event{}, err)
		}
	}
}

func (s *Service) run(ctx context.Context, req RunRequest, yield func(Event, error) bool) error {
	if err := s.validate(&req); err != nil {
		return err
	}
	release, err := s.acquire(ctx, req.ThreadID)
	if err != nil {
		return err
	}
	defer release()

	info, err := s.sessions.Get(ctx, req.ThreadID)
	if err != nil {
		return err
	}
	if err := checkOwner(ctx, info); err != nil {
		return err
	}
	if err := s.checkBudget(ctx); err != nil {
		return err
	}
	var ws *workspace.Workspace
	if req.WorkspaceID != "" {
		if info != nil && info.WorkspaceID != req.WorkspaceID {
			return fmt.Errorf("%w: thread %s runs in workspace %q", ErrThreadCwdMismatch, req.ThreadID, info.WorkspaceID)
		}
		var release func()
		if ws, release, err = s.acquireWorkspace(ctx, req.WorkspaceID); err != nil {
			return err
		}
		defer release()
		req.Cwd = ws.Dir
	} else if info != nil && (info.WorkspaceID != "" || info.Cwd != req.Cwd) {
		return fmt.Errorf("%w: thread %s uses %s", ErrThreadCwdMismatch, req.ThreadID, info.Cwd)
	}
	prev, err := s.checkpointer.Latest(ctx, req.ThreadID)
	if err != nil {
		return err
	}
	in, err := buildInput(req, prev)
	if err != nil {
		return err
	}
	rm, err := s.runModelFor(req, info)
	if err != nil {
		return err
	}
	ctx = withRunModel(ctx, rm)
	bt, err := s.browserToolsFor(ctx, req)
	if err != nil {
		return err
	}
	a, err := s.agentFor(ctx, req, bt, ws)
	if err != nil {
		return err
	}
	if err := s.touch(ctx, req, prev, rm.spec); err != nil {
		return err
	}
	s.recordDecisions(ctx, req, prev)
	var runErr error
	started := time.Now()
	s.metrics.runStarted()
	defer func() {
		var before []llm.Message
		if prev != nil {
			before = prev.State.Messages
		}
		var paused *runtime.InterruptError
		failed, outcome := runErr, runOK
		switch {
		case errors.As(runErr, &paused):
			failed, outcome = nil, runInterrupted
		case runErr != nil:
			outcome = runFailed
		}
		spent := 0.0
		if latest, err := s.checkpointer.Latest(context.WithoutCancel(ctx), req.ThreadID); err == nil && latest != nil {
			spent = s.recordRun(ctx, req, rm.spec, before, latest.State.Messages, failed)
		}
		s.metrics.runFinished(outcome, time.Since(started), spent)
	}()

	// sent counts the messages already sent per namespace ("" is the main agent).
	sent := map[string]int{}
	if prev != nil {
		sent[""] = len(prev.State.Messages)
	}
	cfg := runtime.RunConfig{
		ThreadID:     req.ThreadID,
		Configurable: map[string]any{AutoApproveKey: req.AutoApprove, planmode.ModeKey: req.Mode, planmode.BuildKey: req.BuildPlan},
	}
	if req.Resume != nil {
		cfg.Tags = []string{ResumeTraceTag}
	}
	ctx, tr := s.startTrace(ctx, req.ThreadID)
	defer tr.stop()
	ctx = trace.WithBrowserMCP(ctx, string(bt.kind))
	for ev, err := range a.Stream(ctx, in, cfg, runtime.StreamValues, runtime.StreamMessages) {
		if err != nil {
			runErr = err
			break
		}
		if !tr.flush(yield) {
			return nil
		}
		for _, e := range convert(ev, sent) {
			tr.subagentMessage(e)
			if !yield(e, nil) {
				return nil
			}
		}
	}
	if !tr.flush(yield) {
		return nil
	}

	latest, err := s.checkpointer.Latest(context.WithoutCancel(ctx), req.ThreadID)
	if err == nil && latest != nil {
		for _, m := range latest.State.Messages[min(sent[""], len(latest.State.Messages)):] {
			if !yield(Event{Kind: EventMessage, Message: &m}, nil) {
				return nil
			}
		}
		_ = s.touch(context.WithoutCancel(ctx), req, latest, rm.spec)
	}

	var paused *runtime.InterruptError
	if errors.As(runErr, &paused) {
		ev := Event{Kind: EventInterrupt}
		for _, in := range paused.Interrupts {
			ev.Interrupts = append(ev.Interrupts, toInterrupt(in))
		}
		yield(ev, nil)
		return nil
	}
	return runErr
}

// convert maps an agent event to client events. Values events are diffed
// against the number of messages already sent in their namespace, so each
// subagent run is tracked on its own.
func convert(ev runtime.Event, sent map[string]int) []Event {
	ns := slices.Clone(ev.Namespace)
	switch ev.Mode {
	case runtime.StreamMessages:
		if c, ok := ev.Data.(llm.Chunk); ok && !c.Done {
			if text := c.Delta.Text(); text != "" {
				return []Event{{Kind: EventDelta, Text: text, Namespace: ns}}
			}
		}
	case runtime.StreamValues:
		key := strings.Join(ns, "/")
		st, ok := ev.Data.(*runtime.State)
		if !ok || len(st.Messages) <= sent[key] {
			return nil
		}
		var out []Event
		for _, m := range st.Messages[sent[key]:] {
			out = append(out, Event{Kind: EventMessage, Message: &m, Namespace: ns})
		}
		sent[key] = len(st.Messages)
		return out
	}
	return nil
}

func (s *Service) validate(req *RunRequest) error {
	if req.ThreadID == "" {
		return fmt.Errorf("%w: thread_id is required", ErrBadRequest)
	}
	switch {
	case req.WorkspaceID != "" && s.workspaces == nil:
		return fmt.Errorf("%w: this server has no workspaces (start quill serve with -workspaces)", ErrBadRequest)
	case req.WorkspaceID != "":
		req.Cwd = ""
		return validateRest(req)
	case s.requireWS:
		return fmt.Errorf("%w: workspace_id is required; this server does not run in its own directories", ErrBadRequest)
	}
	if !filepath.IsAbs(req.Cwd) {
		return fmt.Errorf("%w: cwd must be an absolute path, got %q", ErrBadRequest, req.Cwd)
	}
	req.Cwd = filepath.Clean(req.Cwd)
	fi, err := os.Stat(req.Cwd)
	if err != nil {
		return fmt.Errorf("%w: cwd: %v", ErrBadRequest, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: cwd %s is not a directory", ErrBadRequest, req.Cwd)
	}
	return validateRest(req)
}

func validateRest(req *RunRequest) error {
	if (len(req.Messages) > 0) == (req.Resume != nil) {
		return fmt.Errorf("%w: exactly one of messages and resume is required", ErrBadRequest)
	}
	switch req.Mode {
	case "", ModeAgent:
	case ModePlan:
		if !req.Interactive {
			return fmt.Errorf("%w: plan mode needs an interactive session", ErrBadRequest)
		}
	default:
		return fmt.Errorf("%w: mode must be %q or %q, got %q", ErrBadRequest, ModeAgent, ModePlan, req.Mode)
	}
	if req.BuildPlan && (req.Mode == ModePlan || req.Resume != nil) {
		return fmt.Errorf("%w: build_plan needs messages and agent mode", ErrBadRequest)
	}
	if req.BrowserMCP != "" {
		if _, err := browser.ParseKind(req.BrowserMCP); err != nil {
			return fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
	}
	return nil
}

func buildInput(req RunRequest, prev *runtime.Checkpoint) (agent.Input, error) {
	pending := prev != nil && len(prev.PendingInterrupts) > 0
	if req.Resume == nil {
		if pending {
			return agent.Input{}, ErrPendingApproval
		}
		return agent.Input{Messages: req.Messages}, nil
	}
	if !pending {
		return agent.Input{}, ErrNoPendingApproval
	}
	resume := map[string]any{}
	for _, in := range prev.PendingInterrupts {
		resume[in.ID] = req.Resume.Decisions
	}
	return agent.Input{Resume: &runtime.Command{Resume: resume}}, nil
}

// acquire gives the caller the thread until release is called, or fails
// with ErrThreadBusy while a run in this or another process has it.
func (s *Service) acquire(ctx context.Context, threadID string) (release func(), err error) {
	s.mu.Lock()
	if s.busy[threadID] {
		s.mu.Unlock()
		return nil, ErrThreadBusy
	}
	s.busy[threadID] = true
	s.mu.Unlock()
	done := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.busy, threadID)
	}
	if s.store.Locks == nil {
		return done, nil
	}
	unlock, err := s.store.Locks.TryLock(ctx, threadID)
	if err != nil {
		done()
		if errors.Is(err, storage.ErrLocked) {
			return nil, ErrThreadBusy
		}
		return nil, err
	}
	return func() {
		unlock()
		done()
	}, nil
}

// agentFor returns the cached agent for the run's cwd, building it at most
// once per key. The build runs outside s.mu so it never delays acquire and
// release for other threads.
func (s *Service) agentFor(ctx context.Context, req RunRequest, bt browserTools, ws *workspace.Workspace) (agent.Agent, error) {
	key := agentKey{cwd: req.Cwd, workspace: req.WorkspaceID, interactive: req.Interactive, browser: bt.key()}
	s.mu.Lock()
	e, ok := s.agents[key]
	if !ok {
		e = &agentEntry{}
		s.agents[key] = e
	}
	s.mu.Unlock()

	e.once.Do(func() {
		var reviewModel llm.ChatModel
		if !req.Interactive {
			reviewModel = s.model
		}
		opts := codeagent.Options{
			ReviewModel:  reviewModel,
			Settings:     s.settings,
			Model:        s.model,
			Cwd:          req.Cwd,
			UserDir:      s.paths.UserDir,
			Checkpointer: s.checkpointer,
			Plans:        s.store.Plans,
			Artifacts:    s.artifacts,
			WebModel:     s.webModel,
			Cache:        s.store.Cache,
			Interactive:  req.Interactive,
			NoVerifyGate: s.noVerifyGate,
			Tracer:       s.tracer,
			MCPTools:     slices.Concat(s.mcpTools, bt.tools),
			BrowserMCP:   string(bt.kind),
			Processes:    s.processes,
		}
		if ws != nil {
			opts.Backend = s.workspaces.Backend(ws)
			opts.ProcessShell = s.workspaces.Processes(ws)
			opts.WorkingDirSection = workspace.WorkingDirSection(ws)
			opts.UserDir = ""
			opts.ExtraTools = append(slices.Clip(opts.ExtraTools), s.createPRTool())
			opts.ExtraInterrupts = map[string]hitl.InterruptOnConfig{
				createPRToolName: {Always: true, Describe: describeCreatePR},
			}
		}
		e.agent, e.err = codeagent.CreateCLIAgent(ctx, opts)
	})
	if e.err != nil {
		s.mu.Lock()
		if s.agents[key] == e {
			delete(s.agents, key)
		}
		s.mu.Unlock()
		return nil, fmt.Errorf("create agent for %s: %w", req.Cwd, e.err)
	}
	return e.agent, nil
}

// runModelFor picks the run's model: the request's, else the one the thread
// last used (falling back to the default if it can no longer be built),
// else the default.
func (s *Service) runModelFor(req RunRequest, info *sessions.Info) (runModel, error) {
	if req.Model != "" {
		return s.modelFor(req.Model)
	}
	if info != nil && info.Model != "" {
		if rm, err := s.modelFor(info.Model); err == nil {
			return rm, nil
		}
	}
	return s.defaultModel, nil
}

func (s *Service) touch(ctx context.Context, req RunRequest, cp *runtime.Checkpoint, model string) error {
	info := sessions.Info{ThreadID: req.ThreadID, Cwd: req.Cwd, WorkspaceID: req.WorkspaceID,
		Owner: auth.OwnerFrom(ctx), UpdatedAt: time.Now(), Model: model}
	var msgs []llm.Message
	if cp != nil {
		msgs = cp.State.Messages
		info.MessageCount = len(msgs)
	}
	for _, m := range append(msgs, req.Messages...) {
		if m.Role == llm.RoleUser {
			info.Title = sessions.Title(m.Text())
			break
		}
	}
	return s.sessions.Touch(ctx, info)
}

// toInterrupt decodes an interrupt value into action requests. After a
// Postgres round trip the value is generic JSON, so it is re-decoded.
func toInterrupt(in runtime.Interrupt) Interrupt {
	out := Interrupt{ID: in.ID}
	switch v := in.Value.(type) {
	case []hitl.ActionRequest:
		out.ActionRequests = v
	default:
		raw, err := json.Marshal(v)
		if err == nil {
			_ = json.Unmarshal(raw, &out.ActionRequests)
		}
	}
	return out
}
