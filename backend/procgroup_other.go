//go:build !unix

package backend

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(*exec.Cmd) error { return nil }
