package codetools

import (
	"context"
	"testing"
)

func TestParseProbe(t *testing.T) {
	out := "file:package.json\nfile:.eslintrc.json\ncmd:node\ncmd:npx\n" + packageMarker + "\n" +
		`{"scripts":{"test":"nyc --reporter=html mocha"},"devDependencies":{"jest":"29"}}`
	p := parseProbe(out)
	if !p.files["package.json"] || !p.cmds["npx"] || p.cmds["go"] || !p.hasESLintConfig() {
		t.Fatalf("project = %+v", p)
	}
	if got := p.nodeFramework(); got != frameworkMocha {
		t.Errorf("framework = %q, want mocha (the test script wins over dependencies)", got)
	}
}

func TestNodeFramework(t *testing.T) {
	cases := []struct {
		pkg  packageJSON
		want string
	}{
		{packageJSON{Scripts: map[string]string{"test": "vitest run"}}, frameworkVitest},
		{packageJSON{Scripts: map[string]string{"test": "node scripts/test.js"}, DevDependencies: map[string]string{"jest": "1"}}, frameworkJest},
		{packageJSON{Dependencies: map[string]string{"mocha": "1"}}, frameworkMocha},
		{packageJSON{Scripts: map[string]string{"test": "tap"}}, frameworkNpm},
	}
	for _, c := range cases {
		if got := (project{pkg: c.pkg}).nodeFramework(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.pkg, got, c.want)
		}
	}
}

func TestLanguage(t *testing.T) {
	files := func(names ...string) project {
		p := project{files: map[string]bool{}}
		for _, n := range names {
			p.files[n] = true
		}
		return p
	}
	cases := []struct {
		p        project
		explicit string
		args     []string
		want     string
	}{
		{files("go.mod", "package.json"), "", nil, langGo},
		{files("pyproject.toml", "package.json"), "", nil, langPython},
		{files("package.json"), "", nil, langNode},
		{files("pyproject.toml", "package.json"), "", []string{"web/app.test.ts"}, langNode},
		{files("package.json"), "Python", nil, langPython},
		{files(), "", nil, ""},
	}
	for _, c := range cases {
		got, err := c.p.language(c.explicit, c.args)
		if err != nil || got != c.want {
			t.Errorf("%v %q %v: %q, %v, want %q", c.p.files, c.explicit, c.args, got, err, c.want)
		}
	}
	if _, err := files().language("cobol", nil); err == nil {
		t.Error("unknown language accepted")
	}
}

func TestProbeProjectEndToEnd(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[project]\nname='x'\n")
	writeFile(t, dir, "package.json", `{"devDependencies":{"vitest":"1"}}`)
	p := probeProject(context.Background(), newShell(t, dir))
	if !p.files["pyproject.toml"] || p.files["go.mod"] || p.nodeFramework() != frameworkVitest {
		t.Fatalf("project = %+v", p)
	}
}
