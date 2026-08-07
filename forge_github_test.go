package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitHubForgeContract(t *testing.T) {
	forge, err := newGitHubForge(profile{Provider: ForgeGitHub, URL: "https://github.com", Token: "token", AuthKind: AuthBearer})
	if err != nil {
		t.Fatal(err)
	}
	runForgeBaseContract(t, forge, ForgeGitHub, true, true, true)
}

func TestGitHubRepositoryParsing(t *testing.T) {
	tests := []struct {
		name, server, value, owner, repository string
		wantErr                                bool
	}{
		{name: "shorthand", server: "https://github.com", value: "OpenAI/codex", owner: "OpenAI", repository: "codex"},
		{name: "host shorthand", server: "https://github.com", value: "github.com/OpenAI/codex.git", owner: "OpenAI", repository: "codex"},
		{name: "canonical URL", server: "https://github.com", value: "https://github.com/OpenAI/codex", owner: "OpenAI", repository: "codex"},
		{name: "enterprise", server: "https://git.example.test", value: "https://git.example.test/team/repo", owner: "team", repository: "repo"},
		{name: "extra component", server: "https://github.com", value: "https://github.com/a/b/issues", wantErr: true},
		{name: "wrong host", server: "https://github.com", value: "https://evil.test/a/b", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner, repository, err := parseGitHubRepository(test.server, test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseGitHubRepository() error = %v, wantErr %v", err, test.wantErr)
			}
			if owner != test.owner || repository != test.repository {
				t.Fatalf("parseGitHubRepository() = %q/%q", owner, repository)
			}
		})
	}
}

func TestGitHubResolveRepositoryHeadersAndIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v3/repos/input/repo" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" || request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("headers = %#v", request.Header)
		}
		if request.Header.Get("X-Github-Api-Version") != githubAPIVersion {
			t.Fatalf("api version = %q", request.Header.Get("X-Github-Api-Version"))
		}
		json.NewEncoder(response).Encode(map[string]any{
			"id": 42, "name": "Repo", "full_name": "Canonical/Repo", "default_branch": "trunk",
			"pushed_at": "2026-08-07T00:00:00Z", "owner": map[string]string{"login": "Canonical"},
		})
	}))
	defer server.Close()
	forge, err := newGitHubForge(profile{Provider: ForgeGitHub, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
	if err != nil {
		t.Fatal(err)
	}
	ref, info, err := forge.ResolveRepository(context.Background(), "input/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != "Canonical" || ref.Name != "Repo" || ref.RemoteID != "42" || ref.Canonical != "Canonical/Repo" {
		t.Fatalf("ref = %#v", ref)
	}
	if info.DefaultBranch != "trunk" || info.Empty {
		t.Fatalf("info = %#v", info)
	}
}

func TestGitHubApplyCommitUsesGitDatabaseAndNonForceRefUpdate(t *testing.T) {
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/git/ref/heads/main"):
			json.NewEncoder(response).Encode(map[string]any{"ref": "refs/heads/main", "object": map[string]string{"sha": "base"}})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/git/commits/base"):
			json.NewEncoder(response).Encode(map[string]any{"sha": "base", "tree": map[string]string{"sha": "base-tree"}})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/blobs"):
			var payload map[string]string
			json.NewDecoder(request.Body).Decode(&payload)
			if payload["encoding"] != "base64" || payload["content"] != base64.StdEncoding.EncodeToString([]byte("hello\x00")) {
				t.Fatalf("blob payload = %#v", payload)
			}
			json.NewEncoder(response).Encode(map[string]string{"sha": "blob-new"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/trees"):
			var payload struct {
				BaseTree string `json:"base_tree"`
				Tree     []struct {
					Path string  `json:"path"`
					Mode string  `json:"mode"`
					Type string  `json:"type"`
					SHA  *string `json:"sha"`
				} `json:"tree"`
			}
			json.NewDecoder(request.Body).Decode(&payload)
			if payload.BaseTree != "base-tree" || len(payload.Tree) != 2 || payload.Tree[0].SHA == nil || *payload.Tree[0].SHA != "blob-new" || payload.Tree[1].SHA != nil {
				t.Fatalf("tree payload = %#v", payload)
			}
			json.NewEncoder(response).Encode(map[string]string{"sha": "tree-new"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/commits"):
			var payload struct {
				Message string   `json:"message"`
				Tree    string   `json:"tree"`
				Parents []string `json:"parents"`
			}
			json.NewDecoder(request.Body).Decode(&payload)
			if payload.Message != "message" || payload.Tree != "tree-new" || !reflect.DeepEqual(payload.Parents, []string{"base"}) {
				t.Fatalf("commit payload = %#v", payload)
			}
			json.NewEncoder(response).Encode(map[string]string{"sha": "commit-new"})
		case request.Method == http.MethodPatch && strings.HasSuffix(request.URL.Path, "/git/refs/heads/main"):
			var payload struct {
				SHA   string `json:"sha"`
				Force bool   `json:"force"`
			}
			json.NewDecoder(request.Body).Decode(&payload)
			if payload.SHA != "commit-new" || payload.Force {
				t.Fatalf("ref payload = %#v", payload)
			}
			json.NewEncoder(response).Encode(map[string]any{"ref": "refs/heads/main", "object": map[string]string{"sha": "commit-new"}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	forge, err := newGitHubForge(profile{Provider: ForgeGitHub, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
	if err != nil {
		t.Fatal(err)
	}
	ref := RepositoryRef{Forge: ForgeGitHub, Server: server.URL, Namespace: "acme", Name: "demo"}
	result, err := forge.ApplyCommit(context.Background(), ApplyCommitRequest{
		Repository: ref, Branch: "main", ExpectedHead: "base", Message: "message",
		Changes: []RemoteChange{
			{Operation: "update", Path: "bin.dat", Content: []byte("hello\x00"), Mode: 0o755},
			{Operation: "delete", Path: "old.txt", BlobID: "old"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CommitID != "commit-new" || !result.ConditionalRef || !reflect.DeepEqual(result.ParentIDs, []string{"base"}) {
		t.Fatalf("result = %#v", result)
	}
	want := []string{
		"GET /api/v3/repos/acme/demo/git/ref/heads/main",
		"GET /api/v3/repos/acme/demo/git/commits/base",
		"POST /api/v3/repos/acme/demo/git/blobs",
		"POST /api/v3/repos/acme/demo/git/trees",
		"POST /api/v3/repos/acme/demo/git/commits",
		"PATCH /api/v3/repos/acme/demo/git/refs/heads/main",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}

func TestGitHubRefConflictRequiresConfirmedHeadChange(t *testing.T) {
	for _, test := range []struct {
		name, confirmed string
		wantStale       bool
	}{{name: "changed", confirmed: "advanced", wantStale: true}, {name: "unchanged policy failure", confirmed: "base"}} {
		t.Run(test.name, func(t *testing.T) {
			headReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				switch {
				case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/git/ref/heads/main"):
					headReads++
					head := "base"
					if headReads > 1 {
						head = test.confirmed
					}
					json.NewEncoder(response).Encode(map[string]any{"object": map[string]string{"sha": head}})
				case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/git/commits/base"):
					json.NewEncoder(response).Encode(map[string]any{"sha": "base", "tree": map[string]string{"sha": "tree-base"}})
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/blobs"):
					json.NewEncoder(response).Encode(map[string]string{"sha": "blob"})
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/trees"):
					json.NewEncoder(response).Encode(map[string]string{"sha": "tree"})
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/commits"):
					json.NewEncoder(response).Encode(map[string]string{"sha": "created"})
				case request.Method == http.MethodPatch:
					http.Error(response, `{"message":"ref update rejected"}`, http.StatusUnprocessableEntity)
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			forge, _ := newGitHubForge(profile{Provider: ForgeGitHub, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
			_, err := forge.ApplyCommit(context.Background(), ApplyCommitRequest{Repository: RepositoryRef{Forge: ForgeGitHub, Namespace: "a", Name: "b"}, Branch: "main", ExpectedHead: "base", Message: "message", Changes: []RemoteChange{{Operation: "create", Path: "file", Content: []byte("data")}}})
			if errors.Is(err, ErrStaleHead) != test.wantStale || !strings.Contains(err.Error(), "returned 422") {
				t.Fatalf("error = %v, wantStale %v", err, test.wantStale)
			}
			if headReads != 2 {
				t.Fatalf("head reads = %d, want 2", headReads)
			}
		})
	}
}

func TestGitHubBlobValidationFailureIsNeverStale(t *testing.T) {
	headReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/git/ref/heads/main"):
			headReads++
			json.NewEncoder(response).Encode(map[string]any{"object": map[string]string{"sha": "base"}})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/git/commits/base"):
			json.NewEncoder(response).Encode(map[string]any{"sha": "base", "tree": map[string]string{"sha": "tree-base"}})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/blobs"):
			http.Error(response, `{"message":"invalid blob"}`, http.StatusUnprocessableEntity)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	forge, _ := newGitHubForge(profile{Provider: ForgeGitHub, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
	_, err := forge.ApplyCommit(context.Background(), ApplyCommitRequest{Repository: RepositoryRef{Forge: ForgeGitHub, Namespace: "a", Name: "b"}, Branch: "main", ExpectedHead: "base", Message: "message", Changes: []RemoteChange{{Operation: "create", Path: "file", Content: []byte("data")}}})
	if err == nil || errors.Is(err, ErrStaleHead) || headReads != 1 {
		t.Fatalf("blob error = %v, headReads=%d", err, headReads)
	}
}

func TestGitHubEmptyRepositoryPushRefusesBeforeMutation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		http.Error(response, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	forge, err := newGitHubForge(profile{Provider: ForgeGitHub, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
	if err != nil {
		t.Fatal(err)
	}
	_, err = forge.ApplyCommit(context.Background(), ApplyCommitRequest{
		Repository: RepositoryRef{Forge: ForgeGitHub, Namespace: "acme", Name: "empty"},
		Branch:     "main", Message: "initial", Changes: []RemoteChange{{Operation: "create", Path: "one.txt", Content: []byte("one")}},
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("empty push made %d HTTP requests", requests)
	}
}

func TestGitHubEmptyRepositoryCLIPreservesQueue(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			mutations++
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	t.Setenv("GEW_SERVER", server.URL)
	t.Setenv("GEW_PROVIDER", string(ForgeGitHub))
	t.Setenv("GEW_AUTH_KIND", string(AuthBearer))
	t.Setenv("GEW_TOKEN", "secret")
	t.Setenv("GEW_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	root := t.TempDir()
	content := []byte("initial\n")
	hash := sha256.Sum256(content)
	objectID := hex.EncodeToString(hash[:])
	if err := storeObject(root, objectID, content); err != nil {
		t.Fatal(err)
	}
	commit := localCommit{ID: "local-commit", Message: "initial", Changes: []commitChange{{Kind: "created", Path: "README.md", Object: objectID, Mode: 0o644}}}
	if err := saveLocalCommit(root, commit); err != nil {
		t.Fatal(err)
	}
	state := workspaceState{
		Version: stateVersion, Provider: ForgeGitHub,
		Remote: RepositoryRef{Forge: ForgeGitHub, Server: server.URL, Namespace: "acme", Name: "empty", Canonical: "acme/empty"},
		Branch: "main", Files: map[string]fileState{}, Queue: []string{commit.ID}, History: []string{commit.ID},
	}
	if err := saveState(root, state); err != nil {
		t.Fatal(err)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	err = (app{out: io.Discard, errOut: io.Discard}).push(nil)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("push error = %v", err)
	}
	_, after, err := findWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Queue, []string{commit.ID}) {
		t.Fatalf("queue = %#v", after.Queue)
	}
	if mutations != 0 {
		t.Fatalf("empty push made %d mutating requests", mutations)
	}
}

func TestGitHubTruncatedTreeFallsBackToSubtreeWalk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/git/commits/commit"):
			json.NewEncoder(response).Encode(map[string]any{"sha": "commit", "tree": map[string]string{"sha": "root"}})
		case strings.HasSuffix(request.URL.Path, "/git/trees/root") && request.URL.Query().Has("recursive"):
			json.NewEncoder(response).Encode(githubTreeResponse{SHA: "root", Truncated: true})
		case strings.HasSuffix(request.URL.Path, "/git/trees/root"):
			json.NewEncoder(response).Encode(githubTreeResponse{SHA: "root", Tree: []githubTreeEntry{
				{Path: "root.txt", Type: "blob", SHA: "root-blob", Mode: "100644"},
				{Path: "dir", Type: "tree", SHA: "subtree", Mode: "040000"},
			}})
		case strings.HasSuffix(request.URL.Path, "/git/trees/subtree"):
			json.NewEncoder(response).Encode(githubTreeResponse{SHA: "subtree", Tree: []githubTreeEntry{{Path: "nested.bin", Type: "blob", SHA: "nested-blob", Mode: "100644", Size: 3}}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	forge, _ := newGitHubForge(profile{Provider: ForgeGitHub, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
	files, err := forge.Tree(context.Background(), RepositoryRef{Forge: ForgeGitHub, Namespace: "a", Name: "b"}, "commit")
	if err != nil {
		t.Fatal(err)
	}
	if files["root.txt"].BlobID != "root-blob" || files["dir/nested.bin"].BlobID != "nested-blob" || len(files) != 2 {
		t.Fatalf("files = %#v", files)
	}
}

func TestGitHubArchiveRedirectDoesNotLeakAuthorization(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Fatalf("redirect leaked authorization: %q", authorization)
		}
		io.WriteString(response, "zip")
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("source authorization = %q", request.Header.Get("Authorization"))
		}
		http.Redirect(response, request, destination.URL+"/archive.zip", http.StatusFound)
	}))
	defer source.Close()
	forge, _ := newGitHubForge(profile{Provider: ForgeGitHub, URL: source.URL, Token: "secret", AuthKind: AuthBearer})
	data, err := forge.Snapshot(context.Background(), RepositoryRef{Forge: ForgeGitHub, Namespace: "a", Name: "b"}, "commit")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "zip" {
		t.Fatalf("snapshot = %q", data)
	}
}
