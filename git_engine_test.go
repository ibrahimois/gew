package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func buildGitEngineFixture(t *testing.T) (string, plumbing.Hash, plumbing.Hash) {
	t.Helper()
	directory := t.TempDir()
	repository, err := git.PlainInit(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	branch := plumbing.NewBranchReferenceName("feature/مرحبا")
	if err := repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, branch)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "binary.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("binary.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("empty.txt"); err != nil {
		t.Fatal(err)
	}
	signature := &object.Signature{Name: "Gew Test", Email: "gew@example.invalid", When: time.Unix(1_700_000_000, 0).UTC()}
	first, err := worktree.Commit("first", &git.CommitOptions{Author: signature, Committer: signature})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "empty.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("empty.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "note.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("note.txt"); err != nil {
		t.Fatal(err)
	}
	status, err := worktree.Status()
	if err != nil || status.IsClean() {
		t.Fatalf("staged status = %#v, %v", status, err)
	}
	second, err := worktree.Commit("second", &git.CommitOptions{Author: signature, Committer: signature})
	if err != nil {
		t.Fatal(err)
	}
	firstCommit, err := repository.CommitObject(first)
	if err != nil {
		t.Fatal(err)
	}
	secondCommit, err := repository.CommitObject(second)
	if err != nil {
		t.Fatal(err)
	}
	firstTree, _ := firstCommit.Tree()
	secondTree, _ := secondCommit.Tree()
	changes, err := firstTree.Diff(secondTree)
	if err != nil || len(changes) != 2 {
		t.Fatalf("tree changes = %#v, %v", changes, err)
	}
	privateName := plumbing.ReferenceName("refs/gew/remotes/gitea/feature%2F%D9%85%D8%B1%D8%AD%D8%A8%D8%A7")
	initialRef := plumbing.NewHashReference(privateName, first)
	if err := repository.Storer.SetReference(initialRef); err != nil {
		t.Fatal(err)
	}
	advancedRef := plumbing.NewHashReference(privateName, second)
	if err := repository.Storer.CheckAndSetReference(advancedRef, initialRef); err != nil {
		t.Fatal(err)
	}
	if err := repository.Storer.CheckAndSetReference(initialRef, initialRef); err == nil {
		t.Fatal("stale private-ref compare-and-swap succeeded")
	}
	reopened, err := git.PlainOpen(directory)
	if err != nil {
		t.Fatal(err)
	}
	head, err := reopened.Head()
	if err != nil || head.Hash() != second || head.Name() != branch {
		t.Fatalf("reopened head = %#v, %v", head, err)
	}
	reopenedWorktree, _ := reopened.Worktree()
	reopenedStatus, err := reopenedWorktree.Status()
	if err != nil || !reopenedStatus.IsClean() {
		t.Fatalf("reopened status = %#v, %v", reopenedStatus, err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "binary.bin"))
	if err != nil || !bytes.Equal(data, []byte{'a', 0, 'b'}) {
		t.Fatalf("binary = %v, %v", data, err)
	}
	return directory, first, second
}

func TestGitEnginePureGoRepository(t *testing.T) {
	_, first, second := buildGitEngineFixture(t)
	if first.IsZero() || second.IsZero() || first == second {
		t.Fatalf("commit IDs = %s, %s", first, second)
	}
}

func TestGitCLIConformance(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if errors.Is(err, exec.ErrNotFound) {
		t.Skip("system git is unavailable for read-only conformance inspection")
	}
	if err != nil {
		t.Fatal(err)
	}
	directory, _, second := buildGitEngineFixture(t)
	commands := [][]string{
		{"fsck", "--full"},
		{"status", "--porcelain"},
		{"rev-parse", "HEAD"},
		{"show-ref", "refs/gew/remotes/gitea/feature%2F%D9%85%D8%B1%D8%AD%D8%A8%D8%A7"},
	}
	for _, arguments := range commands {
		command := exec.Command(gitPath, arguments...)
		command.Dir = directory
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
		if arguments[0] == "status" && len(bytes.TrimSpace(output)) != 0 {
			t.Fatalf("git status = %q", output)
		}
		if arguments[0] == "rev-parse" && strings.TrimSpace(string(output)) != second.String() {
			t.Fatalf("git rev-parse = %q, want %s", output, second)
		}
	}
}
