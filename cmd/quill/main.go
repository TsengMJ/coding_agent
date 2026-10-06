// Command quill is the Quill terminal coding agent.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/tsengmingchun/quill"
	"github.com/tsengmingchun/quill/backend/ossandbox"
	codeagent "github.com/tsengmingchun/quill/code/agent"
	"github.com/tsengmingchun/quill/code/client"
	"github.com/tsengmingchun/quill/code/config"
	"github.com/tsengmingchun/quill/code/gui"
	"github.com/tsengmingchun/quill/code/server"
	"github.com/tsengmingchun/quill/code/sessions"
	"github.com/tsengmingchun/quill/code/storage"
	"github.com/tsengmingchun/quill/code/tui"
	"github.com/tsengmingchun/quill/langsmith"
)

const (
	modelFlagUsage   = "model as provider:model (default $" + config.ModelEnvVar + ", models.default in config.toml, or " + server.DefaultModelSpec + ")"
	serverFlagUsage  = "quill serve URL (default $" + config.ServerEnvVar + " or server.url in config.toml)"
	sandboxFlagUsage = "shell sandbox for this run: read-only, workspace-write or full-access (default $" +
		config.SandboxModeEnvVar + " or sandbox.mode in config.toml)"
)

func main() {
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
		r, err := client.NewRemote(serverURL)
		if err != nil {
			return nil, err
		}
		if err := r.Ping(ctx); err != nil {
			return nil, fmt.Errorf("%w (is `quill serve` running?)", err)
		}
		return r, nil
	}
	svc, err := newService(ctx, e, model)
	if err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}
	return svc, nil
}

// newService opens Postgres and Redis, both required, and starts the
// in-process service on them.
func newService(ctx context.Context, e env, model string) (*server.Service, error) {
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
	svc, err := server.New(ctx, cfg)
	if err != nil {
		closeTracer(ls)
		st.Close()
		return nil, err
	}
	return svc, nil
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
	plain := fs.Bool("plain", false, "use the line REPL instead of the GUI")
	useTUI := fs.Bool("tui", false, "use the full-screen terminal UI instead of the GUI")
	noOpen := fs.Bool("no-open", false, "print the GUI URL instead of opening a browser")
	model := fs.String("model", "", modelFlagUsage)
	serverFlag := fs.String("server", "", serverFlagUsage+"; empty runs in-process")
	quiet := fs.Bool("q", false, "with -n: print only the agent's reply (no thread ID or tool lines)")
	noStream := fs.Bool("no-stream", false, "with -n: print the reply once complete instead of token by token")
	timeout := fs.Duration("timeout", 0, "with -n: cancel after this long (e.g. 10m) and exit 124")
	sandboxMode := fs.String("sandbox", "", sandboxFlagUsage)
	version := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage:\n"+
			"  quill [flags] [-n prompt | prompt...]   run the agent in the current directory\n"+
			"                                         (interactive: web GUI; -tui terminal UI; -plain line REPL)\n"+
			"  quill serve [-addr host:port]          run a long-lived agent server\n"+
			"  quill threads [-all] [-json]           list threads\n"+
			"  quill sandbox [-mode m] -- cmd...      run one command in the shell sandbox\n\n"+
			"quill needs Postgres ($QUILL_POSTGRES_CONN_STRING or $POSTGRES_CONN_STRING) for threads,\n"+
			"plans and traces, and Redis ($QUILL_REDIS_URL or $REDIS_URL) for caches; it will not start without both.\n"+
			"Tool calls run without approval by default. With startup.mode = \"manual\" in\n"+
			"%s, tools that change files (write_file, edit_file, apply_patch, delete) or run\n"+
			"commands (execute, run_tests, diagnostics) ask first (-y skips it for one run;\n"+
			"shell.allow_list lets matching commands skip it; allowing go covers run_tests and diagnostics).\n"+
			"Shell commands run in the current directory. With sandbox.mode = \"workspace-write\" (or -sandbox)\n"+
			"they run in an OS sandbox (macOS Seatbelt, Linux bubblewrap) that can write only the project,\n"+
			"temp and build caches, cannot read credential directories such as ~/.ssh, and has no network;\n"+
			"\"read-only\" blocks all writes. In \"full-access\" mode (the default) they are not sandboxed.\n\n", "~/.quill/config.toml")
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
	saveModel := func(spec string) error { return config.SetDefaultModel(e.paths.ConfigFile, spec) }
	defaultModel := modelName(ctx, srv)

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

	if prompt != "" {
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
	switch interactiveUI(*plain, *useTUI, isTerminal(os.Stdin), isTerminal(os.Stdout)) {
	case uiTUI:
		return 0, tui.Run(ctx, tui.Options{
			Server: srv, Cwd: e.cwd, ThreadID: thread, AutoApprove: yolo,
			ModelName: defaultModel, Model: *model, SaveDefaultModel: saveModel,
		})
	case uiGUI:
		if thread == "" {
			thread, err = defaultGUIThread(ctx, srv, e.cwd)
			if err != nil {
				return 1, err
			}
		}
		return 0, runGUI(ctx, srv, serverURL, gui.Info{
			Cwd: e.cwd, ThreadID: thread, Model: defaultModel, ModelOverride: *model, AutoApprove: yolo, Version: quill.Version,
		}, saveModel, *noOpen, stderr)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	return 0, client.RunInteractive(ctx, srv, client.InteractiveOptions{
		ThreadID: thread, Cwd: e.cwd, AutoApprove: yolo,
		Model: *model, ModelName: defaultModel, SaveDefaultModel: saveModel,
		In: os.Stdin, Out: stdout, Err: stderr, Interrupt: sig,
	})
}

// defaultGUIThread opens the newest conversation with saved state in cwd.
// A failed first run may leave a catalog entry but no checkpoint.
func defaultGUIThread(ctx context.Context, srv server.Server, cwd string) (string, error) {
	list, err := srv.ListThreads(ctx, sessions.Filter{Cwd: cwd})
	if err != nil {
		return "", err
	}
	for _, item := range list {
		state, err := srv.GetThread(ctx, item.ThreadID)
		if err != nil {
			return "", err
		}
		if state != nil {
			return item.ThreadID, nil
		}
	}
	return sessions.NewThreadID(), nil
}

type ui int

const (
	uiREPL ui = iota
	uiTUI
	uiGUI
)

// interactiveUI picks the interactive front end: the GUI by default, the
// TUI with -tui on a terminal, and the line REPL with -plain or when stdin
// is not a terminal (scripts piping input).
func interactiveUI(plain, useTUI, stdinTTY, stdoutTTY bool) ui {
	switch {
	case plain || !stdinTTY:
		return uiREPL
	case useTUI && stdoutTTY:
		return uiTUI
	case useTUI:
		return uiREPL
	default:
		return uiGUI
	}
}

// runGUI serves the web UI on a free loopback port until Ctrl-C. The token
// travels in the URL fragment, which browsers never send to servers.
func runGUI(ctx context.Context, srv server.Server, serverURL string, info gui.Info, saveModel func(string) error, noOpen bool, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop)
	token, err := gui.NewToken()
	if err != nil {
		return err
	}
	var api http.Handler = server.NewHandler(srv)
	if serverURL != "" {
		if api, err = gui.RemoteAPI(serverURL); err != nil {
			return err
		}
	}
	h, err := gui.Handler(gui.Options{API: api, Token: token, Info: info, SaveDefaultModel: saveModel})
	if err != nil {
		return err
	}
	return server.ServeHandler(ctx, "127.0.0.1:0", h, func(a net.Addr) {
		url := guiURL(a, token)
		fmt.Fprintf(stderr, "quill GUI: %s\n  thread %s · Ctrl-C to quit\n", url, info.ThreadID)
		if noOpen {
			return
		}
		if err := gui.OpenBrowser(url); err != nil {
			fmt.Fprintf(stderr, "quill: %v; open the URL above yourself\n", err)
		}
	})
}

func guiURL(a net.Addr, token string) string {
	return "http://" + a.String() + "/#token=" + token
}

// modelName is the server's default model spec, or "" if it cannot say.
func modelName(ctx context.Context, srv server.Server) string {
	switch s := srv.(type) {
	case *server.Service:
		return s.DefaultModel()
	case *client.Remote:
		name, _ := s.DefaultModel(ctx)
		return name
	}
	return ""
}

func runServe(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("quill serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", server.DefaultAddr, "listen address (loopback only)")
	model := fs.String("model", "", modelFlagUsage)
	sandboxMode := fs.String("sandbox", "", sandboxFlagUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	if err := e.applySandbox(*sandboxMode); err != nil {
		return err
	}
	svc, err := newService(ctx, e, *model)
	if err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	defer svc.Close()
	context.AfterFunc(ctx, func() {
		fmt.Fprintln(stderr, "quill serve: stopping; running turns get up to 30s to finish (Ctrl-C again to force quit)")
	})
	return server.Serve(ctx, *addr, svc, func(a net.Addr) {
		fmt.Fprintf(stderr, "quill serve listening on http://%s (model %s, storage %s)\n", a, svc.DefaultModel(), svc.Storage())
		fmt.Fprintf(stderr, "  clients: export %s=http://%s\n", client.ServerEnvVar, a)
	})
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
// An empty result means interactive mode.
func resolvePrompt(flagPrompt string, args []string) (string, error) {
	if flagPrompt != "" && len(args) > 0 {
		return "", fmt.Errorf("pass the prompt with -n or as arguments, not both")
	}
	if flagPrompt != "" {
		return flagPrompt, nil
	}
	return strings.Join(args, " "), nil
}
