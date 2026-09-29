package notimpl

import (
	"errors"
	"strings"
	"testing"
)

func TestErrWrapsSentinel(t *testing.T) {
	if !errors.Is(Err("pkg.Func"), ErrNotImplemented) {
		t.Fatal("Err must wrap ErrNotImplemented")
	}
}

func TestErrIncludesLocation(t *testing.T) {
	if !strings.Contains(Err("pkg.Func").Error(), "pkg.Func") {
		t.Fatal("Err message must include the function location")
	}
}
