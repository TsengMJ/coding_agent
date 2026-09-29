package main

import "testing"

func TestResolvePrompt(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "flag", flag: "summarize README", want: "summarize README"},
		{name: "args", args: []string{"fix", "the", "tests"}, want: "fix the tests"},
		{name: "empty stays interactive"},
		{name: "both", flag: "a", args: []string{"b"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePrompt(tt.flag, tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePrompt: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
