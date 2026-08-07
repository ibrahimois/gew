package bitbucket

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	forgecore "gew/internal/forge"
	"gew/internal/forge/forgetest"
)

func TestBitbucketForgeContract(t *testing.T) {
	forge, err := New(forgecore.Config{Provider: forgecore.ForgeBitbucket, URL: "https://bitbucket.org", Token: "token", AuthKind: forgecore.AuthBearer})
	if err != nil {
		t.Fatal(err)
	}
	forgetest.RunBaseContract(t, forge, forgecore.ForgeBitbucket, false, true, false)
}

func TestBitbucketRepositoryParsing(t *testing.T) {
	tests := []struct {
		value, workspace, repository string
		wantErr                      bool
	}{
		{value: "workspace/repo", workspace: "workspace", repository: "repo"},
		{value: "bitbucket.org/workspace/repo.git", workspace: "workspace", repository: "repo"},
		{value: "https://bitbucket.org/workspace/repo", workspace: "workspace", repository: "repo"},
		{value: "https://example.test/workspace/repo", wantErr: true},
		{value: "workspace/group/repo", wantErr: true},
	}
	for _, test := range tests {
		workspace, repository, err := parseBitbucketRepository(test.value)
		if (err != nil) != test.wantErr || workspace != test.workspace || repository != test.repository {
			t.Fatalf("parseBitbucketRepository(%q) = %q/%q, %v", test.value, workspace, repository, err)
		}
	}
}

func TestBitbucketResolveRepositoryWithBasicAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "user@example.com" || password != "secret" {
			t.Fatalf("basic auth = %q/%q, %v", username, password, ok)
		}
		json.NewEncoder(response).Encode(map[string]any{
			"uuid": "{repo-uuid}", "full_name": "canonical/renamed", "slug": "renamed",
			"workspace": map[string]string{"slug": "canonical"}, "mainbranch": map[string]string{"name": "main"},
		})
	}))
	defer server.Close()
	forge, err := newBitbucketForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeBitbucket, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthBasic, Username: "user@example.com"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ref, info, err := forge.ResolveRepository(context.Background(), "input/repo")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != "canonical" || ref.Name != "renamed" || ref.RemoteID != "{repo-uuid}" || ref.Canonical != "canonical/renamed" {
		t.Fatalf("ref = %#v", ref)
	}
	if info.Empty || info.DefaultBranch != "main" {
		t.Fatalf("info = %#v", info)
	}
}

func TestBitbucketTreeWalkPinsCommitAndFollowsPagination(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(request.URL.Path, "/src/commit/") && request.URL.Query().Get("page") == "2":
			json.NewEncoder(response).Encode(bitbucketTreePage{Values: []bitbucketTreeEntry{{Path: "second.txt", Type: "commit_file", Size: 2, Commit: struct {
				Hash string `json:"hash"`
			}{Hash: "commit"}}}})
		case strings.HasSuffix(request.URL.Path, "/src/commit/"):
			json.NewEncoder(response).Encode(bitbucketTreePage{
				Values: []bitbucketTreeEntry{
					{Path: "root.txt", Type: "commit_file", Size: 1, Commit: struct {
						Hash string `json:"hash"`
					}{Hash: "commit"}},
					{Path: "dir", Type: "commit_directory", Commit: struct {
						Hash string `json:"hash"`
					}{Hash: "commit"}},
				},
				Next: server.URL + request.URL.Path + "?page=2",
			})
		case strings.HasSuffix(request.URL.Path, "/src/commit/dir/"):
			entry := bitbucketTreeEntry{Path: "dir/nested.bin", Type: "commit_file", Size: 3, Attributes: []string{"binary"}}
			entry.Commit.Hash = "commit"
			json.NewEncoder(response).Encode(bitbucketTreePage{Values: []bitbucketTreeEntry{entry}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	forge, _ := newBitbucketForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeBitbucket, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthBearer}, server.URL)
	files, err := forge.Tree(context.Background(), forgecore.RepositoryRef{Forge: forgecore.ForgeBitbucket, Namespace: "ws", Name: "repo"}, "commit")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files["dir/nested.bin"].Size != 3 {
		t.Fatalf("files = %#v", files)
	}
	commit, filePath, err := parseBitbucketBlobID(files["dir/nested.bin"].BlobID)
	if err != nil || commit != "commit" || filePath != "dir/nested.bin" {
		t.Fatalf("blob identity = %q/%q, %v", commit, filePath, err)
	}
}

func TestBitbucketSnapshotSynthesizesSafeZip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/src/commit/") {
			entry := bitbucketTreeEntry{Path: "file.txt", Type: "commit_file", Size: 5}
			entry.Commit.Hash = "commit"
			json.NewEncoder(response).Encode(bitbucketTreePage{Values: []bitbucketTreeEntry{entry}})
			return
		}
		if strings.HasSuffix(request.URL.Path, "/src/commit/file.txt") {
			io.WriteString(response, "hello")
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	forge, _ := newBitbucketForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeBitbucket, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthBearer}, server.URL)
	data, err := forgecore.Snapshot(context.Background(), forge, forgecore.RepositoryRef{Forge: forgecore.ForgeBitbucket, Namespace: "ws", Name: "repo"}, "commit")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != 1 || !strings.HasSuffix(reader.File[0].Name, "/file.txt") {
		t.Fatalf("zip = %#v, %v", reader.File, err)
	}
	file, _ := reader.File[0].Open()
	content, _ := io.ReadAll(file)
	file.Close()
	if string(content) != "hello" {
		t.Fatalf("content = %q", content)
	}
}

func TestBitbucketMultipartCommitEncoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			json.NewEncoder(response).Encode(map[string]any{"target": map[string]string{"hash": "base"}})
			return
		}
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/src") {
			http.NotFound(response, request)
			return
		}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if request.FormValue("message") != "message" || request.FormValue("branch") != "feature" || request.FormValue("parents") != "base" {
			t.Fatalf("fields = %#v", request.MultipartForm.Value)
		}
		if values := request.MultipartForm.Value["files"]; len(values) != 1 || values[0] != "/old.txt" {
			t.Fatalf("delete fields = %#v", values)
		}
		files := request.MultipartForm.File["/dir/new.bin"]
		if len(files) != 1 {
			t.Fatalf("files = %#v", request.MultipartForm.File)
		}
		file, _ := files[0].Open()
		content, _ := io.ReadAll(file)
		file.Close()
		if !bytes.Equal(content, []byte{'a', 0, 'b'}) {
			t.Fatalf("content = %v", content)
		}
		json.NewEncoder(response).Encode(map[string]any{"hash": "next", "message": "message", "parents": []map[string]string{{"hash": "base"}}})
	}))
	defer server.Close()
	forge, _ := newBitbucketForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeBitbucket, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthBearer}, server.URL)
	result, err := forge.applyCommitUnchecked(context.Background(), forgecore.ApplyCommitRequest{
		Repository: forgecore.RepositoryRef{Forge: forgecore.ForgeBitbucket, Namespace: "ws", Name: "repo"}, Branch: "main", NewBranch: "feature", ExpectedHead: "base", Message: "message",
		Changes: []forgecore.RemoteChange{{Operation: "create", Path: "dir/new.bin", Content: []byte{'a', 0, 'b'}}, {Operation: "delete", Path: "old.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CommitID != "next" || result.ConditionalRef || len(result.ParentIDs) != 1 || result.ParentIDs[0] != "base" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBitbucketPushCapabilityRemainsGated(t *testing.T) {
	forge, _ := newBitbucketForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeBitbucket, URL: "https://bitbucket.org", Token: "secret", AuthKind: forgecore.AuthBearer}, "https://api.bitbucket.org/2.0")
	if forge.Capabilities().Push {
		t.Fatal("Bitbucket push must remain disabled before live concurrency verification")
	}
	_, err := forge.ApplyCommit(context.Background(), forgecore.ApplyCommitRequest{ExpectedHead: "base"})
	if !errors.Is(err, forgecore.ErrUnsupported) {
		t.Fatalf("ApplyCommit() error = %v", err)
	}
}
