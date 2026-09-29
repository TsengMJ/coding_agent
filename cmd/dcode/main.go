// Command dcode is the Go port of the Deep Agents Code terminal agent.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/tsengmingchun/deepagents_golang"
	"github.com/tsengmingchun/deepagents_golang/code/client"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/server"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dcode:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dcode", flag.ContinueOnError)
	promptFlag := fs.String("n", "", "run a single prompt non-interactively")
	threadID := fs.String("thread", "", "thread ID to resume")
	version := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: dcode [-n prompt | prompt...] [-thread id]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version {
		fmt.Println("dcode (go)", deepagents.Version)
		return nil
	}
	prompt, err := resolvePrompt(*promptFlag, fs.Args())
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	paths, err := config.DefaultPaths(cwd)
	if err != nil {
		return fmt.Errorf("resolve paths: %w", err)
	}
	srv, err := server.New(ctx, server.Config{Paths: paths})
	if err != nil {
		return fmt.Errorf("start agent server: %w", err)
	}
	defer srv.Close()

	if prompt != "" {
		code, err := client.RunNonInteractive(ctx, srv, client.NonInteractiveOptions{
			Prompt: prompt, ThreadID: *threadID, Stdout: os.Stdout, Stderr: os.Stderr,
		})
		if err != nil {
			return err
		}
		os.Exit(code)
	}
	return client.RunInteractive(ctx, srv, client.InteractiveOptions{ThreadID: *threadID})
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
