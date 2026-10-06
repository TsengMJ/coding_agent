package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/tsengmingchun/quill/backend/ossandbox"
	codeagent "github.com/tsengmingchun/quill/code/agent"
)

// runSandbox runs one command under the shell sandbox, the way the agent's
// shell tools would, so a policy can be tried by hand.
func runSandbox(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("quill sandbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "", "read-only, workspace-write or full-access (default sandbox.mode, or workspace-write when that is full-access)")
	network := fs.String("network", "", "none or full (default sandbox.network)")
	printProfile := fs.Bool("print-policy", false, "print the policy (and the Seatbelt profile on macOS) before running")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage:\n  quill sandbox [flags] -- command [args...]\n\nRuns command in the current directory inside the sandbox quill's shell tools use.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2, err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2, errors.New("sandbox: missing command")
	}
	e, err := loadEnv()
	if err != nil {
		return 1, err
	}
	s := *e.settings
	if !s.Sandbox.Enabled() {
		s.Sandbox.Mode = ossandbox.WorkspaceWrite
	}
	if *mode != "" {
		if s.Sandbox.Mode, err = ossandbox.ParseMode(*mode); err != nil {
			return 2, err
		}
	}
	if *network != "" {
		if s.Sandbox.Network, err = ossandbox.ParseNetwork(*network); err != nil {
			return 2, err
		}
	}
	if s.Sandbox.Network == ossandbox.NetworkAllowlist {
		return 2, errors.New("sandbox: -network allowlist needs the agent's proxy; use none or full")
	}
	sb, policy, err := codeagent.NewShellSandbox(&s, e.cwd)
	if err != nil {
		return 1, err
	}
	if sb == nil {
		sb, policy = ossandbox.None(), ossandbox.Policy{Mode: ossandbox.FullAccess}
	}
	cwd := e.cwd
	if *printProfile {
		fmt.Fprintf(stderr, "sandbox: %s, mode %s, network %s\nwritable: %v\ndeny write: %v\ndeny read: %v\n",
			sb.Name(), policy.Mode, policy.Network, policy.WritableRoots, policy.DenyWrite, policy.DenyRead)
		if _, ok := sb.(ossandbox.Seatbelt); ok {
			fmt.Fprintln(stderr, ossandbox.SeatbeltProfile(policy))
		}
	}
	cmd, err := sb.Command(ctx, policy, ossandbox.ShellJoin(fs.Args()))
	if err != nil {
		return 1, err
	}
	cmd.Dir = cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}
