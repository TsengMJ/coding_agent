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
)

// runSandbox runs one command under the shell sandbox, the way the agent's
// shell tools would, so a policy can be tried by hand.
func runSandbox(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("quill sandbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", string(ossandbox.WorkspaceWrite), "read-only, workspace-write or full-access")
	network := fs.String("network", string(ossandbox.NetworkNone), "none or full")
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
	m, err := ossandbox.ParseMode(*mode)
	if err != nil {
		return 2, err
	}
	n, err := ossandbox.ParseNetwork(*network)
	if err != nil {
		return 2, err
	}
	if n == ossandbox.NetworkAllowlist {
		return 2, errors.New("sandbox: -network allowlist needs the agent's proxy; use none or full")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	policy := ossandbox.DefaultPolicy(m, cwd)
	policy.Network = n
	sb := ossandbox.None()
	if m != ossandbox.FullAccess {
		if sb, err = ossandbox.Native(); err != nil {
			return 1, err
		}
	}
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
