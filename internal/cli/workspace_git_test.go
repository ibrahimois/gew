package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
)

func gitTestHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func makeGitWorkspace(t *testing.T) (string, workspaceState) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := workspaceState{
		Version: stateVersion, Backend: WorkspaceGit, Provider: ForgeGitea,
		Remote: RepositoryRef{Forge: ForgeGitea, Server: "https://example.test", Namespace: "team", Name: "repo"},
		Branch: "main", BaseCommit: "remote-base", Files: map[string]fileState{"README.md": {Hash: gitTestHash([]byte("base\n"))}},
	}
	if err := initializeGitWorkspace(root, &state, false); err != nil {
		t.Fatal(err)
	}
	if err := saveState(root, state); err != nil {
		t.Fatal(err)
	}
	if err := saveGitExportReceipt(root, gitExportReceipt{Version: exportReceiptVersion, LocalOID: state.Hybrid.LastLocalOID, ProviderID: state.BaseCommit}); err != nil {
		t.Fatal(err)
	}
	return root, state
}

func TestGitWorkspaceAddResetDiffCommitLogAndStatus(t *testing.T) {
	root, state := makeGitWorkspace(t)
	var output bytes.Buffer
	a := app{out: &output, errOut: &output}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(root, "README.md")
	if err := a.gitAdd(root, state, []string{readmePath}, false); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := a.gitDiff(root, state, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "-base") || !strings.Contains(output.String(), "+changed") {
		t.Fatalf("staged diff = %q", output.String())
	}
	if err := a.gitReset(root, state, []string{readmePath}); err != nil {
		t.Fatal(err)
	}
	repository, _ := git.PlainOpen(root)
	worktree, _ := repository.Worktree()
	status, _ := worktree.Status()
	if status["README.md"].Staging != git.Unmodified || status["README.md"].Worktree == git.Unmodified {
		t.Fatalf("status after reset = %#v", status)
	}
	if err := a.gitAdd(root, state, []string{readmePath}, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEW_AUTHOR_NAME", "Gew User")
	t.Setenv("GEW_AUTHOR_EMAIL", "gew@example.invalid")
	if err := a.gitCommit(root, state, "local change", "", ""); err != nil {
		t.Fatal(err)
	}
	pending, err := pendingGitCommits(repository, state.Hybrid.TrackingRef)
	if err != nil || len(pending) != 1 || pending[0].Hash.String() == state.BaseCommit {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	output.Reset()
	if err := a.gitStatus(root, state, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1 unpushed local Git commit") {
		t.Fatalf("status output = %q", output.String())
	}
	output.Reset()
	if err := a.gitLog(root, state, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "[unpushed]") || !strings.Contains(output.String(), "local change") {
		t.Fatalf("log output = %q", output.String())
	}
}

func TestStateV4BackendDefaultsAndGitRefsFailClosed(t *testing.T) {
	if backend, err := normalizeWorkspaceBackend(""); err != nil || backend != WorkspaceGew {
		t.Fatalf("default backend = %q, %v", backend, err)
	}
	for _, branch := range []string{"../main", "main.lock", "bad\nname", "/main"} {
		if _, err := gewTrackingRef(ForgeGitea, branch); err == nil {
			t.Fatalf("hostile branch %q was accepted", branch)
		}
	}
	ref, err := gewTrackingRef(ForgeGitHub, "feature/unicode-مرحبا")
	if err != nil || !strings.HasPrefix(ref, "refs/gew/remotes/github/") || strings.Contains(ref, "../") {
		t.Fatalf("tracking ref = %q, %v", ref, err)
	}
	legacy := workspaceState{Version: 3}
	data, _ := json.Marshal(legacy)
	var decoded workspaceState
	json.Unmarshal(data, &decoded)
	backend, err := normalizeWorkspaceBackend(decoded.Backend)
	if err != nil || backend != WorkspaceGew || decoded.Version != 3 {
		t.Fatalf("legacy decode = %#v, %v", decoded, err)
	}
}

func TestGitWorkspaceRequiresExplicitIdentityBeforeCommit(t *testing.T) {
	root, state := makeGitWorkspace(t)
	repository, _ := git.PlainOpen(root)
	configuration, _ := repository.Config()
	configuration.User.Name = ""
	configuration.User.Email = ""
	if err := repository.SetConfig(configuration); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEW_AUTHOR_NAME", "")
	t.Setenv("GEW_AUTHOR_EMAIL", "")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	if err := a.gitAdd(root, state, []string{filepath.Join(root, "README.md")}, false); err != nil {
		t.Fatal(err)
	}
	if err := a.gitCommit(root, state, "message", "", ""); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("identity error = %v", err)
	}
}

func TestGitPullCleanImportsRemoteAnchor(t *testing.T) {
	root, state := makeGitWorkspace(t)
	a := app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	remote := &gitExportForge{head: "remote-next", files: map[string][]byte{"README.md": []byte("remote\n"), "new.txt": []byte("new\n")}}
	if err := a.gitPull(root, state, remote, false); err != nil {
		t.Fatal(err)
	}
	_, updated, err := readWorkspaceAt(root)
	if err != nil || updated.BaseCommit != "remote-next" || updated.Hybrid.LastProviderID != "remote-next" {
		t.Fatalf("updated = %#v, %v", updated, err)
	}
	repository, _ := git.PlainOpen(root)
	pending, err := pendingGitCommits(repository, updated.Hybrid.TrackingRef)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(content) != "remote\n" {
		t.Fatalf("README = %q", content)
	}
}

func TestGitPullMergesPendingCommitAndConflictAbort(t *testing.T) {
	root, state := makeGitWorkspace(t)
	a := app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	t.Setenv("GEW_AUTHOR_NAME", "Gew User")
	t.Setenv("GEW_AUTHOR_EMAIL", "gew@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.gitAdd(root, state, []string{filepath.Join(root, "README.md")}, false); err != nil {
		t.Fatal(err)
	}
	if err := a.gitCommit(root, state, "local", "", ""); err != nil {
		t.Fatal(err)
	}
	repository, _ := git.PlainOpen(root)
	oldHead, _ := repository.Head()
	remote := &gitExportForge{head: "remote-next", files: map[string][]byte{"README.md": []byte("remote\n")}}
	err := a.gitPull(root, state, remote, false)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("pull conflict = %v", err)
	}
	conflicted, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if !strings.Contains(string(conflicted), "<<<<<<< ours") {
		t.Fatalf("conflicted README = %q", conflicted)
	}
	_, during, _ := readWorkspaceAt(root)
	if err := a.gitMerge(root, during, true, false, ""); err != nil {
		t.Fatal(err)
	}
	repository, _ = git.PlainOpen(root)
	restoredHead, _ := repository.Head()
	if restoredHead.Hash() != oldHead.Hash() {
		t.Fatalf("restored head = %s, want %s", restoredHead.Hash(), oldHead.Hash())
	}
	restored, _ := os.ReadFile(filepath.Join(root, "README.md"))
	if string(restored) != "local\n" {
		t.Fatalf("restored README = %q", restored)
	}
	_, restoredState, err := readWorkspaceAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateHybridState(root, &restoredState); err != nil {
		t.Fatalf("aborted workspace did not reopen cleanly: %v", err)
	}
}

func TestGitPullMergesNonOverlappingPendingCommit(t *testing.T) {
	root, state := makeGitWorkspace(t)
	a := app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	t.Setenv("GEW_AUTHOR_NAME", "Gew User")
	t.Setenv("GEW_AUTHOR_EMAIL", "gew@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.gitAdd(root, state, []string{filepath.Join(root, "local.txt")}, false); err != nil {
		t.Fatal(err)
	}
	if err := a.gitCommit(root, state, "local", "", ""); err != nil {
		t.Fatal(err)
	}
	remote := &gitExportForge{head: "remote-next", files: map[string][]byte{"README.md": []byte("base\n"), "remote.txt": []byte("remote\n")}}
	if err := a.gitPull(root, state, remote, false); err != nil {
		t.Fatal(err)
	}
	_, updated, _ := readWorkspaceAt(root)
	repository, _ := git.PlainOpen(root)
	pending, err := pendingGitCommits(repository, updated.Hybrid.TrackingRef)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
	for _, name := range []string{"README.md", "local.txt", "remote.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitMergeContinueCreatesPendingResolution(t *testing.T) {
	root, state := makeGitWorkspace(t)
	a := app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	t.Setenv("GEW_AUTHOR_NAME", "Gew User")
	t.Setenv("GEW_AUTHOR_EMAIL", "gew@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.gitAdd(root, state, []string{filepath.Join(root, "README.md")}, false); err != nil {
		t.Fatal(err)
	}
	if err := a.gitCommit(root, state, "local", "", ""); err != nil {
		t.Fatal(err)
	}
	remote := &gitExportForge{head: "remote-next", files: map[string][]byte{"README.md": []byte("remote\n")}}
	if err := a.gitPull(root, state, remote, false); err == nil {
		t.Fatal("expected merge conflict")
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, during, _ := readWorkspaceAt(root)
	if err := a.gitMerge(root, during, false, true, "resolve conflict"); err != nil {
		t.Fatal(err)
	}
	repository, _ := git.PlainOpen(root)
	pending, err := pendingGitCommits(repository, during.Hybrid.TrackingRef)
	if err != nil || len(pending) != 1 || pending[0].Message != "resolve conflict" {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
}
