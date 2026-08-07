package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeGitea struct {
	mu              sync.Mutex
	commit          int
	files           map[string][]byte
	messages        []string
	branches        map[string]bool
	failMessage     string
	lastParent      string
	lastChanged     []string
	resetAfterApply bool
	snapshots       map[string]map[string][]byte
}

func newFakeGitea() *fakeGitea {
	fake := &fakeGitea{
		commit: 1,
		files: map[string][]byte{
			"README.md":       []byte("hello\n"),
			"config/app.json": []byte("{\"enabled\":true}\n"),
		},
	}
	fake.snapshots = map[string]map[string][]byte{fake.commitSHA(): cloneByteMap(fake.files)}
	return fake
}

func cloneByteMap(source map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(source))
	for key, value := range source {
		result[key] = append([]byte(nil), value...)
	}
	return result
}

func (f *fakeGitea) recordSnapshotLocked() {
	if f.snapshots == nil {
		f.snapshots = make(map[string]map[string][]byte)
	}
	f.snapshots[f.commitSHA()] = cloneByteMap(f.files)
}

func (f *fakeGitea) commitSHA() string {
	return fmt.Sprintf("%040d", f.commit)
}

func fakeBlobSHA(content []byte) string {
	hasher := sha1.New()
	fmt.Fprintf(hasher, "blob %d%c", len(content), 0)
	hasher.Write(content)
	return hex.EncodeToString(hasher.Sum(nil))
}

func (f *fakeGitea) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	response.Header().Set("Content-Type", "application/json")

	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/version":
		json.NewEncoder(response).Encode(map[string]string{"version": "1.27.0-test"})
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repos/acme/demo":
		json.NewEncoder(response).Encode(giteaRepository{DefaultBranch: "main", Empty: f.commit == 0})
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/v1/repos/acme/demo/branches/"):
		branch := strings.TrimPrefix(request.URL.Path, "/api/v1/repos/acme/demo/branches/")
		exists := branch == "main"
		if f.branches != nil {
			exists = f.branches[branch]
		}
		if f.commit == 0 || !exists {
			http.NotFound(response, request)
			return
		}
		json.NewEncoder(response).Encode(map[string]any{
			"name": branch, "commit": map[string]string{"id": f.commitSHA()},
		})
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/v1/repos/acme/demo/git/trees/"):
		entries := make([]giteaTreeEntry, 0, len(f.files))
		for name, content := range f.files {
			entries = append(entries, giteaTreeEntry{Path: name, SHA: fakeBlobSHA(content), Type: "blob", Mode: "100644"})
		}
		json.NewEncoder(response).Encode(giteaTreeResponse{Tree: entries})
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/v1/repos/acme/demo/git/blobs/"):
		sha := strings.TrimPrefix(request.URL.Path, "/api/v1/repos/acme/demo/git/blobs/")
		for _, content := range f.files {
			if fakeBlobSHA(content) == sha {
				json.NewEncoder(response).Encode(giteaBlobResponse{Content: base64.StdEncoding.EncodeToString(content), Encoding: "base64", SHA: sha})
				return
			}
		}
		http.NotFound(response, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/v1/repos/acme/demo/git/commits/"):
		sha := strings.TrimPrefix(request.URL.Path, "/api/v1/repos/acme/demo/git/commits/")
		if sha != f.commitSHA() {
			http.NotFound(response, request)
			return
		}
		details := giteaCommitDetails{SHA: sha}
		if len(f.messages) > 0 {
			details.Commit.Message = f.messages[len(f.messages)-1]
		}
		if f.lastParent != "" {
			details.Parents = append(details.Parents, struct {
				SHA string `json:"sha"`
			}{SHA: f.lastParent})
		}
		for _, filePath := range f.lastChanged {
			details.Files = append(details.Files, struct {
				Filename string `json:"filename"`
			}{Filename: filePath})
		}
		json.NewEncoder(response).Encode(details)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/v1/repos/acme/demo/archive/"):
		response.Header().Set("Content-Type", "application/zip")
		ref := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/api/v1/repos/acme/demo/archive/"), ".zip")
		archiveFiles := f.files
		if ref != "main" {
			var exists bool
			archiveFiles, exists = f.snapshots[ref]
			if !exists {
				http.NotFound(response, request)
				return
			}
		}
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		for name, content := range archiveFiles {
			file, _ := writer.Create("demo-" + f.commitSHA() + "/" + name)
			file.Write(content)
		}
		writer.Close()
		response.Write(archive.Bytes())
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/repos/acme/demo/contents":
		var payload giteaChangeFilesRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.Message == f.failMessage {
			http.Error(response, "injected push failure", http.StatusInternalServerError)
			return
		}
		parent := ""
		if f.commit > 0 {
			parent = f.commitSHA()
		}
		changed := make([]string, 0, len(payload.Files))
		for _, operation := range payload.Files {
			changed = append(changed, operation.Path)
			switch operation.Operation {
			case "create", "update":
				content, err := base64.StdEncoding.DecodeString(operation.Content)
				if err != nil {
					http.Error(response, err.Error(), http.StatusBadRequest)
					return
				}
				f.files[operation.Path] = content
			case "delete":
				delete(f.files, operation.Path)
			}
		}
		if payload.NewBranch != "" {
			if f.branches == nil {
				f.branches = map[string]bool{"main": f.commit > 0}
			}
			f.branches[payload.NewBranch] = true
		}
		f.messages = append(f.messages, payload.Message)
		f.lastParent = parent
		f.lastChanged = changed
		f.commit++
		f.recordSnapshotLocked()
		if f.resetAfterApply {
			f.resetAfterApply = false
			connection, _, hijackErr := response.(http.Hijacker).Hijack()
			if hijackErr == nil {
				connection.Close()
			}
			return
		}
		response.WriteHeader(http.StatusCreated)
		json.NewEncoder(response).Encode(map[string]any{"commit": map[string]string{"sha": f.commitSHA()}})
	default:
		http.NotFound(response, request)
	}
}

func TestEndToEndWorkflow(t *testing.T) {
	fake := newFakeGitea()
	server := httptest.NewServer(fake)
	defer server.Close()

	t.Setenv("GEW_SERVER", server.URL)
	t.Setenv("GEW_TOKEN", "test-token")
	t.Setenv("GEW_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDirectory)
	parent := t.TempDir()
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	a := app{out: &output, errOut: &output}
	if err := a.clone([]string{"acme/demo", "checkout"}); err != nil {
		t.Fatalf("clone: %v", err)
	}
	checkout := filepath.Join(parent, "checkout")
	if content, err := os.ReadFile(filepath.Join(checkout, "README.md")); err != nil || string(content) != "hello\n" {
		t.Fatalf("unexpected cloned content %q, %v", content, err)
	}
	if err := os.Chdir(filepath.Join(checkout, "config")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := a.status(nil); err != nil {
		t.Fatalf("clean status: %v", err)
	}
	if !strings.Contains(output.String(), "Workspace is clean") {
		t.Fatalf("unexpected status output: %s", output.String())
	}

	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("changed locally\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(checkout, "config", "app.json")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"modified README.md", "deleted  config/app.json", "created  new.txt"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("status missing %q:\n%s", expected, output.String())
		}
	}

	output.Reset()
	if err := a.add([]string{"-A"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := a.commit([]string{"-m", "test change"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := a.push(nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	fake.mu.Lock()
	if string(fake.files["README.md"]) != "changed locally\n" || string(fake.files["new.txt"]) != "new\n" {
		t.Fatalf("push did not update remote files: %#v", fake.files)
	}
	if _, exists := fake.files["config/app.json"]; exists {
		t.Fatal("push did not delete remote file")
	}
	fake.files["README.md"] = []byte("changed remotely\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()

	output.Reset()
	if err := a.pull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(checkout, "README.md"))
	if err != nil || string(content) != "changed remotely\n" {
		t.Fatalf("pull did not update local file: %q, %v", content, err)
	}

	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("another local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"../README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "must fail"}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.files["server.txt"] = []byte("server change\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.push(nil); err == nil || !strings.Contains(err.Error(), "remote branch advanced") {
		t.Fatalf("expected remote-advanced error, got %v", err)
	}
	if err := a.pull(nil); err != nil {
		t.Fatalf("merge pull with queued commit: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(checkout, "server.txt")); err != nil || string(content) != "server change\n" {
		t.Fatalf("merge pull did not include remote file: %q, %v", content, err)
	}
	_, mergedState, err := findWorkspace()
	if err != nil || len(mergedState.Queue) != 1 {
		t.Fatalf("merge pull did not create one replacement commit: %#v, %v", mergedState.Queue, err)
	}
}

func TestEmptyRepositoryFirstPush(t *testing.T) {
	fake := &fakeGitea{files: make(map[string][]byte)}
	server := httptest.NewServer(fake)
	defer server.Close()

	t.Setenv("GEW_SERVER", server.URL)
	t.Setenv("GEW_TOKEN", "test-token")
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDirectory)
	parent := t.TempDir()
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	a := app{out: &output, errOut: &output}
	if err := a.clone([]string{"acme/demo", "checkout"}); err != nil {
		t.Fatalf("clone empty repository: %v", err)
	}
	checkout := filepath.Join(parent, "checkout")
	if err := os.WriteFile(filepath.Join(checkout, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"-A"}); err != nil {
		t.Fatalf("add first file: %v", err)
	}
	if err := a.commit([]string{"-m", "Initial commit"}); err != nil {
		t.Fatalf("initial local commit: %v", err)
	}
	if err := a.push(nil); err != nil {
		t.Fatalf("first push: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.commit != 1 || string(fake.files["main.go"]) != "package main\n" {
		t.Fatalf("unexpected remote after first push: commit=%d files=%#v", fake.commit, fake.files)
	}
}

func TestStagingSnapshotAndSequentialPush(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, output := clonedTestWorkspace(t, fake)
	_, initialState, err := findWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(objectPath(checkout, initialState.Files["README.md"].Hash)); err != nil {
		t.Fatal(err)
	}

	readme := filepath.Join(checkout, "README.md")
	if err := os.WriteFile(readme, []byte("first staged version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatalf("add first version: %v", err)
	}
	if err := os.WriteFile(readme, []byte("second working version\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Changes to be committed") || !strings.Contains(output.String(), "Changes not staged") {
		t.Fatalf("status did not separate staged and unstaged changes:\n%s", output.String())
	}
	output.Reset()
	if err := a.diff([]string{"--staged"}); err != nil {
		t.Fatalf("staged diff: %v", err)
	}
	if !strings.Contains(output.String(), "-hello\n") || !strings.Contains(output.String(), "+first staged version\n") {
		t.Fatalf("unexpected staged diff:\n%s", output.String())
	}
	output.Reset()
	if err := a.diff(nil); err != nil {
		t.Fatalf("unstaged diff: %v", err)
	}
	if !strings.Contains(output.String(), "-first staged version\n") || !strings.Contains(output.String(), "+second working version\n") {
		t.Fatalf("unexpected unstaged diff:\n%s", output.String())
	}

	if err := a.commit([]string{"-m", "First local commit"}); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatalf("add second version: %v", err)
	}
	if err := a.commit([]string{"-m", "Second local commit"}); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	output.Reset()
	if err := a.log([]string{"--oneline"}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(output.String(), "Second local commit") || !strings.Contains(output.String(), "First local commit") || !strings.Contains(output.String(), "unpushed") {
		t.Fatalf("unexpected local log:\n%s", output.String())
	}
	if err := a.push(nil); err != nil {
		t.Fatalf("sequential push: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := string(fake.files["README.md"]); got != "second working version\n" {
		t.Fatalf("final remote content = %q", got)
	}
	if len(fake.messages) != 2 || fake.messages[0] != "First local commit" || fake.messages[1] != "Second local commit" {
		t.Fatalf("remote commit order/messages = %#v", fake.messages)
	}
}

func TestAddPathScopesResetAndRestaging(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)

	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("readme edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "config", "extra.txt"), []byte("extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(checkout, "config", "app.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(checkout, "config")); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"."}); err != nil {
		t.Fatalf("add directory: %v", err)
	}
	index, err := loadIndex(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 2 || index.Entries["README.md"].Kind != "" {
		t.Fatalf("directory pathspec staged wrong files: %#v", index.Entries)
	}
	if err := a.reset([]string{"extra.txt"}); err != nil {
		t.Fatalf("reset one file: %v", err)
	}
	index, _ = loadIndex(checkout)
	if len(index.Entries) != 1 || index.Entries["config/app.json"].Kind != "deleted" {
		t.Fatalf("single reset left wrong index: %#v", index.Entries)
	}
	if err := a.reset(nil); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"-A"}); err != nil {
		t.Fatalf("add all: %v", err)
	}
	index, _ = loadIndex(checkout)
	if len(index.Entries) != 3 {
		t.Fatalf("add -A staged %d entries, want 3: %#v", len(index.Entries), index.Entries)
	}
	if err := os.Remove(filepath.Join(checkout, "config", "extra.txt")); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"-A"}); err != nil {
		t.Fatalf("restage removed new file: %v", err)
	}
	index, _ = loadIndex(checkout)
	if _, exists := index.Entries["config/extra.txt"]; exists {
		t.Fatalf("new file deleted after staging remained in index: %#v", index.Entries)
	}
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"-A"}); err != nil {
		t.Fatalf("restage restored file: %v", err)
	}
	index, _ = loadIndex(checkout)
	if len(index.Entries) != 1 || index.Entries["config/app.json"].Kind != "deleted" {
		t.Fatalf("restoring baseline did not unstage change: %#v", index.Entries)
	}
}

func TestPullRefusesStagedAndMergesUnstagedChanges(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	readme := filepath.Join(checkout, "README.md")
	if err := os.WriteFile(readme, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.pull(nil); err == nil || !strings.Contains(err.Error(), "staged changes") {
		t.Fatalf("expected staged pull refusal, got %v", err)
	}
	if err := a.reset(nil); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.files["remote.txt"] = []byte("remote\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.pull(nil); err != nil {
		t.Fatalf("merge unstaged change: %v", err)
	}
	if content, err := os.ReadFile(readme); err != nil || string(content) != "local\n" {
		t.Fatalf("local change was not preserved: %q, %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(checkout, "remote.txt")); err != nil || string(content) != "remote\n" {
		t.Fatalf("remote change missing after merge: %q, %v", content, err)
	}
}

func TestBinaryDiffAndJSONStatus(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, output := clonedTestWorkspace(t, fake)
	if err := os.WriteFile(filepath.Join(checkout, "asset.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"asset.bin"}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := a.diff([]string{"--staged"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Binary files differ") {
		t.Fatalf("binary diff not detected:\n%s", output.String())
	}
	output.Reset()
	if err := a.status([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Staged   []change `json:"staged"`
		Unstaged []change `json:"unstaged"`
	}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatalf("invalid JSON status: %v\n%s", err, output.String())
	}
	if len(status.Staged) != 1 || status.Staged[0].Path != "asset.bin" || len(status.Unstaged) != 0 {
		t.Fatalf("unexpected JSON status: %#v", status)
	}
}

func TestCommandErrorsAndPathSafety(t *testing.T) {
	fake := newFakeGitea()
	a, _, _ := clonedTestWorkspace(t, fake)
	if err := a.commit([]string{"-m", "empty"}); err == nil || !strings.Contains(err.Error(), "nothing staged") {
		t.Fatalf("expected empty commit error, got %v", err)
	}
	if err := a.add([]string{"../outside.txt"}); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("expected outside path error, got %v", err)
	}
	if err := a.add([]string{"missing.txt"}); err == nil || !strings.Contains(err.Error(), "did not match") {
		t.Fatalf("expected unmatched pathspec error, got %v", err)
	}
	if err := a.add([]string{".gew/state.json"}); err == nil || !strings.Contains(err.Error(), "internal metadata") {
		t.Fatalf("expected metadata path error, got %v", err)
	}
}

func TestPushToNewBranch(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "Feature commit"}); err != nil {
		t.Fatal(err)
	}
	if err := a.push([]string{"--new-branch", "feature/demo"}); err != nil {
		t.Fatalf("push new branch: %v", err)
	}
	_, state, err := findWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if state.Branch != "feature/demo" || len(state.Queue) != 0 {
		t.Fatalf("unexpected state after new branch push: %#v", state)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.branches["feature/demo"] {
		t.Fatalf("new branch was not requested: %#v", fake.branches)
	}
}

func TestPartialPushCheckpointsAndResumes(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	readme := filepath.Join(checkout, "README.md")
	if err := os.WriteFile(readme, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "first"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"second.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "second"}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.failMessage = "second"
	fake.mu.Unlock()
	if err := a.push(nil); err == nil || !strings.Contains(err.Error(), "injected push failure") {
		t.Fatalf("expected injected second-commit failure, got %v", err)
	}
	_, state, err := findWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Queue) != 1 {
		t.Fatalf("queue after partial push = %#v", state.Queue)
	}
	fake.mu.Lock()
	if string(fake.files["README.md"]) != "first\n" {
		t.Fatalf("first commit was not pushed before failure")
	}
	if _, exists := fake.files["second.txt"]; exists {
		t.Fatalf("failed second commit changed remote")
	}
	fake.failMessage = ""
	fake.mu.Unlock()
	if err := a.push(nil); err != nil {
		t.Fatalf("resume push: %v", err)
	}
	_, state, _ = findWorkspace()
	if len(state.Queue) != 0 {
		t.Fatalf("queue not empty after resume: %#v", state.Queue)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if string(fake.files["second.txt"]) != "second\n" {
		t.Fatalf("resumed commit did not reach remote")
	}
}

func TestAmbiguousSuccessfulPushReconcilesOnRetry(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, output := clonedTestWorkspace(t, fake)
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("ambiguous\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "ambiguous commit"}); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.resetAfterApply = true
	fake.mu.Unlock()
	if err := a.push(nil); err == nil {
		t.Fatal("expected connection failure after remote applied commit")
	}
	_, state, err := findWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Queue) != 1 {
		t.Fatalf("ambiguous push should remain queued: %#v", state.Queue)
	}
	output.Reset()
	if err := a.push(nil); err != nil {
		t.Fatalf("reconcile retry: %v", err)
	}
	if !strings.Contains(output.String(), "Reconciled already-applied commit") {
		t.Fatalf("retry did not report reconciliation:\n%s", output.String())
	}
	_, state, _ = findWorkspace()
	if len(state.Queue) != 0 {
		t.Fatalf("reconciled queue not empty: %#v", state.Queue)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.messages) != 1 {
		t.Fatalf("retry duplicated remote commit: %#v", fake.messages)
	}
}

func TestTreePagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		page := request.URL.Query().Get("page")
		if page == "1" {
			json.NewEncoder(response).Encode(giteaTreeResponse{Tree: []giteaTreeEntry{{Path: "one.txt", SHA: "one", Type: "blob"}}, Truncated: true})
			return
		}
		json.NewEncoder(response).Encode(giteaTreeResponse{Tree: []giteaTreeEntry{{Path: "two.txt", SHA: "two", Type: "blob"}}})
	}))
	defer server.Close()
	forge, err := forgeFromProfile(profile{Provider: ForgeGitea, URL: server.URL, Token: "token", AuthKind: AuthToken})
	if err != nil {
		t.Fatal(err)
	}
	files, err := forge.Tree(context.Background(), RepositoryRef{Forge: ForgeGitea, Namespace: "acme", Name: "demo"}, "commit")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files["one.txt"].BlobID != "one" || files["two.txt"].BlobID != "two" {
		t.Fatalf("paginated tree = %#v", files)
	}
}

func TestVersionOneWorkspaceMigratesInMemory(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDirectory)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".gew"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"server":"https://example.test","owner":"a","repository":"b","branch":"main","base_commit":"abc","files":{}}`
	if err := os.WriteFile(filepath.Join(root, ".gew", "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	_, state, err := findWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 1 || state.Backend != WorkspaceGew || state.Files == nil {
		t.Fatalf("legacy state was not migrated: %#v", state)
	}
}

func TestVersionTwoWorkspaceMigrationPreservesProviderNeutralIdentity(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDirectory)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".gew"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":2,"server":"https://example.test","owner":"team","repository":"demo","branch":"main","base_commit":"abc","files":{},"queue":["queued"],"history":["queued"]}`
	if err := os.WriteFile(filepath.Join(root, ".gew", "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	_, state, err := findWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 2 || state.Backend != WorkspaceGew || state.Provider != ForgeGitea || state.Remote.Namespace != "team" || state.Remote.Name != "demo" {
		t.Fatalf("legacy identity was not migrated: %#v", state)
	}
	if len(state.Queue) != 1 || len(state.History) != 1 || state.BaseCommit != "abc" {
		t.Fatalf("legacy queue/history/base were not preserved: %#v", state)
	}
	if err := saveState(root, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gew", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "token") {
		t.Fatalf("workspace state contains a credential field: %s", data)
	}
	var persisted workspaceState
	if err := json.Unmarshal(data, &persisted); err != nil || persisted.Version != stateVersion {
		t.Fatalf("explicit mutation did not upgrade state to version %d: %#v, %v", stateVersion, persisted, err)
	}
}

func TestProfileMigrationDefaultsToGiteaInMemory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("GEW_CONFIG", configPath)
	t.Setenv("GEW_SERVER", "")
	t.Setenv("GEW_TOKEN", "")
	legacy := `{"current":"default","profiles":{"default":{"url":"https://example.test","token":"secret"}}}`
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := profileFromConfig()
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != ForgeGitea || p.AuthKind != AuthToken {
		t.Fatalf("legacy profile was not migrated in memory: %#v", p)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "provider") {
		t.Fatalf("read-only migration rewrote profile: %s", data)
	}
}

func TestFutureWorkspaceVersionFailsClosed(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(originalDirectory)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".gew"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gew", "state.json"), []byte(`{"version":999,"files":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := findWorkspace(); err == nil || !strings.Contains(err.Error(), "unsupported workspace state version 999") {
		t.Fatalf("future version error = %v", err)
	}
}

func TestExtractArchiveRejectsSymlink(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "root/link"}
	header.SetMode(os.ModeSymlink | 0o777)
	file, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("target"))
	writer.Close()
	err = extractArchive(buffer.Bytes(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestUnifiedDiffUsesCompactContextHunks(t *testing.T) {
	before := []byte("line01\nline02\nline03\nline04\nline05\nline06\nline07\nline08\nline09\nline10\nline11\nline12\nline13\nline14\nline15\n")
	after := bytes.Replace(before, []byte("line10\n"), []byte("changed10\n"), 1)
	var output bytes.Buffer
	printUnifiedDiff(&output, "file.txt", before, true, after, true)
	if strings.Contains(output.String(), " line01\n") || strings.Contains(output.String(), " line15\n") {
		t.Fatalf("compact diff included distant unchanged context:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "-line10\n") || !strings.Contains(output.String(), "+changed10\n") {
		t.Fatalf("compact diff omitted changed lines:\n%s", output.String())
	}
}

func TestThreeWayTextMergeCases(t *testing.T) {
	tests := []struct {
		name         string
		base         string
		ours         string
		theirs       string
		want         string
		wantConflict bool
	}{
		{name: "ours only", base: "a\nb\nc\n", ours: "A\nb\nc\n", theirs: "a\nb\nc\n", want: "A\nb\nc\n"},
		{name: "theirs only", base: "a\nb\nc\n", ours: "a\nb\nc\n", theirs: "a\nb\nC\n", want: "a\nb\nC\n"},
		{name: "independent lines", base: "a\nb\nc\n", ours: "A\nb\nc\n", theirs: "a\nb\nC\n", want: "A\nb\nC\n"},
		{name: "adjacent replacements", base: "a\nb\nc\n", ours: "A\nb\nc\n", theirs: "a\nB\nc\n", want: "A\nB\nc\n"},
		{name: "same edit", base: "a\nb\n", ours: "a\nB\n", theirs: "a\nB\n", want: "a\nB\n"},
		{name: "independent insertions", base: "a\nb\nc\n", ours: "a\nx\nb\nc\n", theirs: "a\nb\ny\nc\n", want: "a\nx\nb\ny\nc\n"},
		{name: "same point insertion conflict", base: "a\nb\n", ours: "a\nx\nb\n", theirs: "a\ny\nb\n", wantConflict: true},
		{name: "same line conflict", base: "a\nb\n", ours: "a\nours\n", theirs: "a\ntheirs\n", wantConflict: true},
		{name: "delete unchanged", base: "a\nb\n", ours: "a\n", theirs: "a\nb\n", want: "a\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			merged, conflict := mergeText([]byte(test.base), []byte(test.ours), []byte(test.theirs))
			if conflict != test.wantConflict {
				t.Fatalf("conflict=%v, want %v; merged:\n%s", conflict, test.wantConflict, merged)
			}
			if !test.wantConflict && string(merged) != test.want {
				t.Fatalf("merged=%q, want %q", merged, test.want)
			}
			if test.wantConflict && (!bytes.Contains(merged, []byte("<<<<<<< ours")) || !bytes.Contains(merged, []byte(">>>>>>> theirs"))) {
				t.Fatalf("missing conflict markers:\n%s", merged)
			}
		})
	}
}

func TestThreeWayFileMergeDeleteAddAndBinary(t *testing.T) {
	text := func(value string) optionalContent {
		return optionalContent{Exists: true, Content: []byte(value), Mode: 0o644}
	}
	missing := optionalContent{}
	if merged, conflict, _ := mergeFile(text("base\n"), missing, text("base\n")); conflict || merged.Exists {
		t.Fatalf("delete versus unchanged should delete: %#v conflict=%v", merged, conflict)
	}
	if merged, conflict, binary := mergeFile(text("base\n"), missing, text("changed\n")); !conflict || binary || !bytes.Contains(merged.Content, []byte("<<<<<<< ours")) {
		t.Fatalf("delete/modify should be a text conflict: %#v conflict=%v binary=%v", merged, conflict, binary)
	}
	if merged, conflict, _ := mergeFile(missing, text("same\n"), text("same\n")); conflict || string(merged.Content) != "same\n" {
		t.Fatalf("identical add/add should merge: %#v conflict=%v", merged, conflict)
	}
	if _, conflict, binary := mergeFile(optionalContent{Exists: true, Content: []byte{0, 1}}, optionalContent{Exists: true, Content: []byte{0, 2}}, optionalContent{Exists: true, Content: []byte{0, 3}}); !conflict || !binary {
		t.Fatalf("binary edits should conflict: conflict=%v binary=%v", conflict, binary)
	}
}

func TestMergeConflictAbortRestoresWorkspace(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, output := clonedTestWorkspace(t, fake)
	readme := filepath.Join(checkout, "README.md")
	if err := os.WriteFile(readme, []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, beforeState, err := findWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.files["README.md"] = []byte("theirs\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.pull(nil); err == nil || !strings.Contains(err.Error(), "conflicts require resolution") {
		t.Fatalf("expected merge conflict, got %v", err)
	}
	content, err := os.ReadFile(readme)
	if err != nil || !bytes.Contains(content, []byte("<<<<<<< ours")) {
		t.Fatalf("conflict markers missing: %q, %v", content, err)
	}
	output.Reset()
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "merge with remote") {
		t.Fatalf("status did not report merge state:\n%s", output.String())
	}
	if err := a.push(nil); err == nil || !strings.Contains(err.Error(), "merge is in progress") {
		t.Fatalf("push should be blocked during conflict, got %v", err)
	}
	if err := a.add([]string{"-A"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "must not commit markers"}); err == nil || !strings.Contains(err.Error(), "conflict markers remain") {
		t.Fatalf("direct commit should reject conflict markers, got %v", err)
	}
	if err := a.merge([]string{"--abort"}); err != nil {
		t.Fatalf("merge abort: %v", err)
	}
	content, err = os.ReadFile(readme)
	if err != nil || string(content) != "ours\n" {
		t.Fatalf("abort did not restore ours: %q, %v", content, err)
	}
	_, afterState, err := findWorkspace()
	if err != nil || afterState.BaseCommit != beforeState.BaseCommit {
		t.Fatalf("abort did not restore state: before=%s after=%s err=%v", beforeState.BaseCommit, afterState.BaseCommit, err)
	}
	if mergeState, err := loadMergeState(checkout); err != nil || mergeState != nil {
		t.Fatalf("merge state remains after abort: %#v, %v", mergeState, err)
	}
	if index, err := loadIndex(checkout); err != nil || len(index.Entries) != 0 {
		t.Fatalf("abort did not clear merge staging index: %#v, %v", index.Entries, err)
	}
}

func TestMergeConflictContinueCreatesPushableCommit(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	readme := filepath.Join(checkout, "README.md")
	if err := os.WriteFile(readme, []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.files["README.md"] = []byte("theirs\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.pull(nil); err == nil {
		t.Fatal("expected merge conflict")
	}
	if err := a.merge([]string{"--continue"}); err == nil || !strings.Contains(err.Error(), "conflict markers remain") {
		t.Fatalf("continue should reject unresolved markers, got %v", err)
	}
	if err := os.WriteFile(readme, []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.merge([]string{"--continue", "-m", "Resolve README merge"}); err != nil {
		t.Fatalf("continue resolved merge: %v", err)
	}
	_, state, err := findWorkspace()
	if err != nil || len(state.Queue) != 1 {
		t.Fatalf("resolved merge queue: %#v, %v", state.Queue, err)
	}
	if mergeState, err := loadMergeState(checkout); err != nil || mergeState != nil {
		t.Fatalf("merge state remains after continue: %#v, %v", mergeState, err)
	}
	if err := a.push(nil); err != nil {
		t.Fatalf("push resolved merge: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if string(fake.files["README.md"]) != "resolved\n" {
		t.Fatalf("resolved content not pushed: %q", fake.files["README.md"])
	}
}

func TestBinaryMergeConflictWritesSideFiles(t *testing.T) {
	fake := newFakeGitea()
	fake.files["asset.bin"] = []byte{0, 1}
	fake.snapshots[fake.commitSHA()] = cloneByteMap(fake.files)
	a, checkout, _ := clonedTestWorkspace(t, fake)
	asset := filepath.Join(checkout, "asset.bin")
	if err := os.WriteFile(asset, []byte{0, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.files["asset.bin"] = []byte{0, 3}
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.pull(nil); err == nil {
		t.Fatal("expected binary merge conflict")
	}
	for _, suffix := range []string{".base", ".ours", ".theirs"} {
		if _, err := os.Stat(filepath.Join(checkout, ".gew", "conflicts", "asset.bin") + suffix); err != nil {
			t.Fatalf("missing binary conflict side %s: %v", suffix, err)
		}
	}
	if err := a.merge([]string{"--abort"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(asset)
	if err != nil || !bytes.Equal(content, []byte{0, 2}) {
		t.Fatalf("binary abort did not restore ours: %v %v", content, err)
	}
}

func TestFastForwardOnlyRefusesMerge(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.files["remote.txt"] = []byte("remote\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.pull([]string{"--ff-only"}); err == nil || !strings.Contains(err.Error(), "fast-forward pull is not possible") {
		t.Fatalf("expected ff-only refusal, got %v", err)
	}
}

func TestQueuedCommitMergeCreatesReplacementCommit(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, output := clonedTestWorkspace(t, fake)
	if err := os.WriteFile(filepath.Join(checkout, "README.md"), []byte("local commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "Local work"}); err != nil {
		t.Fatal(err)
	}
	_, before, _ := findWorkspace()
	originalCommit := before.Queue[0]
	fake.mu.Lock()
	fake.files["remote.txt"] = []byte("remote\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.pull(nil); err != nil {
		t.Fatalf("merge queued commit: %v", err)
	}
	_, after, err := findWorkspace()
	if err != nil || len(after.Queue) != 1 || after.Queue[0] == originalCommit {
		t.Fatalf("replacement queue is wrong: before=%#v after=%#v err=%v", before.Queue, after.Queue, err)
	}
	original, err := loadLocalCommit(checkout, originalCommit)
	if err != nil || original.SupersededBy != after.Queue[0] {
		t.Fatalf("original commit not linked to replacement: %#v, %v", original, err)
	}
	output.Reset()
	if err := a.log([]string{"--oneline"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "superseded by") {
		t.Fatalf("log does not show superseded commit:\n%s", output.String())
	}
}

func TestQueuedConflictAbortRestoresOriginalQueue(t *testing.T) {
	fake := newFakeGitea()
	a, checkout, _ := clonedTestWorkspace(t, fake)
	readme := filepath.Join(checkout, "README.md")
	if err := os.WriteFile(readme, []byte("ours queued\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.add([]string{"README.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.commit([]string{"-m", "Queued work"}); err != nil {
		t.Fatal(err)
	}
	_, before, _ := findWorkspace()
	fake.mu.Lock()
	fake.files["README.md"] = []byte("theirs remote\n")
	fake.commit++
	fake.recordSnapshotLocked()
	fake.mu.Unlock()
	if err := a.pull(nil); err == nil {
		t.Fatal("expected queued merge conflict")
	}
	_, during, _ := findWorkspace()
	if len(during.Queue) != 0 {
		t.Fatalf("queue should be suspended during conflict: %#v", during.Queue)
	}
	if err := a.merge([]string{"--abort"}); err != nil {
		t.Fatal(err)
	}
	_, after, err := findWorkspace()
	if err != nil || len(after.Queue) != 1 || after.Queue[0] != before.Queue[0] {
		t.Fatalf("abort did not restore queue: before=%#v after=%#v err=%v", before.Queue, after.Queue, err)
	}
	content, err := os.ReadFile(readme)
	if err != nil || string(content) != "ours queued\n" {
		t.Fatalf("abort did not restore queued content: %q, %v", content, err)
	}
}

func TestThreeWayMergeAlgebraicProperties(t *testing.T) {
	random := rand.New(rand.NewSource(42))
	randomText := func() []byte {
		count := random.Intn(12)
		var builder strings.Builder
		for index := 0; index < count; index++ {
			fmt.Fprintf(&builder, "%c-%d\n", 'a'+rune(random.Intn(6)), random.Intn(20))
		}
		return []byte(builder.String())
	}
	for iteration := 0; iteration < 1000; iteration++ {
		base := randomText()
		variant := randomText()
		merged, conflict := mergeText(base, variant, base)
		if conflict || !bytes.Equal(merged, variant) {
			t.Fatalf("ours-only identity failed at iteration %d: conflict=%v got=%q want=%q", iteration, conflict, merged, variant)
		}
		merged, conflict = mergeText(base, base, variant)
		if conflict || !bytes.Equal(merged, variant) {
			t.Fatalf("theirs-only identity failed at iteration %d: conflict=%v got=%q want=%q", iteration, conflict, merged, variant)
		}
		merged, conflict = mergeText(base, variant, variant)
		if conflict || !bytes.Equal(merged, variant) {
			t.Fatalf("equal-sides identity failed at iteration %d: conflict=%v got=%q want=%q", iteration, conflict, merged, variant)
		}
	}
}

func TestConflictMarkerDetectionRequiresExactLines(t *testing.T) {
	if !containsConflictMarkerLine([]byte("before\n<<<<<<< ours\nafter\n")) {
		t.Fatal("exact conflict marker was not detected")
	}
	if containsConflictMarkerLine([]byte("Documentation mentions <<<<<<< ours and >>>>>>> theirs inline.\n")) {
		t.Fatal("inline documentation was incorrectly detected as a conflict marker")
	}
}

func FuzzThreeWayMergeIdentities(f *testing.F) {
	f.Add("a\nb\n", "a\nchanged\n")
	f.Add("", "new\n")
	f.Add("one\ntwo\nthree\n", "one\nthree\n")
	f.Fuzz(func(t *testing.T, baseText, variantText string) {
		if len(baseText) > 32_000 || len(variantText) > 32_000 {
			t.Skip()
		}
		base := []byte(baseText)
		variant := []byte(variantText)
		merged, conflict := mergeText(base, variant, base)
		if conflict || !bytes.Equal(merged, variant) {
			t.Fatalf("ours-only identity failed: conflict=%v got=%q want=%q", conflict, merged, variant)
		}
		merged, conflict = mergeText(base, base, variant)
		if conflict || !bytes.Equal(merged, variant) {
			t.Fatalf("theirs-only identity failed: conflict=%v got=%q want=%q", conflict, merged, variant)
		}
		merged, conflict = mergeText(base, variant, variant)
		if conflict || !bytes.Equal(merged, variant) {
			t.Fatalf("equal-sides identity failed: conflict=%v got=%q want=%q", conflict, merged, variant)
		}
	})
}

func clonedTestWorkspace(t *testing.T, fake *fakeGitea) (app, string, *bytes.Buffer) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	t.Setenv("GEW_SERVER", server.URL)
	t.Setenv("GEW_TOKEN", "test-token")
	t.Setenv("GEW_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	parent := t.TempDir()
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	a := app{out: output, errOut: output}
	if err := a.clone([]string{"acme/demo", "checkout"}); err != nil {
		t.Fatalf("clone test workspace: %v", err)
	}
	checkout := filepath.Join(parent, "checkout")
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	return a, checkout, output
}

func TestExtractArchiveRejectsTraversal(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("root/../../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("bad"))
	writer.Close()

	err = extractArchive(buffer.Bytes(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
}
