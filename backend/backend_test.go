package backend

import (
	"context"
	"strings"
	"testing"
)

var (
	_ Backend        = (*StateBackend)(nil)
	_ Backend        = (*StoreBackend)(nil)
	_ Backend        = (*FilesystemBackend)(nil)
	_ Backend        = (*CompositeBackend)(nil)
	_ Backend        = (*ContextHubBackend)(nil)
	_ Deleter        = (*StateBackend)(nil)
	_ Deleter        = (*StoreBackend)(nil)
	_ SandboxBackend = (*LocalShellBackend)(nil)
	_ SandboxBackend = (*BaseSandbox)(nil)
	_ SandboxBackend = (*LangSmithSandbox)(nil)
)

func TestLocalShellSupportsExecution(t *testing.T) {
	if !SupportsExecution(NewLocalShellBackend(LocalShellOptions{})) {
		t.Fatal("LocalShellBackend must support execution")
	}
}

func TestStateBackendDoesNotSupportExecution(t *testing.T) {
	if SupportsExecution(NewStateBackend()) {
		t.Fatal("StateBackend must not support execution")
	}
}

func TestStubReportsNotImplemented(t *testing.T) {
	res := unimplemented{name: "Stub"}.Ls(context.Background(), "/")
	if !strings.Contains(res.Error, "backend.Stub.Ls: not implemented") {
		t.Fatalf("unexpected error text: %q", res.Error)
	}
}

func TestRunContextRoundTrip(t *testing.T) {
	rc := &RunContext{}
	if RunContextFrom(WithRunContext(context.Background(), rc)) != rc {
		t.Fatal("RunContext must round-trip through context")
	}
}
