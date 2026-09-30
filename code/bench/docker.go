package bench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ContainerSpec starts a long-lived task container.
type ContainerSpec struct {
	Image string
	// Name is optional; dbench names containers so leftovers are findable.
	Name      string
	NoNetwork bool
	CPUs      float64
	MemoryMB  int
	// Mounts bind host directories: host path -> container path.
	Mounts  map[string]string
	Workdir string
}

// ExecSpec runs one command in a container.
type ExecSpec struct {
	Cmd     []string
	Workdir string
	Stdin   io.Reader
	// Timeout kills the docker exec client; zero means no limit.
	Timeout time.Duration
}

// ExecResult is a finished docker exec.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	TimedOut bool
}

// Docker is the subset of the docker CLI dbench needs; tests replace it.
type Docker interface {
	// EnsureImage pulls image if it is not present and returns its digest.
	EnsureImage(ctx context.Context, image string) (string, error)
	Run(ctx context.Context, spec ContainerSpec) (string, error)
	Exec(ctx context.Context, id string, spec ExecSpec) (ExecResult, error)
	// CopyTo copies a host file or directory to dst in the container.
	CopyTo(ctx context.Context, id, src, dst string) error
	// CopyFrom copies src in the container to a host path.
	CopyFrom(ctx context.Context, id, src, dst string) error
	// CopyOut copies src from a fresh, never-started container of image.
	CopyOut(ctx context.Context, image, src, dst string) error
	Remove(ctx context.Context, id string) error
	RemoveImage(ctx context.Context, image string) error
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
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (d CLIDocker) EnsureImage(ctx context.Context, image string) (string, error) {
	digest := func() (string, error) {
		return d.run(ctx, "image", "inspect", "--format", "{{if .RepoDigests}}{{index .RepoDigests 0}}{{else}}{{.Id}}{{end}}", image)
	}
	if dg, err := digest(); err == nil {
		return dg, nil
	}
	if _, err := d.run(ctx, "pull", "--platform", "linux/amd64", "--quiet", image); err != nil {
		return "", err
	}
	return digest()
}

// keepAlive is the container command; the images default to an interactive
// shell, so the entrypoint is replaced.
var keepAlive = []string{"--entrypoint", "sleep"}

const keepAliveArg = "2147483647"

func (d CLIDocker) Run(ctx context.Context, spec ContainerSpec) (string, error) {
	args := []string{"run", "-d", "--label", "dbench=1", "--platform", "linux/amd64"}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	if spec.NoNetwork {
		args = append(args, "--network", "none")
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.CPUs, 'f', -1, 64))
	}
	if spec.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.MemoryMB))
	}
	for host, ctr := range spec.Mounts {
		args = append(args, "-v", host+":"+ctr)
	}
	if spec.Workdir != "" {
		args = append(args, "-w", spec.Workdir)
	}
	args = append(args, keepAlive...)
	args = append(args, spec.Image, keepAliveArg)
	return d.run(ctx, args...)
}

func (d CLIDocker) Exec(ctx context.Context, id string, spec ExecSpec) (ExecResult, error) {
	args := []string{"exec"}
	if spec.Stdin != nil {
		args = append(args, "-i")
	}
	if spec.Workdir != "" {
		args = append(args, "-w", spec.Workdir)
	}
	args = append(args, id)
	args = append(args, spec.Cmd...)
	runCtx := ctx
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, d.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &stdout, &stderr, spec.Stdin
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	res := ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		res.TimedOut, res.ExitCode = true, 124
		return res, nil
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case err != nil:
		return res, fmt.Errorf("docker exec: %w", err)
	}
	return res, nil
}

func (d CLIDocker) CopyTo(ctx context.Context, id, src, dst string) error {
	_, err := d.run(ctx, "cp", src, id+":"+dst)
	return err
}

func (d CLIDocker) CopyFrom(ctx context.Context, id, src, dst string) error {
	_, err := d.run(ctx, "cp", id+":"+src, dst)
	return err
}

func (d CLIDocker) CopyOut(ctx context.Context, image, src, dst string) error {
	id, err := d.run(ctx, "create", "--platform", "linux/amd64", "--label", "dbench=1", image)
	if err != nil {
		return err
	}
	defer d.Remove(context.WithoutCancel(ctx), id)
	return d.CopyFrom(ctx, id, src, dst)
}

func (d CLIDocker) Remove(ctx context.Context, id string) error {
	_, err := d.run(ctx, "rm", "-f", "-v", id)
	return err
}

func (d CLIDocker) RemoveImage(ctx context.Context, image string) error {
	_, err := d.run(ctx, "rmi", image)
	return err
}

// Version reports the docker server version and resources for run metadata.
func (d CLIDocker) Version(ctx context.Context) (map[string]string, error) {
	out, err := d.run(ctx, "info", "--format", "{{.ServerVersion}}|{{.NCPU}}|{{.MemTotal}}|{{.Architecture}}")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(out, "|")
	if len(parts) != 4 {
		return nil, fmt.Errorf("docker info: unexpected output %q", out)
	}
	return map[string]string{"server_version": parts[0], "ncpu": parts[1], "mem_total": parts[2], "arch": parts[3]}, nil
}
