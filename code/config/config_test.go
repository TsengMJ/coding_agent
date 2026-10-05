package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultPaths(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	p, err := DefaultPaths("/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	if p.UserDir != filepath.Join("/home/tester", ".quill") {
		t.Errorf("UserDir = %q", p.UserDir)
	}
	if p.ProjectDir != filepath.Join("/work/repo", ".quill") {
		t.Errorf("ProjectDir = %q", p.ProjectDir)
	}
}

func TestDefaultPathsRequiresCwd(t *testing.T) {
	if _, err := DefaultPaths(""); err == nil {
		t.Fatal("want error for empty cwd")
	}
}
