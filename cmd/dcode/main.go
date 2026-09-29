// Command dcode is the Go port of the Deep Agents Code terminal agent.
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

	"github.com/tsengmingchun/deepagents_golang"
	"github.com/tsengmingchun/deepagents_golang/code/client"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/code/tui"
)

// postgresEnvVar selects Postgres thread storage.
const postgresEnvVar = "POSTGRES_CONN_STRING"

const (
	modelFlagUsage  = "model as provider:model (default $" + config.ModelEnvVar + ", models.default in config.toml, or " + server.DefaultModelSpec + ")"
	serverFlagUsage = "dcode serve URL (default $" + config.ServerEnvVar + " or server.url in config.toml)"
)

func main() {
	code, err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dcode:", err)
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

// serverURL returns the -server flag if given, else server.url / $DCODE_SERVER.
func (e env) serverURL(fs *flag.FlagSet, flagValue string) string {
	if flagWasSet(fs, "server") {
		return flagValue
	}
	return e.settings.ServerURL
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
func connect(ctx context.Context, e env, serverURL, model string, stderr io.Writer) (server.Server, error) {
	if serverURL != "" {
		if model != "" {
			fmt.Fprintln(stderr, "dcode: -model is ignored when connecting to a server; pass it to `dcode serve`")
		}
		r, err := client.NewRemote(serverURL)
		if err != nil {
			return nil, err
		}
		if err := r.Ping(ctx); err != nil {
			return nil, fmt.Errorf("%w (is `dcode serve` running?)", err)
		}
		return r, nil
	}
	svc, err := newService(ctx, e, model)
	if err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}
	return svc, nil
}

func newService(ctx context.Context, e env, model string) (*server.Service, error) {
	return server.New(ctx, server.Config{
		Settings: e.settings, Paths: e.paths, ModelSpec: model, PostgresConn: os.Getenv(postgresEnvVar),
	})
}

func runAgent(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("dcode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	promptFlag := fs.String("n", "", "run a single prompt non-interactively")
	threadID := fs.String("thread", "", "thread ID to continue")
	resume := fs.Bool("r", false, "continue the most recent thread in this directory")
	autoApprove := fs.Bool("y", false, "approve every tool call without asking (startup.mode = \"yolo\")")
	plain := fs.Bool("plain", false, "use the line REPL instead of the full-screen TUI")
	model := fs.String("model", "", modelFlagUsage)
	serverFlag := fs.String("server", "", serverFlagUsage+"; empty runs in-process")
	quiet := fs.Bool("q", false, "with -n: print only the agent's reply (no thread ID or tool lines)")
	noStream := fs.Bool("no-stream", false, "with -n: print the reply once complete instead of token by token")
	timeout := fs.Duration("timeout", 0, "with -n: cancel after this long (e.g. 10m) and exit 124")
	version := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage:\n"+
			"  dcode [flags] [-n prompt | prompt...]   run the agent in the current directory\n"+
			"  dcode serve [-addr host:port]          run a long-lived agent server\n"+
			"  dcode threads [-all] [-json]           list threads\n\n"+
			"Threads are stored in Postgres when $%s is set, otherwise in memory.\n"+
			"write_file, edit_file and execute ask for approval unless -y is given;\n"+
			"shell.allow_list in %s lets matching commands skip it.\n"+
			"Shell commands run in the current directory and are not sandboxed.\n\n", postgresEnvVar, "~/.deepagents/config.toml")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if *version {
		fmt.Fprintln(stdout, "dcode (go)", deepagents.Version)
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
	srv, err := connect(ctx, e, serverURL, *model, stderr)
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
			fmt.Fprintln(stderr, "dcode: no previous thread in this directory; starting a new one")
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
			Prompt: prompt, ThreadID: thread, Cwd: e.cwd, AutoApprove: yolo,
			Approver: approver, Quiet: *quiet, NoStream: *noStream, Timeout: *timeout,
			Stdout: stdout, Stderr: stderr,
		})
	}
	if !*plain && isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		return 0, tui.Run(ctx, tui.Options{
			Server: srv, Cwd: e.cwd, ThreadID: thread, AutoApprove: yolo, ModelName: modelName(srv, serverURL),
		})
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	return 0, client.RunInteractive(ctx, srv, client.InteractiveOptions{
		ThreadID: thread, Cwd: e.cwd, AutoApprove: yolo,
		In: os.Stdin, Out: stdout, Err: stderr, Interrupt: sig,
	})
}

func modelName(srv server.Server, serverURL string) string {
	if svc, ok := srv.(*server.Service); ok {
		info := svc.ModelInfo()
		return info.Provider + ":" + info.Model
	}
	return serverURL
}

func runServe(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("dcode serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", server.DefaultAddr, "listen address (loopback only)")
	model := fs.String("model", "", modelFlagUsage)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	svc, err := newService(ctx, e, *model)
	if err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	defer svc.Close()
	info := svc.ModelInfo()
	context.AfterFunc(ctx, func() {
		fmt.Fprintln(stderr, "dcode serve: stopping; running turns get up to 30s to finish (Ctrl-C again to force quit)")
	})
	return server.Serve(ctx, *addr, svc, func(a net.Addr) {
		fmt.Fprintf(stderr, "dcode serve listening on http://%s (model %s:%s, storage %s)\n", a, info.Provider, info.Model, svc.Storage())
		fmt.Fprintf(stderr, "  clients: export %s=http://%s\n", client.ServerEnvVar, a)
		if svc.Storage() == "memory" {
			fmt.Fprintf(stderr, "  note: threads are kept in memory and lost on restart; set %s to keep them\n", postgresEnvVar)
		}
	})
}

func runThreads(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("dcode threads", flag.ContinueOnError)
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
	srv, err := connect(ctx, e, e.serverURL(fs, *serverFlag), "", io.Discard)
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
		return fmt.Errorf("%s requires -n or a prompt argument, e.g. dcode -q -n 'summarize README.md'", strings.Join(used, " and "))
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
