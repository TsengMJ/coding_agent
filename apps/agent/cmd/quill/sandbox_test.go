package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSandboxCommandRequiresACommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := run(context.Background(), []string{"sandbox"}, &stdout, &stderr)
	if code != 2 || err == nil {
		t.Fatalf("code %d, err %v", code, err)
	}
}

func TestSandboxCommandRejectsUnknownMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := run(context.Background(), []string{"sandbox", "-mode", "yolo", "--", "true"}, &stdout, &stderr)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "yolo") {
		t.Fatalf("code %d, err %v", code, err)
	}
}

func TestSandboxFlagRejectsUnknownMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, err := run(context.Background(), []string{"-sandbox", "yolo", "-n", "hi"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "-sandbox") {
		t.Fatalf("err = %v", err)
	}
}

func TestSandboxCommandFullAccessPassesExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := run(context.Background(), []string{"sandbox", "-mode", "full-access", "--", "sh", "-c", "echo hi; exit 3"}, &stdout, &stderr)
	if err != nil || code != 3 || stdout.String() != "hi\n" {
		t.Fatalf("code %d, err %v, stdout %q", code, err, stdout.String())
	}
}
