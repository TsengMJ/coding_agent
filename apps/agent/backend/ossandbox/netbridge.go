package ossandbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
)

// NetBridgeArg is the hidden quill subcommand that runs inside a bubblewrap
// network namespace: it listens on 127.0.0.1:port, forwards each connection
// to the proxy's unix socket and runs the command.
//
//	quill __netbridge <port> <socket> <command> [args...]
const NetBridgeArg = "__netbridge"

// MaybeRunNetBridge runs the bridge and exits when the process was started
// as one. Call it first in main (and TestMain of packages whose tests run
// NetworkAllowlist commands under bubblewrap).
func MaybeRunNetBridge() {
	if len(os.Args) > 1 && os.Args[1] == NetBridgeArg {
		os.Exit(RunNetBridge(os.Args[2:]))
	}
}

// RunNetBridge runs the bridge with args after NetBridgeArg and returns the
// command's exit code.
func RunNetBridge(args []string) int {
	code, err := runNetBridge(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "quill %s: %v\n", NetBridgeArg, err)
	}
	return code
}

func runNetBridge(args []string) (int, error) {
	if len(args) < 3 {
		return 2, errors.New("usage: <port> <socket> <command> [args...]")
	}
	port, err := strconv.Atoi(args[0])
	if err != nil {
		return 2, fmt.Errorf("port %q: %w", args[0], err)
	}
	socket := args[1]
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return 1, err
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				up, err := net.Dial("unix", socket)
				if err != nil {
					c.Close()
					return
				}
				pipe(c, up)
			}()
		}
	}()
	cmd := exec.Command(args[2], args[3:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return code, nil
		}
		return 1, nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}
