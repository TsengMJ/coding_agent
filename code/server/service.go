package server

import (
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

	"github.com/tsengmingchun/deepagents_golang/agent"
	codeagent "github.com/tsengmingchun/deepagents_golang/code/agent"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/cost"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/code/storage"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/planmode"
	"github.com/tsengmingchun/deepagents_golang/middleware/summarization"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// AutoApproveKey is the RunConfig.Configurable key that disables review.
const AutoApproveKey = hitl.AutoApproveKey

// Config configures a Service.
type Config struct {
	Settings *config.Settings
	Paths    config.Paths
	// ModelSpec ("provider:model") takes precedence over DCODE_MODEL and
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
}

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

	mu     sync.Mutex
	agents map[agentKey]*agentEntry
	busy   map[string]bool
}

type agentKey struct {
	cwd         string
	interactive bool
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
	s := &Service{
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

// Storage reports where threads are kept: "postgres" or "memory".
func (s *Service) Storage() string { return s.store.Kind }

// ModelInfo identifies the service's default model.
func (s *Service) ModelInfo() llm.ModelInfo { return s.defaultModel.model.Info() }

// DefaultModel is the "provider:model" spec runs use unless the request or
// the thread names another.
func (s *Service) DefaultModel() string { return s.defaultModel.spec }

func (s *Service) Close() error {
	err := s.traces.Close()
	s.store.Close()
	return err
}

func (s *Service) GetTrace(ctx context.Context, threadID string) (*trace.Trace, error) {
	return s.traces.Load(threadID)
}

func (s *Service) GetLLMCall(ctx context.Context, threadID string, seq int) (*trace.LLMCallDetail, error) {
	return s.traces.LLMCall(threadID, seq)
}

func (s *Service) ListThreads(ctx context.Context, f sessions.Filter) ([]sessions.Info, error) {
	return s.sessions.List(ctx, f)
}

// RenameThread gives a saved thread a user-chosen title.
func (s *Service) RenameThread(ctx context.Context, threadID, title string) (*sessions.Info, error) {
	title = strings.TrimSpace(title)
	if threadID == "" || title == "" || len([]rune(title)) > 120 || strings.ContainsAny(title, "\r\n") {
		return nil, fmt.Errorf("%w: title must be a single line of 1–120 characters", ErrBadRequest)
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

// Compact summarizes the thread's older messages now and saves the result
// as a new checkpoint. The messages stay in state; the summary replaces
// them only in what the model is sent.
func (s *Service) Compact(ctx context.Context, threadID string) (*CompactResult, error) {
	if threadID == "" {
		return nil, fmt.Errorf("%w: thread_id is required", ErrBadRequest)
	}
	if !s.acquire(threadID) {
		return nil, ErrThreadBusy
	}
	defer s.release(threadID)

	info, err := s.sessions.Get(ctx, threadID)
	if err != nil {
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
	if err := validate(&req); err != nil {
		return err
	}
	if !s.acquire(req.ThreadID) {
		return ErrThreadBusy
	}
	defer s.release(req.ThreadID)

	info, err := s.sessions.Get(ctx, req.ThreadID)
	if err != nil {
		return err
	}
	if info != nil && info.Cwd != req.Cwd {
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
	a, err := s.agentFor(ctx, req)
	if err != nil {
		return err
	}
	if err := s.touch(ctx, req, prev, rm.spec); err != nil {
		return err
	}

	// sent counts the messages already sent per namespace ("" is the main agent).
	sent := map[string]int{}
	if prev != nil {
		sent[""] = len(prev.State.Messages)
	}
	cfg := runtime.RunConfig{
		ThreadID:     req.ThreadID,
		Configurable: map[string]any{AutoApproveKey: req.AutoApprove, planmode.ModeKey: req.Mode, planmode.BuildKey: req.BuildPlan},
	}
	ctx, tr := s.startTrace(ctx, req.ThreadID)
	defer tr.stop()
	var runErr error
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

func validate(req *RunRequest) error {
	if req.ThreadID == "" {
		return fmt.Errorf("%w: thread_id is required", ErrBadRequest)
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

func (s *Service) acquire(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[threadID] {
		return false
	}
	s.busy[threadID] = true
	return true
}

func (s *Service) release(threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, threadID)
}

// agentFor returns the cached agent for the run's cwd, building it at most
// once per key. The build runs outside s.mu so it never delays acquire and
// release for other threads.
func (s *Service) agentFor(ctx context.Context, req RunRequest) (agent.Agent, error) {
	key := agentKey{cwd: req.Cwd, interactive: req.Interactive}
	s.mu.Lock()
	e, ok := s.agents[key]
	if !ok {
		e = &agentEntry{}
		s.agents[key] = e
	}
	s.mu.Unlock()

	e.once.Do(func() {
		e.agent, e.err = codeagent.CreateCLIAgent(ctx, codeagent.Options{
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
		})
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
	info := sessions.Info{ThreadID: req.ThreadID, Cwd: req.Cwd, UpdatedAt: time.Now(), Model: model}
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
