package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestGitLabForgeContract(t *testing.T) {
	forge, err := newGitLabForge(profile{Provider: ForgeGitLab, URL: "https://gitlab.com", Token: "token", AuthKind: AuthBearer})
	if err != nil {
		t.Fatal(err)
	}
	runForgeBaseContract(t, forge, ForgeGitLab, true, true, false)
}

func TestGitLabRepositoryParsing(t *testing.T) {
	tests := []struct {
		name, server, value, want string
		wantErr                   bool
	}{
		{name: "nested shorthand", server: "https://gitlab.com", value: "group/subgroup/repo", want: "group/subgroup/repo"},
		{name: "canonical", server: "https://gitlab.com", value: "https://gitlab.com/group/sub/repo.git", want: "group/sub/repo"},
		{name: "self managed", server: "https://git.example.test", value: "https://git.example.test/team/repo", want: "team/repo"},
		{name: "numeric project ID", server: "https://gitlab.com", value: "12345", want: "12345"},
		{name: "missing namespace", server: "https://gitlab.com", value: "repo", wantErr: true},
		{name: "wrong host", server: "https://gitlab.com", value: "https://evil.test/a/b", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseGitLabRepository(test.server, test.value)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("parseGitLabRepository() = %q, %v", got, err)
			}
		})
	}
}

func TestGitLabResolveNestedProjectAndPrivateToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v4/projects/group%2Fsub%2Frepo" && request.URL.Path != "/api/v4/projects/group/sub/repo" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Private-Token") != "secret" || request.Header.Get("Authorization") != "" {
			t.Fatalf("auth headers = %#v", request.Header)
		}
		json.NewEncoder(response).Encode(map[string]any{
			"id": 123, "name": "Repo", "path": "repo", "path_with_namespace": "Canonical/Sub/repo",
			"default_branch": "main", "empty_repo": false, "namespace": map[string]string{"full_path": "Canonical/Sub"},
		})
	}))
	defer server.Close()
	forge, err := newGitLabForge(profile{Provider: ForgeGitLab, URL: server.URL, Token: "secret", AuthKind: AuthPrivate})
	if err != nil {
		t.Fatal(err)
	}
	ref, info, err := forge.ResolveRepository(context.Background(), "group/sub/repo")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != "Canonical/Sub" || ref.Name != "repo" || ref.RemoteID != "123" || ref.Canonical != "Canonical/Sub/repo" {
		t.Fatalf("ref = %#v", ref)
	}
	if info.DefaultBranch != "main" || info.Empty {
		t.Fatalf("info = %#v", info)
	}
}

func TestGitLabApplyCommitBuildsLockedBatch(t *testing.T) {
	var posted bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/repository/branches/main"):
			json.NewEncoder(response).Encode(map[string]any{"commit": map[string]string{"id": "base"}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/repository/files/"):
			if request.URL.Query().Get("ref") != "base" {
				t.Fatalf("metadata ref = %q", request.URL.Query().Get("ref"))
			}
			json.NewEncoder(response).Encode(gitLabFileMetadata{BlobID: "blob", LastCommitID: "file-lock"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/repository/commits"):
			posted = true
			var payload struct {
				Branch        string `json:"branch"`
				CommitMessage string `json:"commit_message"`
				StartSHA      string `json:"start_sha"`
				Actions       []struct {
					Action       string `json:"action"`
					FilePath     string `json:"file_path"`
					Content      string `json:"content"`
					Encoding     string `json:"encoding"`
					LastCommitID string `json:"last_commit_id"`
				} `json:"actions"`
			}
			json.NewDecoder(request.Body).Decode(&payload)
			if payload.Branch != "feature" || payload.StartSHA != "base" || payload.CommitMessage != "message" || len(payload.Actions) != 3 {
				t.Fatalf("payload = %#v", payload)
			}
			if payload.Actions[0].Content != base64.StdEncoding.EncodeToString([]byte("new")) || payload.Actions[0].Encoding != "base64" {
				t.Fatalf("create action = %#v", payload.Actions[0])
			}
			if payload.Actions[1].LastCommitID != "file-lock" || payload.Actions[2].LastCommitID != "file-lock" {
				t.Fatalf("locks = %#v", payload.Actions)
			}
			json.NewEncoder(response).Encode(gitLabCommit{ID: "next", Message: "message", ParentIDs: []string{"base"}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	forge, _ := newGitLabForge(profile{Provider: ForgeGitLab, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
	result, err := forge.applyCommitUnchecked(context.Background(), ApplyCommitRequest{
		Repository: RepositoryRef{Forge: ForgeGitLab, RemoteID: "123"}, Branch: "main", NewBranch: "feature", ExpectedHead: "base", Message: "message",
		Changes: []RemoteChange{
			{Operation: "create", Path: "new.bin", Content: []byte("new")},
			{Operation: "update", Path: "edit.txt", Content: []byte("edit")},
			{Operation: "delete", Path: "old.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !posted || result.CommitID != "next" || result.ConditionalRef || !reflect.DeepEqual(result.ParentIDs, []string{"base"}) {
		t.Fatalf("result = %#v posted=%v", result, posted)
	}
}

func TestGitLabPushCapabilityRemainsGated(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		http.Error(response, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()
	forge, _ := newGitLabForge(profile{Provider: ForgeGitLab, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
	if forge.Capabilities().Push {
		t.Fatal("GitLab push must remain disabled before live concurrency verification")
	}
	_, err := forge.ApplyCommit(context.Background(), ApplyCommitRequest{ExpectedHead: "base"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("ApplyCommit() error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("gated push made %d requests", requests)
	}
}

func TestGitLabTreePagination(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		pages++
		entries := make([]gitLabTreeEntry, 0)
		if request.URL.Query().Get("page") == "1" {
			for index := 0; index < 100; index++ {
				entries = append(entries, gitLabTreeEntry{ID: fmt.Sprintf("blob-%d", index), Path: fmt.Sprintf("dir/file-%d", index), Type: "blob", Mode: "100644"})
			}
		} else {
			entries = append(entries, gitLabTreeEntry{ID: "last", Path: "last.txt", Type: "blob", Mode: "100644"})
		}
		json.NewEncoder(response).Encode(entries)
	}))
	defer server.Close()
	forge, _ := newGitLabForge(profile{Provider: ForgeGitLab, URL: server.URL, Token: "secret", AuthKind: AuthBearer})
	files, err := forge.Tree(context.Background(), RepositoryRef{Forge: ForgeGitLab, RemoteID: "1"}, "commit")
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || files["last.txt"].BlobID != "last" {
		t.Fatalf("pages=%d files=%d", pages, len(files))
	}
}
