// Package forgetest contains reusable contract checks for forge adapters.
package forgetest

import (
	"testing"

	"gew/internal/forge"
)

// RunBaseContract verifies the roles and advertised capabilities of an adapter.
func RunBaseContract(t testing.TB, remote forge.Forge, kind forge.ForgeKind, nativeSnapshot, writer, push bool) {
	t.Helper()
	if remote.Kind() != kind {
		t.Fatalf("Kind() = %q, want %q", remote.Kind(), kind)
	}
	if _, ok := any(remote).(forge.RepositoryReader); !ok {
		t.Fatal("adapter does not implement RepositoryReader")
	}
	if _, ok := any(remote).(forge.ForgeSnapshotter); ok != nativeSnapshot {
		t.Fatalf("ForgeSnapshotter = %v, want %v", ok, nativeSnapshot)
	}
	if _, ok := any(remote).(forge.ForgeCommitWriter); ok != writer {
		t.Fatalf("ForgeCommitWriter = %v, want %v", ok, writer)
	}
	if remote.Capabilities().Push != push {
		t.Fatalf("Push = %v, want %v", remote.Capabilities().Push, push)
	}
	if remote.Capabilities().Push && !writer {
		t.Fatal("push-enabled adapter has no writer role")
	}
	if remote.Capabilities().BranchCreate && !writer {
		t.Fatal("branch creation requires a writer role")
	}
}
