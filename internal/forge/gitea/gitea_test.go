package gitea

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	forgecore "gew/internal/forge"
	"gew/internal/forge/forgetest"
)

func TestGiteaForgeContract(t *testing.T) {
	forge, err := New(forgecore.Config{Provider: forgecore.ForgeGitea, URL: "https://gitea.example.test", Token: "token", AuthKind: forgecore.AuthToken})
	if err != nil {
		t.Fatal(err)
	}
	forgetest.RunBaseContract(t, forge, forgecore.ForgeGitea, true, true, true)
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
			json.NewEncoder(response).Encode(map[string]any{"commit": map[string]any{"sha": "next", "message": "message", "parents": []map[string]string{{"sha": "base"}}}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	forge, _ := New(forgecore.Config{Provider: forgecore.ForgeGitea, URL: server.URL, Token: secret, AuthKind: forgecore.AuthToken})
	result, err := forge.ApplyCommit(context.Background(), forgecore.ApplyCommitRequest{
		Repository: forgecore.RepositoryRef{Forge: forgecore.ForgeGitea, Namespace: "acme", Name: "demo"}, Branch: "main", NewBranch: "feature", ExpectedHead: "base", Message: "message",
		Changes: []forgecore.RemoteChange{
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
	want := []string{"POST /api/v1/repos/acme/demo/contents"}
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
			forge, _ := New(forgecore.Config{Provider: forgecore.ForgeGitea, URL: server.URL, Token: secret, AuthKind: forgecore.AuthToken})
			_, err := forge.ApplyCommit(context.Background(), forgecore.ApplyCommitRequest{Repository: forgecore.RepositoryRef{Forge: forgecore.ForgeGitea, Namespace: "a", Name: "b"}, Branch: "main", ExpectedHead: "base", Message: "message", Changes: []forgecore.RemoteChange{{Operation: "create", Path: "file"}}})
			if errors.Is(err, forgecore.ErrStaleHead) != test.wantStale {
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

	forge, err := New(forgecore.Config{Provider: forgecore.ForgeGitea, URL: server.URL, Token: secret, AuthKind: forgecore.AuthToken})
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
	forge, err := New(forgecore.Config{Provider: forgecore.ForgeGitea, URL: server.URL, Token: secret, AuthKind: forgecore.AuthToken})
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
	forge, err := New(forgecore.Config{Provider: forgecore.ForgeGitea, URL: "https://example.test", Token: "token", AuthKind: forgecore.AuthToken})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := forge.Capabilities()
	if !capabilities.BranchCreate || !capabilities.Push {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestGiteaReleasePublisher(t *testing.T) {
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/releases/tags/v1.2.3"):
			json.NewEncoder(response).Encode(giteaRelease{ID: 7, TagName: "v1.2.3", TargetCommitish: "exact", Name: "title", Body: "notes"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/releases"):
			var payload map[string]any
			json.NewDecoder(request.Body).Decode(&payload)
			if payload["target_commitish"] != "exact" || payload["tag_name"] != "v1.2.3" {
				t.Fatalf("release payload = %#v", payload)
			}
			json.NewEncoder(response).Encode(giteaRelease{ID: 7, TagName: "v1.2.3", TargetCommitish: "exact", Name: "title", Body: "notes"})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/releases/7/assets"):
			json.NewEncoder(response).Encode([]giteaReleaseAsset{{ID: 9, Name: "gew.tar.gz", Size: 4}})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/releases/7/assets"):
			if request.URL.Query().Get("name") != "gew.tar.gz" {
				t.Fatalf("asset query = %q", request.URL.RawQuery)
			}
			file, header, err := request.FormFile("attachment")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			uploaded, _ = io.ReadAll(file)
			if header.Filename != "gew.tar.gz" {
				t.Fatalf("filename = %q", header.Filename)
			}
			json.NewEncoder(response).Encode(giteaReleaseAsset{ID: 9, Name: "gew.tar.gz", Size: 4})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/releases/assets/9"):
			response.Write([]byte("data"))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	remote, _ := New(forgecore.Config{URL: server.URL, Token: "secret", AuthKind: forgecore.AuthToken})
	ref := forgecore.RepositoryRef{Namespace: "acme", Name: "demo"}
	release, err := remote.FindReleaseByTag(context.Background(), ref, "v1.2.3")
	if err != nil || release.ID != "7" || release.TargetCommit != "exact" {
		t.Fatalf("find release = %#v, %v", release, err)
	}
	release, err = remote.CreateRelease(context.Background(), forgecore.CreateReleaseRequest{Repository: ref, TagName: "v1.2.3", TargetCommit: "exact", Title: "title", Notes: "notes", Latest: true})
	if err != nil || release.ID != "7" {
		t.Fatalf("create release = %#v, %v", release, err)
	}
	assets, err := remote.ListReleaseAssets(context.Background(), ref, release.ID)
	if err != nil || len(assets) != 1 || assets[0].ID != "9" {
		t.Fatalf("assets = %#v, %v", assets, err)
	}
	asset, err := remote.UploadReleaseAsset(context.Background(), ref, release.ID, "gew.tar.gz", 4, strings.NewReader("data"))
	if err != nil || asset.ID != "9" || string(uploaded) != "data" {
		t.Fatalf("upload = %#v, %q, %v", asset, uploaded, err)
	}
	reader, err := remote.DownloadReleaseAsset(context.Background(), ref, asset)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(reader)
	reader.Close()
	if string(data) != "data" {
		t.Fatalf("download = %q", data)
	}
}
