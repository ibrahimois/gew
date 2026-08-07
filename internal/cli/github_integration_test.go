package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gew/internal/forge"
)

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
	t.Setenv("GEW_PROVIDER", string(forge.ForgeGitHub))
	t.Setenv("GEW_AUTH_KIND", string(forge.AuthBearer))
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
		Version: stateVersion, Provider: forge.ForgeGitHub,
		Remote: forge.RepositoryRef{Forge: forge.ForgeGitHub, Server: server.URL, Namespace: "acme", Name: "empty", Canonical: "acme/empty"},
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
	if !errors.Is(err, forge.ErrUnsupported) {
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
