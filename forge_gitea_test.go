package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGiteaResolveRepositoryRequestAndAuthentication(t *testing.T) {
	const secret = "sentinel-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.EscapedPath() != "/api/v1/repos/space%20owner/demo" {
			t.Fatalf("request = %s %s", request.Method, request.URL.EscapedPath())
		}
		if got := request.Header.Get("Authorization"); got != "token "+secret {
			t.Fatalf("Authorization = %q", got)
		}
		json.NewEncoder(response).Encode(repository{DefaultBranch: "trunk"})
	}))
	defer server.Close()

	forge, err := newGiteaForge(profile{Provider: ForgeGitea, URL: server.URL, Token: secret})
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
	forge, err := newGiteaForge(profile{Provider: ForgeGitea, URL: server.URL, Token: secret})
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
	forge, err := newGiteaForge(profile{Provider: ForgeGitea, URL: "https://example.test", Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := forge.Capabilities()
	if !capabilities.ArchiveSnapshot || !capabilities.AtomicMultiFile || !capabilities.BranchCreate || !capabilities.Push {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if capabilities.ConditionalRef {
		t.Fatal("Gitea contents endpoint must not claim a branch-wide conditional ref")
	}
}
