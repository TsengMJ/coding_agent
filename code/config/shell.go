package config

import (
	"errors"
	"strings"
)

// RecommendedSafeShellCommands are read-only commands that `recommended`
// expands to (Python `RECOMMENDED_SAFE_SHELL_COMMANDS`). Shells, editors,
// interpreters, package managers, and network tools are deliberately absent.
var RecommendedSafeShellCommands = []string{
	"ls", "dir",
	"cat", "head", "tail",
	"grep", "wc", "strings",
	"cut", "tr", "diff", "md5sum", "sha256sum",
	"pwd", "which",
	"uname", "hostname", "whoami", "id", "groups", "uptime", "nproc", "lscpu", "lsmem",
	"ps",
}

// ShellAllowAll is the allow-list value that approves every command.
const ShellAllowAll = "all"

// ParseShellAllowList parses a comma-separated allow list
// (Python `parse_shell_allow_list`). "all" must be the only value and yields
// allowAll; "recommended" expands to RecommendedSafeShellCommands and may be
// mixed with other commands.
func ParseShellAllowList(s string) (commands []string, allowAll bool, err error) {
	return ParseShellAllowListItems(strings.Split(s, ","))
}

// ParseShellAllowListItems is ParseShellAllowList for already-split entries,
// such as a TOML array.
func ParseShellAllowListItems(items []string) (commands []string, allowAll bool, err error) {
	for _, item := range items {
		item = strings.TrimSpace(item)
		switch strings.ToLower(item) {
		case "":
		case ShellAllowAll:
			allowAll = true
		case "recommended":
			commands = append(commands, RecommendedSafeShellCommands...)
		default:
			commands = append(commands, item)
		}
	}
	if allowAll && len(commands) > 0 {
		return nil, false, errors.New(`shell allow list: "all" cannot be combined with other commands`)
	}
	return dedupe(commands), allowAll, nil
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
