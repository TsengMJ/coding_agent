// Package permissions defines filesystem access rules enforced by the
// filesystem middleware and the interrupt configs derived from them
// (Python `FilesystemPermission`, `_fs_interrupt.py`).
package permissions

import (
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
)

// Operation is a filesystem operation class a rule applies to.
type Operation string

const (
	OpRead   Operation = "read"
	OpWrite  Operation = "write"
	OpDelete Operation = "delete"
)

// Mode is the effect of a matching rule.
type Mode string

const (
	ModeAllow     Mode = "allow"
	ModeDeny      Mode = "deny"
	ModeInterrupt Mode = "interrupt"
)

// FilesystemPermission is one rule; rules are evaluated in order, first match wins,
// and no match means allow.
type FilesystemPermission struct {
	Operations []Operation
	// Paths are absolute glob patterns (must start with "/", no "..", no "~").
	Paths []string
	Mode  Mode
}

// Validate checks the path pattern constraints.
func (p FilesystemPermission) Validate() error {
	return notimpl.Err("permissions.FilesystemPermission.Validate")
}

// Check returns the effective mode for op on path.
func Check(rules []FilesystemPermission, op Operation, path string) Mode {
	return ModeAllow
}

// FilterPaths drops paths the rules deny for op (used for ls/glob/grep results).
func FilterPaths(rules []FilesystemPermission, op Operation, paths []string) []string {
	return paths
}

// BuildInterruptOn derives per-tool HITL configs from interrupt-mode rules
// (`_build_interrupt_on_from_permissions`).
func BuildInterruptOn(rules []FilesystemPermission) (map[string]hitl.InterruptOnConfig, error) {
	return nil, notimpl.Err("permissions.BuildInterruptOn")
}
