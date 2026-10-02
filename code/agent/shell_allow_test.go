package agent

import (
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

func executeCall(cmd string) llm.ToolCall {
	return llm.ToolCall{ID: "c1", Name: "execute", Args: map[string]any{"command": cmd}}
}

func TestIsShellCommandAllowed(t *testing.T) {
	basic := []string{"ls", "cat", "grep", "pwd", "echo"}
	cases := []struct {
		cmd  string
		want bool
	}{
		{"ls", true},
		{"  ls  ", true},
		{"ls -la /tmp/test", true},
		{"grep 'pattern' file.txt", true},
		{`echo "test" | grep "te"`, true},
		{"ls | grep x", true},
		{"ls && cat file", true},
		{"ls || cat file", true},
		{"ls; pwd", true},
		{"LS", false},
		{"", false},
		{"   ", false},
		{"rm -rf /", false},
		{"ls | wc -l", false},
		{"ls && rm file", false},
		{"ls || rm file", false},
		{"/bin/rm -rf /", false},
		{"./malicious", false},
		{`ls "unclosed`, false},
		{"cat 'missing quote", false},

		// Injection vectors are refused before tokenizing.
		{`ls "$(rm -rf /)"`, false},
		{"echo `whoami`", false},
		{`cat $'\050whoami\051'`, false},
		{"ls\nrm -rf /", false},
		{"ls\rrm", false},
		{"ls\t-la", false},
		{"cat <(ls)", false},
		{"ls >(cat)", false},
		{"cat <<< hi", false},
		{"cat << EOF", false},
		{"ls >> out", false},
		{"ls > out", false},
		{"cat < /etc/passwd", false},
		{"echo ${HOME}", false},
		{"ls $HOME", false},
		{"grep $IFS file.txt", false},
		{"ls &", false},
		{"cat file.txt & echo done", false},
	}
	for _, c := range cases {
		if got := IsShellCommandAllowed(c.cmd, basic, false); got != c.want {
			t.Errorf("IsShellCommandAllowed(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestIsShellCommandAllowedEmptyListRejects(t *testing.T) {
	if IsShellCommandAllowed("ls", nil, false) {
		t.Error("nil allow list must reject")
	}
}

func TestIsShellCommandAllowedAllSkipsChecks(t *testing.T) {
	if !IsShellCommandAllowed("rm -rf / > out &", nil, true) {
		t.Error(`"all" must approve any non-empty command`)
	}
	if IsShellCommandAllowed("  ", nil, true) {
		t.Error("empty command must still be rejected")
	}
}

func TestInterruptOnSkipsAllowListedCommands(t *testing.T) {
	s := &config.Settings{ShellAllowList: config.RecommendedSafeShellCommands}
	when := InterruptOn(s)["execute"].When
	if when == nil {
		t.Fatal("execute should have a When filter when an allow list is set")
	}
	if when(executeCall("ls -la | grep go")) {
		t.Error("allow-listed command should not need review")
	}
	if !when(executeCall("rm -rf build")) {
		t.Error("other commands should need review")
	}
	if InterruptOn(nil)["execute"].When != nil {
		t.Error("no allow list: every execute call needs review")
	}
	all := InterruptOn(&config.Settings{ShellAllowAll: true})["execute"]
	if all.When == nil || all.When(executeCall("rm -rf build")) {
		t.Error(`"all": ordinary commands should not be reviewed`)
	}
	if all.AlwaysWhen == nil || !all.AlwaysWhen(executeCall("git reset --hard")) {
		t.Error(`"all": destructive commands should still be reviewed`)
	}
}

func TestInterruptOnAlwaysReviewsDestructiveCommands(t *testing.T) {
	for name, s := range map[string]*config.Settings{
		"default":    nil,
		"allow list": {ShellAllowList: []string{"git", "rm"}},
	} {
		cfg := InterruptOn(s)["execute"]
		if cfg.AlwaysWhen == nil || !cfg.AlwaysWhen(executeCall("git clean -fdx")) || cfg.AlwaysWhen(executeCall("git status")) {
			t.Errorf("%s: AlwaysWhen does not single out destructive commands", name)
		}
		if got := cfg.Describe(executeCall("rm -rf ~")); !strings.HasPrefix(got, "Destructive command:") {
			t.Errorf("%s: description = %q", name, got)
		}
	}
}

func TestInterruptOnRunnerToolsFollowAllowList(t *testing.T) {
	runnerTools := []string{"run_tests", "diagnostics"}
	for _, tc := range []struct {
		name     string
		settings *config.Settings
		reviewed bool
	}{
		{"no settings", nil, true},
		{"allow list without runners", &config.Settings{ShellAllowList: []string{"ls"}}, true},
		{"allow list with go only", &config.Settings{ShellAllowList: []string{"ls", "go"}}, true},
		{"allow list with every runner", &config.Settings{ShellAllowList: []string{"go", "npx", "npm", "node", "python3", "python", "ruff"}}, false},
		{"all", &config.Settings{ShellAllowAll: true}, false},
	} {
		cfg := InterruptOn(tc.settings)
		for _, name := range runnerTools {
			if _, ok := cfg[name]; ok != tc.reviewed {
				t.Errorf("%s: %s reviewed = %v, want %v", tc.name, name, ok, tc.reviewed)
			}
		}
	}
	if _, ok := InterruptOn(nil)["apply_patch"]; !ok {
		t.Error("apply_patch must always be reviewed")
	}
	if _, ok := InterruptOn(nil)["git_diff"]; ok {
		t.Error("git_diff is read-only and must not be reviewed")
	}
}
