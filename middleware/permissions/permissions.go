// Package permissions defines filesystem access rules enforced by the
// filesystem middleware and the interrupt configs derived from them
// (Python `FilesystemPermission`, `_fs_interrupt.py`).
package permissions

import (
	"fmt"
	pathpkg "path"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/hitl"
)

// Operation is a filesystem operation class a rule applies to. As in Python,
// `delete` is a write.
type Operation string

const (
	OpRead  Operation = "read"
	OpWrite Operation = "write"
)

// Mode is the effect of a matching rule.
type Mode string

const (
	ModeAllow     Mode = "allow"
	ModeDeny      Mode = "deny"
	ModeInterrupt Mode = "interrupt"
)

// FilesystemPermission is one rule; rules are evaluated in order, first match wins,
// and no match means allow. An empty Mode means allow.
//
// Paths are matched with doublestar (`**`, `{a,b}`); unlike Python's wcmatch,
// `*` and `**` also match dotfiles, so a deny on `/secrets/**` covers
// `/secrets/.env`.
type FilesystemPermission struct {
	Operations []Operation
	// Paths are absolute glob patterns (must start with "/", no "..", no "~").
	Paths []string
	Mode  Mode
}

// Validate checks the path pattern constraints.
func (p FilesystemPermission) Validate() error {
	switch p.Mode {
	case "", ModeAllow, ModeDeny, ModeInterrupt:
	default:
		return fmt.Errorf("permission mode must be allow, deny or interrupt: %q", p.Mode)
	}
	for _, op := range p.Operations {
		if op != OpRead && op != OpWrite {
			return fmt.Errorf("permission operation must be read or write: %q", op)
		}
	}
	for _, path := range p.Paths {
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("permission path must start with '/': %q", path)
		}
		parts := strings.Split(backend.ToPOSIXPath(path), "/")
		if slices.Contains(parts, "..") {
			return fmt.Errorf("permission path must not contain '..': %q", path)
		}
		if slices.Contains(parts, "~") {
			return fmt.Errorf("permission path must not contain '~': %q", path)
		}
		if !doublestar.ValidatePattern(path) {
			return fmt.Errorf("invalid permission path pattern: %q", path)
		}
	}
	return nil
}

// ValidateAll validates every rule.
func ValidateAll(rules []FilesystemPermission) error {
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p FilesystemPermission) mode() Mode {
	if p.Mode == "" {
		return ModeAllow
	}
	return p.Mode
}

// matches reports whether any of the rule's patterns match path.
func (p FilesystemPermission) matches(path string) []string {
	path = normalize(path)
	var out []string
	for _, pattern := range p.Paths {
		if ok, _ := doublestar.Match(pattern, path); ok {
			out = append(out, pattern)
		}
	}
	return out
}

// normalize drops a trailing slash (directory entries) except for the root.
func normalize(path string) string {
	if path != "/" {
		path = strings.TrimRight(path, "/")
	}
	if path == "" {
		return "/"
	}
	return path
}

// Check returns the effective mode for op on path.
func Check(rules []FilesystemPermission, op Operation, path string) Mode {
	for _, r := range rules {
		if !slices.Contains(r.Operations, op) {
			continue
		}
		if len(r.matches(path)) > 0 {
			return r.mode()
		}
	}
	return ModeAllow
}

// FilterPaths drops paths the rules deny for op (used for ls/glob/grep results).
// Interrupt-mode paths pass: the review happened before the tool ran.
func FilterPaths(rules []FilesystemPermission, op Operation, paths []string) []string {
	if len(rules) == 0 {
		return paths
	}
	var out []string
	for _, p := range paths {
		if Check(rules, op, p) != ModeDeny {
			out = append(out, p)
		}
	}
	return out
}

// InterruptAsDeny returns rules with interrupt-mode rules turned into deny,
// for agents that cannot pause for review (subagents).
func InterruptAsDeny(rules []FilesystemPermission) []FilesystemPermission {
	out := slices.Clone(rules)
	for i := range out {
		if out[i].Mode == ModeInterrupt {
			out[i].Mode = ModeDeny
		}
	}
	return out
}

// wildcardChars mark the end of a pattern's literal anchor.
const wildcardChars = "*?[{"

// GlobAnchor returns the longest leading directory of pattern with no
// wildcards (`/secrets/**` -> `/secrets`, `/**/x` -> `/`).
func GlobAnchor(pattern string) string {
	var safe []string
	for _, part := range strings.Split(strings.Trim(backend.ToPOSIXPath(pattern), "/"), "/") {
		if part == "" || strings.ContainsAny(part, wildcardChars) {
			break
		}
		safe = append(safe, part)
	}
	return "/" + strings.Join(safe, "/")
}

// PathsOverlap reports whether the subtrees at a and b intersect: one is a
// component-wise prefix of the other. The root overlaps everything.
func PathsOverlap(a, b string) bool {
	return isWithin(a, b) || isWithin(b, a)
}

// isWithin reports whether p equals base or lies under it.
func isWithin(p, base string) bool {
	p, base = normalize(p), normalize(base)
	return base == "/" || p == base || strings.HasPrefix(p, base+"/")
}

// DeleteDenyPatterns returns the deny-write patterns that block deleting
// target (`_find_delete_deny_patterns`). A recursive delete (hasDescendants)
// is blocked by any deny pattern that could match target or anything under
// it, regardless of rule order; a confirmed plain file uses first-match-wins.
func DeleteDenyPatterns(rules []FilesystemPermission, target string, hasDescendants bool) []string {
	if !hasDescendants {
		for _, r := range rules {
			if !slices.Contains(r.Operations, OpWrite) {
				continue
			}
			matched := r.matches(target)
			if len(matched) == 0 {
				continue
			}
			if r.mode() == ModeDeny {
				return matched
			}
			return nil
		}
		return nil
	}
	var denying []string
	for _, r := range rules {
		if r.mode() != ModeDeny || !slices.Contains(r.Operations, OpWrite) {
			continue
		}
		for _, pattern := range r.Paths {
			if slices.Contains(denying, pattern) {
				continue
			}
			anchor := GlobAnchor(pattern)
			overlaps := PathsOverlap(target, anchor)
			if strings.ContainsAny(pattern, wildcardChars) {
				overlaps = wildcardDeleteOverlap(pattern, anchor, target)
			}
			if overlaps {
				denying = append(denying, pattern)
			}
		}
	}
	return denying
}

// wildcardDeleteOverlap reports whether a wildcard deny pattern's matches
// intersect the subtree removed by deleting target.
func wildcardDeleteOverlap(pattern, anchor, target string) bool {
	target = normalize(target)
	if anchor == "/" {
		return true
	}
	if ok, _ := doublestar.Match(pattern, target); ok {
		return true
	}
	if isWithin(anchor, target) {
		return true
	}
	if !isWithin(target, anchor) {
		return false
	}
	suffix := strings.Split(strings.TrimPrefix(strings.TrimPrefix(pattern, anchor), "/"), "/")
	if len(suffix) != 1 || strings.Contains(suffix[0], "**") {
		return true
	}
	// A glob matching an ancestor of target means target sits inside a
	// denied directory.
	for dir := pathpkg.Dir(target); isWithin(dir, anchor) && dir != anchor; dir = pathpkg.Dir(dir) {
		if ok, _ := doublestar.Match(pattern, dir); ok {
			return true
		}
	}
	return false
}

// toolScope describes how a filesystem tool's path argument is gated.
type toolScope struct {
	op      Operation
	pathArg string
	bulk    bool
	// patternArg can redirect a bulk search root (glob's absolute patterns).
	patternArg string
}

var fsToolScopes = map[string]toolScope{
	"ls":         {op: OpRead, pathArg: "path", bulk: true},
	"read_file":  {op: OpRead, pathArg: "file_path"},
	"write_file": {op: OpWrite, pathArg: "file_path"},
	"edit_file":  {op: OpWrite, pathArg: "file_path"},
	"delete":     {op: OpWrite, pathArg: "file_path", bulk: true},
	"glob":       {op: OpRead, pathArg: "path", bulk: true, patternArg: "pattern"},
	"grep":       {op: OpRead, pathArg: "path", bulk: true},
}

// BuildInterruptOn derives per-tool HITL configs from interrupt-mode rules
// (`_build_interrupt_on_from_permissions`). Exact tools (read/write/edit)
// fire when their path resolves to interrupt under first-match-wins; bulk
// tools (ls/glob/grep/delete) fire when their subtree could overlap an
// interrupt rule's anchor, or when they name no path at all.
func BuildInterruptOn(rules []FilesystemPermission) (map[string]hitl.InterruptOnConfig, error) {
	if err := ValidateAll(rules); err != nil {
		return nil, err
	}
	out := map[string]hitl.InterruptOnConfig{}
	allowed := []hitl.DecisionType{hitl.DecisionApprove, hitl.DecisionEdit, hitl.DecisionReject, hitl.DecisionRespond}
	for name, scope := range fsToolScopes {
		var anchors []string
		for _, r := range rules {
			if r.mode() == ModeInterrupt && slices.Contains(r.Operations, scope.op) {
				for _, p := range r.Paths {
					anchors = append(anchors, GlobAnchor(p))
				}
			}
		}
		if len(anchors) == 0 {
			continue
		}
		when := exactWhen(rules, scope)
		if scope.bulk {
			when = bulkWhen(anchors, scope)
		}
		out[name] = hitl.InterruptOnConfig{AllowedDecisions: allowed, When: when}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func exactWhen(rules []FilesystemPermission, scope toolScope) func(llm.ToolCall) bool {
	return func(call llm.ToolCall) bool {
		raw, ok := call.Args[scope.pathArg].(string)
		if !ok {
			return false
		}
		p, err := backend.ValidatePath(raw, nil)
		if err != nil {
			return false
		}
		return Check(rules, scope.op, p) == ModeInterrupt
	}
}

func bulkWhen(anchors []string, scope toolScope) func(llm.ToolCall) bool {
	return func(call llm.ToolCall) bool {
		raw := call.Args[scope.pathArg]
		if raw == nil {
			return true
		}
		s, ok := raw.(string)
		if !ok {
			return false
		}
		p, err := backend.ValidatePath(s, nil)
		if err != nil {
			return false
		}
		for _, a := range anchors {
			if PathsOverlap(p, a) {
				return true
			}
		}
		if scope.patternArg != "" {
			if pat, ok := call.Args[scope.patternArg].(string); ok && patternFires(pat, anchors) {
				return true
			}
		}
		return false
	}
}

// patternFires reports whether a glob pattern reaches an interrupt subtree
// regardless of the search path: absolute patterns by their anchor, and
// relative patterns climbing with "..".
func patternFires(pattern string, anchors []string) bool {
	posix := backend.ToPOSIXPath(pattern)
	if strings.HasPrefix(posix, "/") {
		anchor := GlobAnchor(posix)
		for _, a := range anchors {
			if PathsOverlap(anchor, a) {
				return true
			}
		}
		return false
	}
	return slices.Contains(strings.Split(posix, "/"), "..")
}
