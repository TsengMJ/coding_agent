package codetools

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestFindRelatedGoPackagesAndDependents(t *testing.T) {
	needCmd(t, "go")
	dir := t.TempDir()
	writeFile(t, dir, "svc/go.mod", "module example.com/svc\n\ngo 1.21\n")
	writeFile(t, dir, "svc/a/a.go", "package a\n\nfunc A() int { return 1 }\n")
	writeFile(t, dir, "svc/b/b.go", "package b\n\nimport \"example.com/svc/a\"\n\nfunc B() int { return a.A() }\n")
	writeFile(t, dir, "svc/c/c_test.go", "package c\n\nimport (\n\t\"testing\"\n\n\t\"example.com/svc/a\"\n)\n\nfunc TestC(t *testing.T) { _ = a.A() }\n")
	writeFile(t, dir, "svc/d/d.go", "package d\n\nfunc D() {}\n")
	got, err := FindRelated(context.Background(), newShell(t, dir), []string{"svc/a/a.go"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != langGo || got.Dir != "svc" {
		t.Fatalf("target = %+v", got)
	}
	if !slices.Equal(got.Packages, []string{"./a"}) || !slices.Equal(got.Dependents, []string{"./b", "./c"}) {
		t.Fatalf("packages = %v, dependents = %v", got.Packages, got.Dependents)
	}
}

func TestFindRelatedNodeTests(t *testing.T) {
	needCmd(t, "node")
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"x"}`)
	writeFile(t, dir, "src/user.js", "module.exports = {};\n")
	writeFile(t, dir, "test/user.test.js", "const u = require('../src/user');\n")
	writeFile(t, dir, "test/api.spec.js", "import user from '../src/user.js';\n")
	writeFile(t, dir, "test/other.test.js", "const o = require('../src/other');\n")
	writeFile(t, dir, "node_modules/dep/user.test.js", "require('../src/user');\n")
	got, err := FindRelated(context.Background(), newShell(t, dir), []string{"src/user.js"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != langNode || !slices.Equal(got.Tests, []string{"test/user.test.js"}) || !slices.Equal(got.Importers, []string{"test/api.spec.js"}) {
		t.Fatalf("related = %+v", got)
	}
}

func TestFindRelatedPythonTests(t *testing.T) {
	needCmd(t, "python3")
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[project]\nname = 'x'\n")
	writeFile(t, dir, "app/user.py", "X = 1\n")
	writeFile(t, dir, "tests/test_user.py", "from app import user\n")
	writeFile(t, dir, "tests/test_api.py", "from app.user import X\n")
	writeFile(t, dir, "tests/test_other.py", "import app.other\n")
	got, err := FindRelated(context.Background(), newShell(t, dir), []string{"app/user.py"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != langPython || !slices.Equal(got.Tests, []string{"tests/test_user.py"}) || !slices.Equal(got.Importers, []string{"tests/test_api.py"}) {
		t.Fatalf("related = %+v", got)
	}
}

func TestFindRelatedKeepsChangedTestFiles(t *testing.T) {
	needCmd(t, "python3")
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[project]\nname = 'x'\n")
	writeFile(t, dir, "tests/test_user.py", "def test_x():\n    pass\n")
	got, err := FindRelated(context.Background(), newShell(t, dir), []string{"tests/test_user.py"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Tests, []string{"tests/test_user.py"}) {
		t.Fatalf("related = %+v", got)
	}
}
