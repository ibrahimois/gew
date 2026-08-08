package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateReleaseTag(t *testing.T) {
	for _, tag := range []string{"v0.6.0", "release/candidate-1"} {
		if err := validateReleaseTag(tag); err != nil {
			t.Errorf("valid tag %q: %v", tag, err)
		}
	}
	for _, tag := range []string{"", " v1", "v 1", "../v1", "refs//v1", "v1.lock", "v1~2"} {
		if err := validateReleaseTag(tag); err == nil {
			t.Errorf("unsafe tag %q accepted", tag)
		}
	}
}

func TestValidateReleaseFilesHashesAndRejectsDuplicateBasenames(t *testing.T) {
	root := t.TempDir()
	notes := filepath.Join(root, "notes.md")
	asset := filepath.Join(root, "asset.bin")
	if err := os.WriteFile(notes, []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	gotNotes, assets, err := validateReleaseFiles(notes, []string{asset})
	if err != nil || string(gotNotes) != "notes\n" || len(assets) != 1 || assets[0].Size != 3 || len(assets[0].SHA256) != 64 {
		t.Fatalf("validation = %q %#v %v", gotNotes, assets, err)
	}
	other := filepath.Join(t.TempDir(), "asset.bin")
	os.WriteFile(other, []byte("different"), 0o600)
	if _, _, err := validateReleaseFiles(notes, []string{asset, other}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate basename error = %v", err)
	}
}

func TestReleaseCreateAndResumeEndToEnd(t *testing.T) {
	const commit = "1111111111111111111111111111111111111111"
	created := 0
	uploads := 0
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/branches/main"):
			json.NewEncoder(response).Encode(map[string]any{"commit": map[string]string{"id": commit}})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/releases/tags/v0.6.0"):
			if created == 0 {
				http.NotFound(response, request)
				return
			}
			json.NewEncoder(response).Encode(map[string]any{"id": 7, "tag_name": "v0.6.0", "target_commitish": commit, "name": "gew v0.6.0", "body": "notes\n", "html_url": serverURL(request) + "/release"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/releases"):
			created++
			json.NewEncoder(response).Encode(map[string]any{"id": 7, "tag_name": "v0.6.0", "target_commitish": commit, "name": "gew v0.6.0", "body": "notes\n", "html_url": serverURL(request) + "/release"})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/releases/7/assets"):
			if uploads == 0 {
				json.NewEncoder(response).Encode([]any{})
				return
			}
			json.NewEncoder(response).Encode([]map[string]any{{"id": 9, "name": "asset.bin", "size": len(uploaded)}})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/releases/7/assets"):
			file, _, err := request.FormFile("attachment")
			if err != nil {
				t.Fatal(err)
			}
			uploaded, _ = io.ReadAll(file)
			file.Close()
			uploads++
			json.NewEncoder(response).Encode(map[string]any{"id": 9, "name": "asset.bin", "size": len(uploaded)})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/releases/assets/9"):
			response.Write(uploaded)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	t.Setenv("GEW_SERVER", server.URL)
	t.Setenv("GEW_TOKEN", "secret")
	t.Setenv("GEW_PROVIDER", "gitea")
	t.Setenv("GEW_AUTH_KIND", "token")

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := scanWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveState(root, workspaceState{Backend: WorkspaceGew, Provider: ForgeGitea, Remote: RepositoryRef{Forge: ForgeGitea, Server: server.URL, Namespace: "acme", Name: "demo"}, Branch: "main", BaseCommit: commit, Files: metadata}); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(t.TempDir(), "asset.bin")
	if err := os.WriteFile(asset, []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	original, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)
	var output bytes.Buffer
	a := app{out: &output, errOut: &output}
	args := []string{"create", "v0.6.0", "--title", "gew v0.6.0", "--notes-file", "README.md", "--asset", asset}
	if err := runTestCommand(a, "release", args...); err != nil {
		t.Fatal(err)
	}
	if err := runTestCommand(a, "release", append(args, "--resume")...); err != nil {
		t.Fatal(err)
	}
	if created != 1 || uploads != 1 || string(uploaded) != "a\x00b" || !strings.Contains(output.String(), "Published v0.6.0") {
		t.Fatalf("created=%d uploads=%d uploaded=%q output=%q", created, uploads, uploaded, output.String())
	}
}

func serverURL(request *http.Request) string {
	return fmt.Sprintf("http://%s", request.Host)
}
