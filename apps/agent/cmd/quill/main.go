// Command quill runs the Quill coding agent: `quill serve` for the desktop
// app, `quill -n` for one headless prompt.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/tsengmingchun/quill/apps/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend/ossandbox"
	codeagent "github.com/tsengmingchun/quill/apps/agent/code/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/client"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/code/server"
	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/langsmith"
)

const (
	modelFlagUsage   = "model as provider:model (default $" + config.ModelEnvVar + ", models.default in config.toml, or " + server.DefaultModelSpec + ")"
	serverFlagUsage  = "quill serve URL (default $" + config.ServerEnvVar + " or server.url in config.toml)"
	sandboxFlagUsage = "shell sandbox for this run: read-only, workspace-write or full-access (default $" +
		config.SandboxModeEnvVar + " or sandbox.mode in config.toml, else workspace-write on macOS and Linux)"
)

func main() {
	ossandbox.MaybeRunNetBridge()
	code, err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "quill:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) > 0 {
		switch args[0] {
		case "serve":
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			// After the first signal, restore the default so a second one
			// force-quits instead of waiting for running turns.
			context.AfterFunc(ctx, stop)
			return 0, runServe(ctx, args[1:], stderr)
		case "threads":
			return 0, runThreads(ctx, args[1:], stdout)
		case "sandbox":
			return runSandbox(ctx, args[1:], stdout, stderr)
		}
	}
	return runAgent(ctx, args, stdout, stderr)
}

type env struct {
	cwd      string
	paths    config.Paths
	settings *config.Settings
}

// loadEnv loads .env files, then config.toml with environment overrides.
func loadEnv() (env, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return env{}, fmt.Errorf("get working directory: %w", err)
	}
	paths, err := config.DefaultPaths(cwd)
	if err != nil {
		return env{}, fmt.Errorf("resolve paths: %w", err)
	}
	if err := config.LoadDotEnv(paths, cwd); err != nil {
		return env{}, fmt.Errorf("load .env: %w", err)
	}
	r, err := config.NewResolver(paths)
	if err != nil {
		return env{}, err
	}
	settings, err := r.Settings()
	if err != nil {
		return env{}, err
	}
	return env{cwd: cwd, paths: paths, settings: settings}, nil
}

// serverURL returns the -server flag if given, else server.url / $QUILL_SERVER.
func (e env) serverURL(fs *flag.FlagSet, flagValue string) string {
	if flagWasSet(fs, "server") {
		return flagValue
	}
	return e.settings.ServerURL
}

// applySandbox overrides sandbox.mode with the -sandbox flag when given and
// checks that the configured sandbox can run, so a missing bwrap fails at
// startup rather than on the first command.
func (e env) applySandbox(flagValue string) error {
	if flagValue != "" {
		m, err := ossandbox.ParseMode(flagValue)
		if err != nil {
			return fmt.Errorf("-sandbox: %w", err)
		}
		e.settings.Sandbox.Mode = m
	}
	_, _, err := codeagent.NewShellSandbox(e.settings, e.cwd)
	return err
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// connect returns the remote server at serverURL, or an in-process service.
func connect(ctx context.Context, e env, serverURL, model string) (server.Server, error) {
	if serverURL != "" {
		r, err := client.NewRemote(serverURL, server.ClientToken(e.paths.AgentFile, serverURL))
		if err != nil {
			return nil, err
		}
		if err := r.Ping(ctx); err != nil {
			return nil, fmt.Errorf("%w (is `quill serve` running?)", err)
		}
		return r, nil
	}
	svc, err := newService(ctx, e, model, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}
	return svc, nil
}

// newService opens Postgres and Redis, both required, and starts the
// in-process service on them.
// ws, when set, enables workspaces; pub, when set, the team server limits.
func newService(ctx context.Context, e env, model string, ws *workspaceFlags, pub *publicFlags) (*server.Service, error) {
	ls, err := codeagent.NewLangSmith(e.settings)
	if err != nil {
		return nil, err
	}
	cfg := server.Config{Settings: e.settings, Paths: e.paths, ModelSpec: model}
	if ls != nil {
		cfg.Tracer = ls
	}
	st, err := storage.Open(ctx, storage.PostgresConnFromEnv(), storage.RedisURLFromEnv())
	if err != nil {
		closeTracer(ls)
		return nil, err
	}
	cfg.Storage = st
	if pub != nil && pub.enabled {
		cfg.DailyLimitUSD, cfg.Metrics = pub.dailyLimitUSD, pub.metrics
	}
	if ws != nil && ws.root != "" {
		m, err := ws.manager(st.Workspaces)
		if err != nil {
			closeTracer(ls)
			st.Close()
			return nil, err
		}
		cfg.Workspaces, cfg.RequireWorkspace = m, ws.require
		go m.Run(ctx, nil)
	}
	if cfg.MCP, err = connectMCP(ctx, e.paths.MCPConfig); err != nil {
		closeTracer(ls)
		st.Close()
		return nil, err
	}
	svc, err := server.New(ctx, cfg)
	if err != nil {
		closeTracer(ls)
		st.Close()
		if cfg.MCP != nil {
			_ = cfg.MCP.Close()
		}
		return nil, err
	}
	return svc, nil
}

// connectMCP starts the servers in the user's MCP config; nil when none are
// configured. Project directories cannot add servers: a cloned repository
// must not be able to start commands.
func connectMCP(ctx context.Context, path string) (mcp.Session, error) {
	cfgs, err := mcp.LoadConfigs(path)
	if err != nil {
		return nil, err
	}
	if len(cfgs) == 0 {
		return nil, nil
	}
	return mcp.Connect(ctx, cfgs)
}

func closeTracer(ls *langsmith.Client) {
	if ls != nil {
		_ = ls.Close()
	}
}

func runAgent(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("quill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	promptFlag := fs.String("n", "", "run a single prompt non-interactively")
	threadID := fs.String("thread", "", "thread ID to continue")
	resume := fs.Bool("r", false, "continue the most recent thread in this directory")
	autoApprove := fs.Bool("y", false, "approve every tool call without asking, even when startup.mode = \"manual\"")
	model := fs.String("model", "", modelFlagUsage)
	serverFlag := fs.String("server", "", serverFlagUsage+"; empty runs in-process")
	quiet := fs.Bool("q", false, "with -n: print only the agent's reply (no thread ID or tool lines)")
	noStream := fs.Bool("no-stream", false, "with -n: print the reply once complete instead of token by token")
	timeout := fs.Duration("timeout", 0, "with -n: cancel after this long (e.g. 10m) and exit 124")
	sandboxMode := fs.String("sandbox", "", sandboxFlagUsage)
	version := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage:\n"+
			"  quill [flags] [-n prompt | prompt...]   run one prompt in the current directory\n"+
			"  quill serve [-addr host:port]          run the agent server for the desktop app\n"+
			"  quill threads [-all] [-json]           list threads\n"+
			"  quill sandbox [-mode m] -- cmd...      run one command in the shell sandbox\n\n"+
			"quill needs Postgres ($QUILL_POSTGRES_CONN_STRING or $POSTGRES_CONN_STRING) for threads,\n"+
			"plans and traces, and Redis ($QUILL_REDIS_URL or $REDIS_URL) for caches; it will not start without both.\n"+
			"Tool calls run without approval by default. With startup.mode = \"manual\" in\n"+
			"%s, tools that change files (write_file, edit_file, apply_patch, delete) or run\n"+
			"commands (execute, run_tests, diagnostics) ask first (-y skips it for one run;\n"+
			"shell.allow_list lets matching commands skip it; allowing go covers run_tests and diagnostics).\n"+
			"Shell commands run in the current directory, inside an OS sandbox (macOS Seatbelt, Linux bubblewrap)\n"+
			"by default (sandbox.mode = \"workspace-write\"): they can write only the project, temp and build\n"+
			"caches, cannot read credential directories such as ~/.ssh, and have no network (sandbox.network =\n"+
			"\"allowlist\" allows package registries through a proxy). Sandboxed commands skip approval except\n"+
			"destructive ones and requests to run outside the sandbox. \"read-only\" blocks all writes;\n"+
			"\"full-access\" (the default on other platforms) does not sandbox them.\n\n", "~/.quill/config.toml")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if *version {
		fmt.Fprintln(stdout, "quill (go)", quill.Version)
		return 0, nil
	}
	if *resume && *threadID != "" {
		return 2, errors.New("pass -r or -thread, not both")
	}
	prompt, err := resolvePrompt(*promptFlag, fs.Args())
	if err != nil {
		return 2, err
	}
	if err := checkHeadlessFlags(prompt, *quiet, *noStream, *timeout); err != nil {
		return 2, err
	}
	if prompt == "" {
		return 2, errors.New("no prompt; pass -n 'prompt', or run `quill serve` and use the desktop app (apps/desktop)")
	}
	e, err := loadEnv()
	if err != nil {
		return 1, err
	}
	serverURL := e.serverURL(fs, *serverFlag)
	if serverURL == "" {
		if err := e.applySandbox(*sandboxMode); err != nil {
			return 1, err
		}
	}
	srv, err := connect(ctx, e, serverURL, *model)
	if err != nil {
		return 1, err
	}
	defer srv.Close()
	yolo := *autoApprove || e.settings.ApprovalMode == config.ApprovalYolo

	thread := *threadID
	if *resume {
		latest, err := srv.ListThreads(ctx, sessions.Filter{Cwd: e.cwd, Limit: 1})
		if err != nil {
			return 1, err
		}
		if len(latest) == 0 {
			fmt.Fprintln(stderr, "quill: no previous thread in this directory; starting a new one")
		} else {
			thread = latest[0].ThreadID
			fmt.Fprintf(stderr, "resuming thread %s (%s)\n", thread, latest[0].Title)
		}
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	var approver client.Approver
	if !yolo && isTerminal(os.Stdin) {
		approver = client.PromptApprover{In: bufio.NewReader(os.Stdin), Out: stderr, Cwd: e.cwd}
	}
	return client.RunNonInteractive(ctx, srv, client.NonInteractiveOptions{
		Prompt: prompt, ThreadID: thread, Cwd: e.cwd, AutoApprove: yolo, Model: *model,
		Approver: approver, Quiet: *quiet, NoStream: *noStream, Timeout: *timeout,
		Stdout: stdout, Stderr: stderr,
	})
}

func runServe(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("quill serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", server.DefaultAddr, "listen address (loopback only, unless -public)")
	model := fs.String("model", "", modelFlagUsage)
	sandboxMode := fs.String("sandbox", "", sandboxFlagUsage)
	var ws workspaceFlags
	ws.register(fs)
	var pub publicFlags
	pub.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := pub.check(&ws); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	if pub.enabled {
		// Commands run in workspace containers, never under the host
		// sandbox; its file guard (.git/hooks, .git/config) still applies.
		e.settings.Sandbox.OnUnavailable = config.SandboxUnavailableWarn
	}
	if err := e.applySandbox(*sandboxMode); err != nil {
		return err
	}
	if pub.enabled {
		return servePublic(ctx, e, *addr, *model, &ws, &pub, stderr)
	}
	token, err := server.ServeToken()
	if err != nil {
		return err
	}
	svc, err := newService(ctx, e, *model, &ws, nil)
	if err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	defer svc.Close()
	h, err := server.NewAppHandler(server.AppOptions{
		API:     server.NewHandler(svc),
		Token:   token,
		Browser: svc,
		Info: func(ctx context.Context) (server.Info, error) {
			thread, err := server.DefaultThread(ctx, svc, e.cwd)
			if err != nil {
				return server.Info{}, err
			}
			return server.Info{
				Cwd: e.cwd, ThreadID: thread, Model: svc.DefaultModel(),
				AutoApprove: e.settings.ApprovalMode == config.ApprovalYolo, Version: quill.Version,
			}, nil
		},
		SaveDefaultModel: func(spec string) error { return config.SetDefaultModel(e.paths.ConfigFile, spec) },
	})
	if err != nil {
		return err
	}
	context.AfterFunc(ctx, func() {
		fmt.Fprintln(stderr, "quill serve: stopping; running turns get up to 30s to finish (Ctrl-C again to force quit)")
	})
	pid := os.Getpid()
	defer server.RemoveAgentFile(e.paths.AgentFile, pid)
	err = server.ServeHandler(ctx, *addr, h, func(a net.Addr) {
		url := "http://" + a.String()
		if err := server.WriteAgentFile(e.paths.AgentFile, server.AgentFile{URL: url, Token: token, PID: pid}); err != nil {
			fmt.Fprintf(stderr, "quill serve: %v; clients need $%s\n", err, server.AgentTokenEnvVar)
		}
		fmt.Fprintf(stderr, "quill serve listening on %s (model %s, storage %s)\n", url, svc.DefaultModel(), svc.Storage())
		fmt.Fprintf(stderr, "  URL and token written to %s for the desktop app and `quill -server %s`\n", e.paths.AgentFile, url)
	})
	return err
}

func runThreads(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("quill threads", flag.ContinueOnError)
	all := fs.Bool("all", false, "list threads from every directory")
	limit := fs.Int("limit", 20, "maximum threads to list (0 = no limit)")
	asJSON := fs.Bool("json", false, `print {"schema_version":1,"command":"threads list","data":[...]}`)
	serverFlag := fs.String("server", "", serverFlagUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	srv, err := connect(ctx, e, e.serverURL(fs, *serverFlag), "")
	if err != nil {
		return err
	}
	defer srv.Close()
	f := sessions.Filter{Limit: *limit}
	if !*all {
		f.Cwd = e.cwd
	}
	list, err := srv.ListThreads(ctx, f)
	if err != nil {
		return err
	}
	if *asJSON {
		if list == nil {
			list = []sessions.Info{}
		}
		return client.WriteJSON(stdout, "threads list", list)
	}
	client.PrintThreads(stdout, list, *all)
	return nil
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// checkHeadlessFlags rejects -q, -no-stream and -timeout outside a -n run.
func checkHeadlessFlags(prompt string, quiet, noStream bool, timeout time.Duration) error {
	if timeout < 0 {
		return fmt.Errorf("-timeout must be positive, got %s", timeout)
	}
	if prompt != "" {
		return nil
	}
	var used []string
	if quiet {
		used = append(used, "-q")
	}
	if noStream {
		used = append(used, "-no-stream")
	}
	if timeout > 0 {
		used = append(used, "-timeout")
	}
	if len(used) > 0 {
		return fmt.Errorf("%s requires -n or a prompt argument, e.g. quill -q -n 'summarize README.md'", strings.Join(used, " and "))
	}
	return nil
}

// resolvePrompt takes the prompt from -n or from leftover arguments.
func resolvePrompt(flagPrompt string, args []string) (string, error) {
	if flagPrompt != "" && len(args) > 0 {
		return "", fmt.Errorf("pass the prompt with -n or as arguments, not both")
	}
	if flagPrompt != "" {
		return flagPrompt, nil
	}
	return strings.Join(args, " "), nil
}
