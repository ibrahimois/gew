package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestGiteaForgeContract(t *testing.T) {
	forge, err := newGiteaForge(profile{Provider: ForgeGitea, URL: "https://gitea.example.test", Token: "token", AuthKind: AuthToken})
	if err != nil {
		t.Fatal(err)
	}
	runForgeBaseContract(t, forge, ForgeGitea, true, true, true)
}

func TestGiteaApplyCommitUsesAtomicContentsEndpoint(t *testing.T) {
	const secret = "sentinel-gitea-write-token"
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		if request.Header.Get("Authorization") != "token "+secret {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/contents"):
			var payload giteaChangeFilesRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Branch != "main" || payload.NewBranch != "feature" || payload.Message != "message" || len(payload.Files) != 3 {
				t.Fatalf("payload = %#v", payload)
			}
			if payload.Files[0].Operation != "create" || payload.Files[0].Content != base64.StdEncoding.EncodeToString([]byte{'a', 0, 'b'}) {
				t.Fatalf("binary create = %#v", payload.Files[0])
			}
			if payload.Files[1].Operation != "update" || payload.Files[1].SHA != "old-blob" || payload.Files[2].Operation != "delete" || payload.Files[2].SHA != "gone-blob" {
				t.Fatalf("update/delete = %#v", payload.Files)
			}
			json.NewEncoder(response).Encode(map[string]any{"ok": true})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/branches/feature"):
			json.NewEncoder(response).Encode(map[string]any{"name": "feature", "commit": map[string]string{"id": "next"}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/commits/next"):
			json.NewEncoder(response).Encode(map[string]any{"sha": "next", "parents": []map[string]string{{"sha": "base"}}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	forge, _ := newGiteaForge(profile{Provider: ForgeGitea, URL: server.URL, Token: secret, AuthKind: AuthToken})
	result, err := forge.ApplyCommit(context.Background(), ApplyCommitRequest{
		Repository: RepositoryRef{Forge: ForgeGitea, Namespace: "acme", Name: "demo"}, Branch: "main", NewBranch: "feature", ExpectedHead: "base", Message: "message",
		Changes: []RemoteChange{
			{Operation: "create", Path: "new.bin", Content: []byte{'a', 0, 'b'}},
			{Operation: "update", Path: "edit.txt", BlobID: "old-blob", Content: []byte("edit")},
			{Operation: "delete", Path: "old.txt", BlobID: "gone-blob"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CommitID != "next" || !reflect.DeepEqual(result.ParentIDs, []string{"base"}) || result.ConditionalRef {
		t.Fatalf("result = %#v", result)
	}
	want := []string{"POST /api/v1/repos/acme/demo/contents", "GET /api/v1/repos/acme/demo/branches/feature", "GET /api/v1/repos/acme/demo/git/commits/next"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %#v, want %#v", requests, want)
	}
}

func TestGiteaConflictRequiresConfirmedHeadChange(t *testing.T) {
	for _, test := range []struct {
		name, observed string
		wantStale      bool
	}{{name: "changed", observed: "advanced", wantStale: true}, {name: "unchanged", observed: "base"}} {
		t.Run(test.name, func(t *testing.T) {
			const secret = "sentinel-gitea-conflict-token"
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodPost {
					response.WriteHeader(http.StatusConflict)
					response.Write([]byte(`{"message":"conflict ` + secret + `"}`))
					return
				}
				json.NewEncoder(response).Encode(map[string]any{"commit": map[string]string{"id": test.observed}})
			}))
			defer server.Close()
			forge, _ := newGiteaForge(profile{Provider: ForgeGitea, URL: server.URL, Token: secret, AuthKind: AuthToken})
			_, err := forge.ApplyCommit(context.Background(), ApplyCommitRequest{Repository: RepositoryRef{Forge: ForgeGitea, Namespace: "a", Name: "b"}, Branch: "main", ExpectedHead: "base", Message: "message", Changes: []RemoteChange{{Operation: "create", Path: "file"}}})
			if errors.Is(err, ErrStaleHead) != test.wantStale {
				t.Fatalf("error = %v, wantStale %v", err, test.wantStale)
			}
			if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "returned 409") {
				t.Fatalf("sanitized provider error = %v", err)
			}
		})
	}
}

func TestGiteaResolveRepositoryRequestAndAuthentication(t *testing.T) {
	const secret = "sentinel-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.EscapedPath() != "/api/v1/repos/space%20owner/demo" {
			t.Fatalf("request = %s %s", request.Method, request.URL.EscapedPath())
		}
		if got := request.Header.Get("Authorization"); got != "token "+secret {
			t.Fatalf("Authorization = %q", got)
		}
		json.NewEncoder(response).Encode(giteaRepository{DefaultBranch: "trunk"})
	}))
	defer server.Close()

	forge, err := newGiteaForge(profile{Provider: ForgeGitea, URL: server.URL, Token: secret, AuthKind: AuthToken})
	if err != nil {
		t.Fatal(err)
	}
	ref, info, err := forge.ResolveRepository(context.Background(), "space owner/demo.git")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != "space owner" || ref.Name != "demo" || info.DefaultBranch != "trunk" {
		t.Fatalf("resolution = %#v %#v", ref, info)
	}
}

func TestGiteaErrorRedactsCredential(t *testing.T) {
	const secret = "sentinel-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Error(response, "server echoed "+secret, http.StatusUnauthorized)
	}))
	defer server.Close()
	forge, err := newGiteaForge(profile{Provider: ForgeGitea, URL: server.URL, Token: secret, AuthKind: AuthToken})
	if err != nil {
		t.Fatal(err)
	}
	err = forge.Probe(context.Background())
	if err == nil {
		t.Fatal("expected probe failure")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("credential was not redacted: %v", err)
	}
}

func TestGiteaCapabilities(t *testing.T) {
	forge, err := newGiteaForge(profile{Provider: ForgeGitea, URL: "https://example.test", Token: "token", AuthKind: AuthToken})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := forge.Capabilities()
	if !capabilities.BranchCreate || !capabilities.Push {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}
