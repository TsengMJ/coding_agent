package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tsengmingchun/deepagents_golang/agent"
	codeagent "github.com/tsengmingchun/deepagents_golang/code/agent"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
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
	// PostgresConn stores threads in Postgres; empty keeps them in memory.
	PostgresConn string
}

// Service runs the coding agent for many threads and working directories.
type Service struct {
	settings     *config.Settings
	paths        config.Paths
	model        llm.ChatModel
	checkpointer runtime.Checkpointer
	sessions     sessions.Store
	storage      string
	closeFn      func()

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

// New builds the model and thread storage.
func New(ctx context.Context, cfg Config) (*Service, error) {
	model := cfg.Model
	if model == nil {
		spec := resolveModelSpec(cfg)
		m, err := llm.InitChatModel(spec, modelOptions(cfg))
		if err != nil {
			return nil, fmt.Errorf("init model %q: %w", spec, err)
		}
		model = m
	}
	s := &Service{
		settings: cfg.Settings,
		paths:    cfg.Paths,
		model:    model,
		agents:   map[agentKey]*agentEntry{},
		busy:     map[string]bool{},
		closeFn:  func() {},
	}
	if cfg.PostgresConn == "" {
		s.checkpointer = runtime.NewMemoryCheckpointer()
		s.sessions = sessions.NewMemoryStore()
		s.storage = "memory"
		return s, nil
	}
	pg, err := runtime.NewPostgresCheckpointer(ctx, cfg.PostgresConn)
	if err != nil {
		return nil, err
	}
	store, err := sessions.NewPostgresStore(ctx, pg.Pool())
	if err != nil {
		pg.Close()
		return nil, err
	}
	s.checkpointer, s.sessions, s.storage, s.closeFn = pg, store, "postgres", pg.Close
	return s, nil
}

// Storage reports where threads are kept: "postgres" or "memory".
func (s *Service) Storage() string { return s.storage }

// ModelInfo identifies the model the service runs.
func (s *Service) ModelInfo() llm.ModelInfo { return s.model.Info() }

func (s *Service) Close() error {
	s.closeFn()
	return nil
}

func (s *Service) ListThreads(ctx context.Context, f sessions.Filter) ([]sessions.Info, error) {
	return s.sessions.List(ctx, f)
}

func (s *Service) GetThread(ctx context.Context, threadID string) (*ThreadState, error) {
	cp, err := s.checkpointer.Latest(ctx, threadID)
	if err != nil || cp == nil {
		return nil, err
	}
	ts := &ThreadState{ThreadID: threadID, Messages: cp.State.Messages}
	for _, in := range cp.PendingInterrupts {
		ts.Interrupts = append(ts.Interrupts, toInterrupt(in))
	}
	return ts, nil
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
	a, err := s.agentFor(ctx, req)
	if err != nil {
		return err
	}
	if err := s.touch(ctx, req, prev); err != nil {
		return err
	}

	sent := 0
	if prev != nil {
		sent = len(prev.State.Messages)
	}
	cfg := runtime.RunConfig{
		ThreadID:     req.ThreadID,
		Configurable: map[string]any{AutoApproveKey: req.AutoApprove},
	}
	var runErr error
	for ev, err := range a.Stream(ctx, in, cfg, runtime.StreamValues, runtime.StreamMessages) {
		if err != nil {
			runErr = err
			break
		}
		for _, e := range convert(ev, &sent) {
			if !yield(e, nil) {
				return nil
			}
		}
	}

	latest, err := s.checkpointer.Latest(context.WithoutCancel(ctx), req.ThreadID)
	if err == nil && latest != nil {
		for _, m := range latest.State.Messages[min(sent, len(latest.State.Messages)):] {
			if !yield(Event{Kind: EventMessage, Message: &m}, nil) {
				return nil
			}
		}
		_ = s.touch(context.WithoutCancel(ctx), req, latest)
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
// against the number of messages already sent.
func convert(ev runtime.Event, sent *int) []Event {
	switch ev.Mode {
	case runtime.StreamMessages:
		if c, ok := ev.Data.(llm.Chunk); ok && !c.Done {
			if text := c.Delta.Text(); text != "" {
				return []Event{{Kind: EventDelta, Text: text}}
			}
		}
	case runtime.StreamValues:
		st, ok := ev.Data.(*runtime.State)
		if !ok || len(st.Messages) <= *sent {
			return nil
		}
		var out []Event
		for _, m := range st.Messages[*sent:] {
			out = append(out, Event{Kind: EventMessage, Message: &m})
		}
		*sent = len(st.Messages)
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
			Interactive:  req.Interactive,
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

func (s *Service) touch(ctx context.Context, req RunRequest, cp *runtime.Checkpoint) error {
	info := sessions.Info{ThreadID: req.ThreadID, Cwd: req.Cwd, UpdatedAt: time.Now()}
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
