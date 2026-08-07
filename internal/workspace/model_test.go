package workspace

import (
	"testing"

	"gew/internal/forge"
)

func TestNormalizeBackend(t *testing.T) {
	for input, want := range map[BackendKind]BackendKind{"": Gew, Gew: Gew, Git: Git} {
		got, err := NormalizeBackend(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeBackend(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := NormalizeBackend("other"); err == nil {
		t.Fatal("unknown backend was accepted")
	}
}

func TestTrackingRef(t *testing.T) {
	got, err := TrackingRef(forge.ForgeGitHub, "refs/heads/feature/api")
	if err != nil || got != "refs/gew/remotes/github/feature%2Fapi" {
		t.Fatalf("TrackingRef() = %q, %v", got, err)
	}
	for _, branch := range []string{"", "../main", "main.lock", "/main"} {
		if _, err := TrackingRef(forge.ForgeGitHub, branch); err == nil {
			t.Fatalf("unsafe branch %q was accepted", branch)
		}
	}
}
