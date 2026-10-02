package tool

import "testing"

func TestEffectConflicts(t *testing.T) {
	read := func(p ...string) Effect { return Effect{Kind: EffectRead, Paths: p} }
	write := func(p ...string) Effect { return Effect{Kind: EffectWrite, Paths: p} }
	everything := Effect{Kind: EffectWrite}
	cases := []struct {
		name string
		a, b Effect
		want bool
	}{
		{"reads never conflict", read(), read("/a"), false},
		{"exec conflicts with a read", Effect{Kind: EffectExec}, read("/a"), true},
		{"write and read of one file", write("/a.go"), read("/a.go"), true},
		{"write and read of other files", write("/a.go"), read("/b.go"), false},
		{"write under a read directory", write("/src/a.go"), read("/src"), true},
		{"sibling prefix is not a parent", write("/src2/a.go"), read("/src"), false},
		{"unscoped read waits for writes", write("/a.go"), Effect{Kind: EffectRead}, true},
		{"unscoped write", everything, read("/b.go"), true},
		{"virtual and relative paths match", write("a.go"), read("/a.go"), true},
		{"touches nothing", Effect{Kind: EffectRead, Paths: []string{}}, write("/a.go"), false},
	}
	for _, c := range cases {
		if got := c.a.Conflicts(c.b); got != c.want {
			t.Errorf("%s: a.Conflicts(b) = %v", c.name, got)
		}
		if got := c.b.Conflicts(c.a); got != c.want {
			t.Errorf("%s: b.Conflicts(a) = %v", c.name, got)
		}
	}
}

func TestUndeclaredToolsExecute(t *testing.T) {
	if e := EffectOf(&Func{ToolName: "x"}, nil); e.Kind != EffectExec {
		t.Fatalf("effect = %+v, want exec", e)
	}
	d := ApplyDescriptionOverrides([]Tool{&Func{ToolName: "x", ToolEffect: ReadOnly}}, map[string]string{"x": "new"})[0]
	if e := EffectOf(d, nil); e.Kind != EffectRead {
		t.Fatalf("description override lost the effect: %+v", e)
	}
}
