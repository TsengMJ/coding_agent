// Package agent builds the coding agent used by the CLI: system prompt,
// tools, backend routes, subagents and CLI-specific middleware.
package agent

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent"
	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/code/askuser"
	"github.com/tsengmingchun/quill/apps/agent/code/cache"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/middleware/planmode"
	"github.com/tsengmingchun/quill/apps/agent/middleware/skills"
	"github.com/tsengmingchun/quill/apps/agent/middleware/subagents"
	"github.com/tsengmingchun/quill/apps/agent/middleware/todolist"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
	"github.com/tsengmingchun/quill/apps/agent/tracing"
)

//go:embed system_prompt.md
var systemPromptTemplate string

// Options configures CreateCLIAgent.
type Options struct {
	Settings  *config.Settings
	Model     llm.ChatModel
	AgentName string
	// Cwd is the project root; file tools see it as "/" and commands run in it.
	Cwd string
	// UserDir is ~/.quill; its <agent>/AGENTS.md is loaded as memory.
	// Empty skips user-level memory.
	UserDir string
	// Backend defaults to a LocalShellBackend rooted at Cwd in virtual mode.
	Backend backend.Backend
	// ExtraTools are added after the web tools.
	ExtraTools []tool.Tool
	// BrowserMCP names the MCP server behind the browser panel tools in
	// MCPTools ("playwright", "chrome-devtools"); empty without the panel.
	BrowserMCP string
	// MCPTools come from connected MCP servers; unless AutoApprove, each
	// needs approval except those in Settings.MCPAllowTools.
	MCPTools []tool.Tool
	// WebModel reads fetched pages for fetch_url prompts; nil uses Model.
	WebModel llm.ChatModel
	// ReviewModel, when set, runs the independent reviewer the headless
	// gate asks once before a turn with changes finishes.
	ReviewModel llm.ChatModel
	// Cache holds web lookups; nil disables caching.
	Cache cache.Cache
	// NoWebTools leaves out web_search and fetch_url (offline evaluation).
	NoWebTools   bool
	Checkpointer rt.Checkpointer
	Store        rt.Store
	// Plans saves plan mode's plans; required when Interactive.
	Plans planmode.Store
	// Artifacts holds offloaded tool results, fetched pages and history;
	// nil keeps them in the Backend under ArtifactsRoot.
	Artifacts *Artifacts
	// AutoApprove disables human review of tool calls (YOLO mode).
	AutoApprove bool
	// Interactive tailors the prompt for a human in the loop; false means headless.
	Interactive bool
	// NoVerifyGate leaves out the VerifyGate a headless run gets by default.
	NoVerifyGate bool
	// ProjectContext overrides the live "Project Context" section when set.
	ProjectContext string
	// WorkingDirSection replaces the rendered working-directory and path
	// section, for a Backend whose commands do not run in Cwd on this host.
	WorkingDirSection string
	// Tracer records run trees (see NewLangSmith); nil turns tracing off.
	Tracer tracing.Tracer
	// Disable turns features off by name (see Features), for ablation runs.
	Disable []string
	// OnBaseline receives every verdict of the headless gate's HEAD
	// baseline, so a benchmark can record why a failing turn finished.
	OnBaseline func(verdict string, tests []string)
	// OnReview receives every review of the headless gate's reviewer and
	// every reviewer error.
	OnReview func(Review, error)
}

// CreateCLIAgent assembles the coding agent on top of quill.CreateAgent.
func CreateCLIAgent(ctx context.Context, opts Options) (core.Agent, error) {
	if opts.Model == nil {
		return nil, errors.New("code/agent.CreateCLIAgent: Model is required")
	}
	if opts.Cwd == "" {
		return nil, errors.New("code/agent.CreateCLIAgent: Cwd is required")
	}
	if err := checkFeatures(opts.Disable); err != nil {
		return nil, fmt.Errorf("code/agent.CreateCLIAgent: %w", err)
	}
	b := opts.Backend
	if b == nil {
		var err error
		if b, err = NewSandboxedBackend(opts.Cwd, opts.Settings); err != nil {
			return nil, fmt.Errorf("code/agent.CreateCLIAgent: %w", err)
		}
	}
	sandboxed := shellSandboxed(b)
	b = withHostRoutes(b, userSkillRoots(opts.UserDir, opts.AgentName), opts.Artifacts, opts.Cwd)
	skillSources := existingSkillSources(ctx, b, SkillSources(opts.UserDir, opts.AgentName))
	b = backend.WithVersionGuard(trace.WrapBackend(b))
	promptOpts := opts
	promptOpts.Model = nil
	promptOpts.ProjectContext = ""
	prompt, err := SystemPrompt(promptOpts)
	if err != nil {
		return nil, err
	}
	mem, err := newMemory(b, opts.UserDir, MemorySources(opts.UserDir, opts.AgentName), opts.Interactive)
	if err != nil {
		return nil, err
	}
	summ, err := NewSummarization(opts.Model, b)
	if err != nil {
		return nil, err
	}
	var mws []core.Middleware
	if !opts.Interactive && !slices.Contains(opts.Disable, FeatureBudget) {
		mws = append(mws, newBudgetNotice())
	}
	if !slices.Contains(opts.Disable, FeatureProgress) {
		mws = append(mws, ProgressMonitor{})
	}
	if !slices.Contains(opts.Disable, FeatureFailureClass) {
		mws = append(mws, FailureClassifier{})
	}
	var planner Planner
	if p := newPlanner(b, opts.Disable); p != nil {
		planner = p
	}
	if !slices.Contains(opts.Disable, FeatureRepairState) {
		mws = append(mws, RepairTracker{Planner: planner})
	}
	if !slices.Contains(opts.Disable, FeatureStuck) {
		mws = append(mws, StuckDetector{Enforce: !opts.Interactive})
	}
	if sb, ok := b.(backend.SandboxBackend); ok && !slices.Contains(opts.Disable, FeatureCheckpoint) {
		if _, inGit := git(ctx, opts.Cwd, "rev-parse", "--is-inside-work-tree"); inGit {
			mws = append(mws, CheckpointManager{Workspace: sb})
		}
	}
	if !slices.Contains(opts.Disable, FeatureMicro) {
		sb, _ := b.(backend.SandboxBackend)
		mws = append(mws, MicroCheck{Files: b, Workspace: sb})
	}
	if !opts.Interactive && !slices.Contains(opts.Disable, FeatureRepro) {
		mws = append(mws, ReproReminder{})
	}
	mws = append(mws, summ, todolist.New("", ""), mem, &liveContext{cwd: opts.Cwd, model: opts.Model, project: opts.ProjectContext})
	if len(skillSources) > 0 {
		sk, err := skills.New(skills.Options{Backend: b, Sources: skillSources})
		if err != nil {
			return nil, err
		}
		mws = append(mws, sk)
	}
	if !opts.Interactive && !opts.NoVerifyGate {
		g := newVerifyGate(b, opts.Disable)
		g.OnBaseline = opts.OnBaseline
		g.Planner = planner
		if opts.ReviewModel != nil && g.Workspace != nil && !slices.Contains(opts.Disable, FeatureReviewer) {
			if g.Reviewer, err = NewModelReviewer(opts.ReviewModel, b); err != nil {
				return nil, err
			}
			g.OnReview = opts.OnReview
		}
		mws = append(mws, g)
	}
	var tools []tool.Tool
	webModel := opts.WebModel
	if webModel == nil {
		webModel = opts.Model
	}
	var subs []subagents.Spec
	if !opts.NoWebTools {
		tools = WebTools(WebOptions{
			Settings: opts.Settings, Extractor: trace.Labeled(webModel, trace.WebExtract),
			Pages: opts.Artifacts.WebPages(opts.Cwd), Cache: opts.Cache,
		})
		if hasWebResearcher(opts) {
			subs = append(subs, webResearcher(webModel, tools))
		}
	}
	codeTools := codetools.New(b)
	tools = append(tools, codeTools...)
	subs = append([]subagents.Spec{explorer(opts.Model, codeTools)}, subs...)
	custom, err := customSubagents(opts)
	if err != nil {
		return nil, err
	}
	subs = append(subs, custom...)
	interrupts := map[string]hitl.InterruptOnConfig{}
	switch {
	case !opts.AutoApprove:
		interrupts = InterruptOn(opts.Settings)
		if sandboxed {
			reviewSandboxedShell(interrupts)
		}
		var allow []string
		if opts.Settings != nil {
			allow = opts.Settings.MCPAllowTools
		}
		reviewMCPTools(interrupts, opts.MCPTools, allow)
	case opts.Interactive:
		interrupts["execute"] = destructiveOnly()
		if sandboxed {
			interrupts["execute"] = sandboxedExecuteReview()
		}
	default:
		mws = append(mws, destructiveGuard{})
	}
	if sandboxed {
		mws = append(mws, sandboxedShell{
			mode:             opts.Settings.Sandbox.Mode,
			network:          opts.Settings.Sandbox.Network,
			allowUnsandboxed: opts.Settings.Sandbox.AllowUnsandboxed,
			noReviewer:       opts.AutoApprove && !opts.Interactive,
		})
	}
	if opts.Interactive {
		var readOnly []string
		for _, s := range subs {
			if slices.Contains(ReadOnlySubagents, s.SubAgentName()) {
				readOnly = append(readOnly, s.SubAgentName())
			}
		}
		pm, err := planmode.New(planmode.Options{Store: opts.Plans, ReadOnlySubagents: readOnly})
		if err != nil {
			return nil, err
		}
		mws = append(mws, pm, askuser.Middleware{})
		interrupts[askuser.ToolName] = askuser.InterruptOn()
	}
	dopts := []quill.Option{
		quill.WithModel(opts.Model),
		quill.WithBackend(b),
		quill.WithSystemPrompt(prompt),
		quill.WithTools(slices.Concat(tools, opts.ExtraTools, opts.MCPTools)...),
		quill.WithMiddleware(append(mws, trace.Middleware{})...),
		quill.WithCheckpointer(opts.Checkpointer),
		quill.WithStore(opts.Store),
		quill.WithName(opts.AgentName),
		quill.WithSubAgents(subs...),
		quill.WithBackgroundTasks(),
		quill.WithTracer(opts.Tracer),
	}
	if opts.Settings != nil && opts.Settings.Sandbox.Enabled() {
		dopts = append(dopts, quill.WithMiddleware(sandboxFileGuard{mode: opts.Settings.Sandbox.Mode}))
	}
	if len(interrupts) > 0 {
		dopts = append(dopts, quill.WithInterruptOn(interrupts))
	}
	return quill.CreateAgent(dopts...)
}

// DefaultInterruptOn requires approval for tools that change files, run
// commands or reach the network. Per run, hitl.AutoApproveKey in RunConfig.Configurable skips it.
func DefaultInterruptOn() map[string]hitl.InterruptOnConfig {
	return map[string]hitl.InterruptOnConfig{
		"write_file":  {},
		"edit_file":   {},
		"apply_patch": {},
		"delete": {
			AllowedDecisions: []hitl.DecisionType{hitl.DecisionApprove, hitl.DecisionReject},
			Description:      "Action: Delete file or directory",
		},
		RollbackToolName: {
			AllowedDecisions: []hitl.DecisionType{hitl.DecisionApprove, hitl.DecisionReject},
			Description:      "Action: Restore the workspace to a checkpoint (files changed since are overwritten, files created since are deleted)",
		},
		"execute":                 {AlwaysWhen: isDestructiveCall, Describe: describeExecute},
		codetools.RunTestsName:    {},
		codetools.DiagnosticsName: {},
		"web_search":              {Describe: FormatWebSearchDescription},
		"fetch_url":               {Describe: FormatFetchURLDescription},
		"resolve_library_id":      {Describe: FormatContext7Description},
		"query_docs":              {Describe: FormatContext7Description},
		"task":                    {When: isWebResearch, Describe: FormatWebResearchDescription},
	}
}

// runnerTools run fixed commands whose arguments the tools validate; which
// runner depends on the project, so the allow list must permit all of them.
var runnerTools = map[string][]string{
	codetools.RunTestsName:    {"go", "npx", "npm", "node", "python3", "python"},
	codetools.DiagnosticsName: {"go", "npx", "node", "python3", "python", "ruff"},
}

// InterruptOn is DefaultInterruptOn narrowed by the shell allow list:
// allow-listed execute calls run without review, and "all" drops review of
// execute entirely. run_tests and diagnostics skip review when every runner
// they may start is allowed.
func InterruptOn(s *config.Settings) map[string]hitl.InterruptOnConfig {
	cfg := DefaultInterruptOn()
	switch {
	case s == nil:
	case s.ShellAllowAll:
		cfg["execute"] = destructiveOnly()
		for name := range runnerTools {
			delete(cfg, name)
		}
	case len(s.ShellAllowList) > 0:
		allow := s.ShellAllowList
		cfg["execute"] = hitl.InterruptOnConfig{AlwaysWhen: isDestructiveCall, Describe: describeExecute, When: func(call llm.ToolCall) bool {
			cmd, _ := call.Args["command"].(string)
			return !IsShellCommandAllowed(cmd, allow, false)
		}}
		for name, runners := range runnerTools {
			if slices.ContainsFunc(runners, func(r string) bool { return !IsShellCommandAllowed(r, allow, false) }) {
				continue
			}
			delete(cfg, name)
		}
	}
	return cfg
}

// PromptTemplateHash fingerprints the system prompt template, so bench runs
// built from a dirty tree record which prompt they used.
func PromptTemplateHash() string {
	sum := sha256.Sum256([]byte(systemPromptTemplate))
	return hex.EncodeToString(sum[:])
}

// SystemPrompt renders the CLI system prompt (mode, model identity, cwd,
// project context).
func SystemPrompt(opts Options) (string, error) {
	if opts.Cwd == "" {
		return "", errors.New("code/agent.SystemPrompt: Cwd is required")
	}
	wd := opts.WorkingDirSection
	if wd == "" {
		wd = workingDirSection(opts.Cwd)
	}
	r := strings.NewReplacer(
		"{mode_description}", pick(opts.Interactive, "an interactive desktop app on the user's computer",
			"non-interactive (headless) mode — there is no human operator monitoring your output in real time"),
		"{interactive_preamble}", pick(opts.Interactive,
			"The user sends you messages and you respond with text and tool calls. The user can see your responses and tool outputs in real time, so keep them informed — but don't over-explain.",
			"You received a single task and must complete it fully and autonomously. There is no human available to answer follow-up questions, so do NOT ask for clarification — make reasonable assumptions and proceed."),
		"{ambiguity_guidance}", pick(opts.Interactive,
			"- If the request is ambiguous, ask questions before acting.\n- If asked how to approach something, explain first, then act.",
			"- Do NOT ask clarifying questions — there is no human to answer them. Make reasonable assumptions and proceed.\n"+
				"- If you encounter ambiguity, choose the most reasonable interpretation and note your assumption briefly.\n"+
				"- Always use non-interactive command variants — no human is available to respond to prompts. Examples: `npm init -y` not `npm init`, `--no-input`/`--non-interactive` flags where available. Never run commands that block waiting for stdin."),
		"{blocked_task_guidance}", pick(opts.Interactive, "Only ask when genuinely blocked.",
			"If essential information cannot be obtained from available sources, report the blocker and any completed work. Do not invent required identifiers or permissions."),
		"{substitution_guidance}", pick(opts.Interactive, "Don't substitute without asking.",
			"If a required tool or dependency is unavailable, report the blocker instead of silently substituting another."),
		"{failure_recovery_guidance}", pick(opts.Interactive,
			"- On the third attempt, stop and ask the user what to do\n- If you notice yourself going in circles, stop and ask the user for help",
			"- After repeated failures, use a different permitted approach. If none is available, report the blocker and any completed work."),
		"{model_identity_section}", modelIdentitySection(opts.Model),
		"{working_dir_section}", wd,
		"{subagents_section}", subagentsSection(opts),
		"{web_search_section}", webSearchSection(opts),
		"{project_context_section}", opts.ProjectContext,
	)
	return r.Replace(systemPromptTemplate) + browserSection(opts.BrowserMCP), nil
}

// browserSection is appended rather than templated so prompts without the
// browser panel (headless runs, benchmarks) stay byte-identical.
func browserSection(kind string) string {
	if kind == "" {
		return ""
	}
	return "\n\n### Browser Panel\n\n" +
		"The `mcp__" + kind + "__*` tools drive a browser panel the user watches next to this conversation. " +
		"Use it to check web work yourself: start the dev server, open its page, interact with it and read the result instead of assuming it works. " +
		"The panel only loads http://localhost and http://127.0.0.1 pages and has no logins or cookies. " +
		"Prefer accessibility snapshots for finding elements; take a screenshot when the visual result matters.\n"
}

func pick(interactive bool, yes, no string) string {
	if interactive {
		return yes
	}
	return no
}

func modelIdentitySection(m llm.ChatModel) string {
	if m == nil || m.Info().Model == "" {
		return ""
	}
	info := m.Info()
	s := fmt.Sprintf("### Model Identity\n\nYou are running as model `%s`", info.Model)
	if info.Provider != "" {
		s += fmt.Sprintf(" (provider: %s)", info.Provider)
	}
	s += ".\n"
	if limit := m.Profile().MaxInputTokens; limit > 0 {
		s += fmt.Sprintf("Your context window is %d tokens.\n", limit)
	}
	return s + "\n"
}

func workingDirSection(cwd string) string {
	return fmt.Sprintf("### Current Working Directory\n\n"+
		"The project root is `%s` on %s.\n\n"+
		"### File System and Paths\n\n"+
		"**IMPORTANT - Path Handling:**\n"+
		"- File tools (`ls`, `read_file`, `write_file`, `edit_file`, `delete`, `glob`, `grep`) use virtual absolute paths where `/` is the project root.\n"+
		"- Example: to create `%s/cmd/app/main.go`, call `write_file(file_path=\"/cmd/app/main.go\", ...)`.\n"+
		"- File tools cannot reach paths outside the project root.\n"+
		"- `execute` runs shell commands with the project root as the working directory, so use paths relative to it there (e.g. `go test ./...`).\n\n",
		cwd, runtime.GOOS, cwd)
}

// ListAgents returns agent names found under the user agents directory.
func ListAgents(p config.Paths) ([]string, error) {
	return nil, notimpl.Err("code/agent.ListAgents")
}

// ResetAgent restores an agent's AGENTS.md to defaults or copies another agent.
func ResetAgent(p config.Paths, name, copyFrom string) error {
	return notimpl.Err("code/agent.ResetAgent")
}
