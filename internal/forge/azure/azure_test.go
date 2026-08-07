package azure

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	forgecore "gew/internal/forge"
	"gew/internal/forge/forgetest"
)

func TestAzureForgeContract(t *testing.T) {
	forge, err := New(forgecore.Config{Provider: forgecore.ForgeAzure, URL: "https://dev.azure.com/example", Token: "token", AuthKind: forgecore.AuthBearer})
	if err != nil {
		t.Fatal(err)
	}
	forgetest.RunBaseContract(t, forge, forgecore.ForgeAzure, false, true, true)
}

func TestAzureServerAndRepositoryParsing(t *testing.T) {
	server, organization, err := normalizeAzureServer("https://example.visualstudio.com")
	if err != nil || server != "https://dev.azure.com/example" || organization != "example" {
		t.Fatalf("legacy server = %q, %q, %v", server, organization, err)
	}
	tests := []struct {
		value, profile, organization, project, repository string
		wantErr                                           bool
	}{
		{value: "Project/Repo", profile: "org", organization: "org", project: "Project", repository: "Repo"},
		{value: "https://dev.azure.com/org/My%20Project/_git/R%C3%A9po.git", profile: "org", organization: "org", project: "My Project", repository: "Répo"},
		{value: "https://org.visualstudio.com/Project/_git/Repo", profile: "org", organization: "org", project: "Project", repository: "Repo"},
		{value: "https://example.test/Project/_git/Repo", profile: "org", wantErr: true},
		{value: "org/Project/Repo", profile: "org", wantErr: true},
	}
	for _, test := range tests {
		organization, project, repository, err := parseAzureRepository(test.value, test.profile)
		if (err != nil) != test.wantErr || organization != test.organization || project != test.project || repository != test.repository {
			t.Fatalf("parseAzureRepository(%q) = %q/%q/%q, %v", test.value, organization, project, repository, err)
		}
	}
	if _, _, err := normalizeAzureServer("https://azure.example.test/org"); err == nil {
		t.Fatal("Azure DevOps Server host was accepted")
	}
}

func TestAzureResolveRepositoryUsesPATAndStableIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "" || password != "secret" {
			t.Fatalf("basic auth = %q/%q, %v", username, password, ok)
		}
		if request.URL.Query().Get("api-version") != azureAPIVersion {
			t.Fatalf("api-version = %q", request.URL.Query().Get("api-version"))
		}
		if request.URL.EscapedPath() != "/Input%20Project/_apis/git/repositories/input" {
			t.Fatalf("path = %q", request.URL.EscapedPath())
		}
		json.NewEncoder(response).Encode(map[string]any{
			"id": "repo-id", "name": "renamed", "defaultBranch": "refs/heads/main",
			"project": map[string]string{"id": "project-id", "name": "Renamed Project"},
		})
	}))
	defer server.Close()
	forge, err := newAzureForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeAzure, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthPAT}, server.URL, "org")
	if err != nil {
		t.Fatal(err)
	}
	ref, info, err := forge.ResolveRepository(context.Background(), "Input Project/input")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != "org" || ref.Project != "project-id" || ref.RemoteID != "repo-id" || ref.Canonical != "org/Renamed Project/renamed" {
		t.Fatalf("ref = %#v", ref)
	}
	if info.Empty || info.DefaultBranch != "main" {
		t.Fatalf("info = %#v", info)
	}
}

func TestAzureHeadTreeBlobAndSnapshotPinCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("api-version") != azureAPIVersion {
			t.Fatalf("missing api version: %s", request.URL.RawQuery)
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/refs"):
			if query.Get("filter") != "refs/heads/main" {
				t.Fatalf("filter = %q", query.Get("filter"))
			}
			json.NewEncoder(response).Encode(map[string]any{"value": []map[string]string{{"name": "refs/heads/main", "objectId": "commit"}}})
		case strings.HasSuffix(request.URL.Path, "/items") && query.Get("recursionLevel") == "Full":
			if query.Get("versionDescriptor.version") != "commit" || query.Get("versionDescriptor.versionType") != "commit" {
				t.Fatalf("version query = %s", request.URL.RawQuery)
			}
			json.NewEncoder(response).Encode(map[string]any{"value": []map[string]any{
				{"objectId": "tree", "gitObjectType": "tree", "commitId": "commit", "path": "/", "isFolder": true},
				{"objectId": "blob", "gitObjectType": "blob", "commitId": "commit", "path": "/dir/file.bin", "contentLength": 3},
			}})
		case strings.HasSuffix(request.URL.Path, "/items"):
			if query.Get("path") != "/dir/file.bin" || query.Get("versionDescriptor.version") != "commit" || query.Get("$format") != "octetStream" {
				t.Fatalf("blob query = %s", request.URL.RawQuery)
			}
			response.Write([]byte{'a', 0, 'b'})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	forge, _ := newAzureForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeAzure, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthBearer}, server.URL, "org")
	ref := forgecore.RepositoryRef{Forge: forgecore.ForgeAzure, Project: "project-id", RemoteID: "repo-id", Name: "repo"}
	head, err := forge.Head(context.Background(), ref, "refs/heads/main")
	if err != nil || head != "commit" {
		t.Fatalf("head = %q, %v", head, err)
	}
	files, err := forge.Tree(context.Background(), ref, head)
	if err != nil || len(files) != 1 || files["dir/file.bin"].Size != 3 {
		t.Fatalf("files = %#v, %v", files, err)
	}
	data, err := forge.Blob(context.Background(), ref, files["dir/file.bin"])
	if err != nil || !bytes.Equal(data, []byte{'a', 0, 'b'}) {
		t.Fatalf("blob = %v, %v", data, err)
	}
	archive, err := forgecore.Snapshot(context.Background(), forge, ref, head)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil || len(reader.File) != 1 || reader.File[0].Name != "repo-commit/dir/file.bin" {
		t.Fatalf("zip = %#v, %v", reader.File, err)
	}
	entry, _ := reader.File[0].Open()
	content, _ := io.ReadAll(entry)
	entry.Close()
	if !bytes.Equal(content, data) {
		t.Fatalf("zip content = %v", content)
	}
}

func TestAzureApplyCommitExactConditionalJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/pushes") {
			http.NotFound(response, request)
			return
		}
		if request.URL.Query().Get("api-version") != azureAPIVersion {
			t.Fatalf("api-version = %q", request.URL.Query().Get("api-version"))
		}
		var payload struct {
			RefUpdates []struct {
				Name, OldObjectID string
			} `json:"refUpdates"`
			Commits []struct {
				Comment string `json:"comment"`
				Changes []struct {
					ChangeType string                `json:"changeType"`
					Item       struct{ Path string } `json:"item"`
					NewContent *struct {
						Content, ContentType string
					} `json:"newContent"`
				} `json:"changes"`
			} `json:"commits"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.RefUpdates) != 1 || payload.RefUpdates[0].Name != "refs/heads/main" || payload.RefUpdates[0].OldObjectID != "base" {
			t.Fatalf("ref updates = %#v", payload.RefUpdates)
		}
		if len(payload.Commits) != 1 || payload.Commits[0].Comment != "message" || len(payload.Commits[0].Changes) != 3 {
			t.Fatalf("commits = %#v", payload.Commits)
		}
		changes := payload.Commits[0].Changes
		if changes[0].ChangeType != "add" || changes[0].Item.Path != "/new.bin" || changes[0].NewContent == nil || changes[0].NewContent.ContentType != "base64encoded" {
			t.Fatalf("add = %#v", changes[0])
		}
		decoded, _ := base64.StdEncoding.DecodeString(changes[0].NewContent.Content)
		if !bytes.Equal(decoded, []byte{'a', 0, 'b'}) {
			t.Fatalf("decoded = %v", decoded)
		}
		if changes[1].ChangeType != "edit" || changes[2].ChangeType != "delete" || changes[2].NewContent != nil {
			t.Fatalf("changes = %#v", changes)
		}
		json.NewEncoder(response).Encode(map[string]any{
			"commits":    []map[string]any{{"commitId": "next", "parents": []string{"base"}}},
			"refUpdates": []map[string]string{{"name": "refs/heads/main", "oldObjectId": "base", "newObjectId": "next"}},
		})
	}))
	defer server.Close()
	forge, _ := newAzureForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeAzure, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthBearer}, server.URL, "org")
	result, err := forge.ApplyCommit(context.Background(), forgecore.ApplyCommitRequest{
		Repository: forgecore.RepositoryRef{Forge: forgecore.ForgeAzure, Project: "project-id", RemoteID: "repo-id"}, Branch: "main", ExpectedHead: "base", Message: "message",
		Changes: []forgecore.RemoteChange{
			{Operation: "create", Path: "new.bin", Content: []byte{'a', 0, 'b'}},
			{Operation: "update", Path: "edit.txt", Content: []byte("updated")},
			{Operation: "delete", Path: "old.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CommitID != "next" || !result.ConditionalRef || len(result.ParentIDs) != 1 || result.ParentIDs[0] != "base" {
		t.Fatalf("result = %#v", result)
	}
}

func TestAzureInitialAndNewBranchOldObjectIDs(t *testing.T) {
	oldObjectIDs := make(chan string, 2)
	names := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload struct {
			RefUpdates []struct {
				Name, OldObjectID string
			} `json:"refUpdates"`
		}
		json.NewDecoder(request.Body).Decode(&payload)
		old := payload.RefUpdates[0].OldObjectID
		name := payload.RefUpdates[0].Name
		oldObjectIDs <- old
		names <- name
		parent := []string(nil)
		responseOld := old
		if name == "refs/heads/feature" {
			parent = []string{"base"}
			responseOld = azureZeroOID
		}
		json.NewEncoder(response).Encode(map[string]any{
			"commits":    []map[string]any{{"commitId": "next", "parents": parent}},
			"refUpdates": []map[string]string{{"name": name, "oldObjectId": responseOld, "newObjectId": "next"}},
		})
	}))
	defer server.Close()
	forge, _ := newAzureForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeAzure, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthBearer}, server.URL, "org")
	ref := forgecore.RepositoryRef{Forge: forgecore.ForgeAzure, Project: "project-id", RemoteID: "repo-id"}
	if _, err := forge.ApplyCommit(context.Background(), forgecore.ApplyCommitRequest{Repository: ref, Branch: "main", Message: "initial", Changes: []forgecore.RemoteChange{{Operation: "create", Path: "README.md"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := forge.ApplyCommit(context.Background(), forgecore.ApplyCommitRequest{Repository: ref, Branch: "main", NewBranch: "feature", ExpectedHead: "base", Message: "branch", Changes: []forgecore.RemoteChange{{Operation: "create", Path: "feature.txt"}}}); err != nil {
		t.Fatal(err)
	}
	if old := <-oldObjectIDs; old != azureZeroOID {
		t.Fatalf("initial oldObjectId = %q", old)
	}
	if name := <-names; name != "refs/heads/main" {
		t.Fatalf("initial ref = %q", name)
	}
	if old := <-oldObjectIDs; old != "base" {
		t.Fatalf("new branch oldObjectId = %q", old)
	}
	if name := <-names; name != "refs/heads/feature" {
		t.Fatalf("new branch ref = %q", name)
	}
}

func TestAzureConflictAndValidationPreserveConditionalSafety(t *testing.T) {
	currentHead := "advanced"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/refs") {
			json.NewEncoder(response).Encode(map[string]any{"value": []map[string]string{{"name": "refs/heads/main", "objectId": currentHead}}})
			return
		}
		response.WriteHeader(http.StatusConflict)
		io.WriteString(response, `{"message":"oldObjectId does not match"}`)
	}))
	defer server.Close()
	forge, _ := newAzureForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeAzure, URL: server.URL, Token: "secret", AuthKind: forgecore.AuthBearer}, server.URL, "org")
	writer, err := forgecore.Writer(forge, false)
	if err != nil {
		t.Fatal(err)
	}
	request := forgecore.ApplyCommitRequest{
		Repository: forgecore.RepositoryRef{Forge: forgecore.ForgeAzure, Project: "project-id", RemoteID: "repo-id"},
		Branch:     "main", ExpectedHead: "stale", Message: "message", Changes: []forgecore.RemoteChange{{Operation: "update", Path: "file.txt"}},
	}
	_, err = writer.ApplyCommit(context.Background(), request)
	if !errors.Is(err, forgecore.ErrStaleHead) {
		t.Fatalf("conflict error = %v", err)
	}
	currentHead = "stale"
	_, err = writer.ApplyCommit(context.Background(), request)
	if err == nil || errors.Is(err, forgecore.ErrStaleHead) || !strings.Contains(err.Error(), "returned 409") {
		t.Fatalf("unchanged-head policy error = %v", err)
	}
	request.Changes = append(request.Changes, forgecore.RemoteChange{Operation: "delete", Path: "file.txt"})
	if _, err := writer.ApplyCommit(context.Background(), request); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
	request.Changes = []forgecore.RemoteChange{{Operation: "update", Path: "../escape"}}
	if _, err := writer.ApplyCommit(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("path error = %v", err)
	}
}

func TestAzureRedirectDoesNotLeakBearer(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		leaked = request.Header.Get("Authorization") != ""
		http.Error(response, "stop", http.StatusUnauthorized)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL, http.StatusFound)
	}))
	defer source.Close()
	forge, _ := newAzureForgeWithAPI(forgecore.Config{Provider: forgecore.ForgeAzure, URL: source.URL, Token: "secret", AuthKind: forgecore.AuthBearer}, source.URL, "org")
	_ = forge.Probe(context.Background())
	if leaked {
		t.Fatal("authorization crossed an origin-changing redirect")
	}
}

func TestAzureQueryAlwaysIncludesVersion(t *testing.T) {
	parsed, err := url.Parse(azureQuery("/path", map[string]string{"filter": "refs/heads/main"}))
	if err != nil || parsed.Query().Get("api-version") != azureAPIVersion || parsed.Query().Get("filter") != "refs/heads/main" {
		t.Fatalf("query = %q, %v", parsed.RawQuery, err)
	}
}
