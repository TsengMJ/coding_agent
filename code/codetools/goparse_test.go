package codetools

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParseTestJSONMixed(t *testing.T) {
	rep := parseTestJSON(fixture(t, "gotest_mixed.jsonl"))
	if rep.Status != "failed" || rep.Passed != 2 || rep.Failed != 2 || rep.Skipped != 1 {
		t.Fatalf("counts = %+v", rep)
	}
	want := []TestFailure{
		{Package: "example.com/t2j/a", Test: "TestFail", File: "a_test.go", Line: 7, Message: "expected 3 attempts,\ngot 1"},
		{Package: "example.com/t2j/a", Test: "TestSub/bad", File: "a_test.go", Line: 11, Message: "boom"},
	}
	if !reflect.DeepEqual(rep.Failures, want) {
		t.Errorf("failures = %+v", rep.Failures)
	}
	wantBuild := []Diagnostic{{File: "b/b.go", Line: 3, Column: 23, Severity: "error", Source: "build", Message: "undefined: retryPolicy"}}
	if !reflect.DeepEqual(rep.BuildErrors, wantBuild) {
		t.Errorf("build errors = %+v", rep.BuildErrors)
	}
}

func TestParseTestJSONPanicPointsAtTest(t *testing.T) {
	rep := parseTestJSON(fixture(t, "gotest_panic.jsonl"))
	if rep.Failed != 1 || len(rep.Failures) != 1 {
		t.Fatalf("got %+v", rep)
	}
	f := rep.Failures[0]
	if f.Test != "TestPanic" || f.File != "/tmp/t2j/c/c_test.go" || f.Line != 7 || !strings.HasPrefix(f.Message, "panic: assignment to entry in nil map") {
		t.Fatalf("failure = %+v", f)
	}
}

func TestParseTestJSONPassed(t *testing.T) {
	out := `{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"pass","Package":"p","Test":"TestA"}
{"Action":"output","Package":"q","Output":"?   \tq\t[no test files]\n"}
{"Action":"skip","Package":"q"}
{"Action":"pass","Package":"p"}`
	rep := parseTestJSON(out)
	if rep.Status != "passed" || rep.Passed != 1 || rep.Failed != 0 || rep.Failures == nil || rep.BuildErrors == nil {
		t.Fatalf("got %+v", rep)
	}
}

func TestParseTestJSONPackageFailureOutsideTests(t *testing.T) {
	out := `{"Action":"output","Package":"p","Output":"panic: bad config\n"}
{"Action":"output","Package":"p","Output":"\t/work/p/config.go:12 +0x1\n"}
{"Action":"output","Package":"p","Output":"FAIL\tp\t0.01s\n"}
{"Action":"fail","Package":"p"}`
	rep := parseTestJSON(out)
	if rep.Status != "failed" || len(rep.Failures) != 1 {
		t.Fatalf("got %+v", rep)
	}
	f := rep.Failures[0]
	if f.Test != "" || f.File != "/work/p/config.go" || f.Line != 12 || !strings.Contains(f.Message, "panic: bad config") {
		t.Fatalf("failure = %+v", f)
	}
}

func TestParseTestJSONPlainBuildOutput(t *testing.T) {
	out := "# example.com/x\n./x.go:4:2: undefined: foo\n" +
		`{"Action":"output","Package":"example.com/x","Output":"FAIL\texample.com/x [build failed]\n"}` + "\n" +
		`{"Action":"fail","Package":"example.com/x","FailedBuild":"example.com/x"}`
	rep := parseTestJSON(out)
	if rep.Status != "failed" || rep.Failed != 0 || len(rep.BuildErrors) != 1 || rep.BuildErrors[0].File != "x.go" {
		t.Fatalf("got %+v", rep)
	}
}

func TestParseTestJSONParentFailureWithoutFailingChild(t *testing.T) {
	out := `{"Action":"pass","Package":"p","Test":"TestP/a"}
{"Action":"output","Package":"p","Test":"TestP","Output":"    p_test.go:20: cleanup failed\n"}
{"Action":"fail","Package":"p","Test":"TestP"}
{"Action":"fail","Package":"p"}`
	rep := parseTestJSON(out)
	if rep.Passed != 1 || rep.Failed != 1 || rep.Failures[0].Test != "TestP" || rep.Failures[0].Line != 20 {
		t.Fatalf("got %+v", rep)
	}
}

func TestParseDiagnostics(t *testing.T) {
	out := "# example.com/t2j/b\n# [example.com/t2j/b]\nvet: b/b.go:3:23: undefined: retryPolicy\n" +
		"./a.go:10:2: fmt.Printf format %d has arg s of wrong type string\n" +
		"c.go:5: missing return\n\tcontinued detail\n"
	got := parseDiagnostics(out, "vet")
	want := []Diagnostic{
		{File: "b/b.go", Line: 3, Column: 23, Severity: "error", Source: "vet", Message: "undefined: retryPolicy"},
		{File: "a.go", Line: 10, Column: 2, Severity: "error", Source: "vet", Message: "fmt.Printf format %d has arg s of wrong type string"},
		{File: "c.go", Line: 5, Severity: "error", Source: "vet", Message: "missing return\ncontinued detail"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestDedupeDiagnostics(t *testing.T) {
	d := Diagnostic{File: "a.go", Line: 1, Message: "x", Source: "build"}
	v := d
	v.Source = "vet"
	got, capped := dedupeDiagnostics([]Diagnostic{d, v})
	if len(got) != 1 || got[0].Source != "build" || capped {
		t.Fatalf("got %+v capped=%v", got, capped)
	}
}
