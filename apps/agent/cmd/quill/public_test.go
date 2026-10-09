package main

import (
	"slices"
	"testing"
)

func TestPublicRejectsUnknownLogFormat(t *testing.T) {
	pub := publicFlags{enabled: true, logFormat: "xml"}
	if err := pub.check(&workspaceFlags{root: "/srv/ws"}); err == nil {
		t.Fatal("-log-format xml was accepted")
	}
}

func TestPublicNeedsWorkspaces(t *testing.T) {
	pub := publicFlags{enabled: true, logFormat: "json"}
	if err := pub.check(&workspaceFlags{}); err == nil {
		t.Fatal("-public without -workspaces was accepted")
	}
	if err := pub.check(&workspaceFlags{root: "/srv/ws", localRepos: true}); err == nil {
		t.Fatal("-public with -workspace-local-repos was accepted")
	}
	ws := workspaceFlags{root: "/srv/ws"}
	if err := pub.check(&ws); err != nil {
		t.Fatal(err)
	}
	if !ws.require {
		t.Fatal("-public did not require workspaces")
	}
}

func TestDailyLimitNeedsPublic(t *testing.T) {
	pub := publicFlags{dailyLimitUSD: 5}
	if err := pub.check(&workspaceFlags{}); err == nil {
		t.Fatal("-daily-limit-usd without -public was accepted")
	}
}

func TestAllowedOrigins(t *testing.T) {
	pub := publicFlags{origins: " app://quill , http://localhost:5173,,"}
	if got := pub.allowedOrigins(); !slices.Equal(got, []string{"app://quill", "http://localhost:5173"}) {
		t.Fatalf("origins = %q", got)
	}
}

func TestAuthServiceNeedsTheSecretKey(t *testing.T) {
	t.Setenv("QUILL_SECRET_KEY", "")
	if _, err := authService(nil); err == nil {
		t.Fatal("auth service without QUILL_SECRET_KEY was built")
	}
}
