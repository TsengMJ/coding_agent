package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ContainerSpec creates a workspace container.
type ContainerSpec struct {
	Name  string
	Image string
	// HostDir is bind-mounted at ContainerDir.
	HostDir string
	Labels  map[string]string
	// CPUs and MemoryMB limit the container; zero is unlimited.
	CPUs     float64
	MemoryMB int
	// Network is the docker network; empty uses the default bridge.
	Network string
}

// Docker is the subset of the docker CLI the Manager needs; tests replace it.
type Docker interface {
	// Build builds dockerfile (relative to dir) as tag.
	Build(ctx context.Context, dir, dockerfile, tag string) error
	// Create creates and starts a container that sleeps until stopped.
	Create(ctx context.Context, spec ContainerSpec) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	// Remove removes the container; a missing container is not an error.
	Remove(ctx context.Context, name string) error
	// Inspect reports whether the container exists and is running.
	Inspect(ctx context.Context, name string) (exists, running bool, err error)
	// Exec runs cmd in the container and returns its exit code and output.
	Exec(ctx context.Context, name, workdir string, cmd ...string) (int, string, error)
}

// CLIDocker drives the docker command-line client.
type CLIDocker struct {
	// Binary defaults to "docker".
	Binary string
}

func (d CLIDocker) bin() string {
	if d.Binary == "" {
		return "docker"
	}
	return d.Binary
}

func (d CLIDocker) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (d CLIDocker) Build(ctx context.Context, dir, dockerfile, tag string) error {
	_, err := d.run(ctx, "build", "--quiet", "-f", dockerfile, "-t", tag, dir)
	return err
}

func (d CLIDocker) Create(ctx context.Context, spec ContainerSpec) error {
	args := []string{"run", "-d", "--name", spec.Name, "--init",
		"-v", spec.HostDir + ":" + ContainerDir, "-w", ContainerDir}
	for k, v := range spec.Labels {
		args = append(args, "--label", k+"="+v)
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprint(spec.CPUs))
	}
	if spec.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.MemoryMB))
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	args = append(args, "--entrypoint", "sleep", spec.Image, "infinity")
	_, err := d.run(ctx, args...)
	return err
}

func (d CLIDocker) Start(ctx context.Context, name string) error {
	_, err := d.run(ctx, "start", name)
	return err
}

func (d CLIDocker) Stop(ctx context.Context, name string) error {
	_, err := d.run(ctx, "stop", "-t", "5", name)
	return err
}

func (d CLIDocker) Remove(ctx context.Context, name string) error {
	_, err := d.run(ctx, "rm", "-f", "-v", name)
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}

func (d CLIDocker) Inspect(ctx context.Context, name string) (bool, bool, error) {
	out, err := d.run(ctx, "inspect", "--type", "container", "--format", "{{.State.Running}}", name)
	if err != nil {
		if strings.Contains(err.Error(), "No such") {
			return false, false, nil
		}
		return false, false, err
	}
	return true, out == "true", nil
}

func (d CLIDocker) Exec(ctx context.Context, name, workdir string, cmd ...string) (int, string, error) {
	args := []string{"exec"}
	if workdir != "" {
		args = append(args, "-w", workdir)
	}
	args = append(append(args, name), cmd...)
	c := exec.CommandContext(ctx, d.bin(), args...)
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	c.WaitDelay = 5 * time.Second
	err := c.Run()
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), out.String(), nil
	case err != nil:
		return -1, out.String(), fmt.Errorf("docker exec: %w", err)
	}
	return 0, out.String(), nil
}
