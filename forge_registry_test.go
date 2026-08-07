package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestForgeRegistryCatalogContract(t *testing.T) {
	wantKinds := []ForgeKind{ForgeAzure, ForgeBitbucket, ForgeGitea, ForgeGitHub, ForgeGitLab}
	if got := registeredForgeKinds(); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("registeredForgeKinds() = %#v, want %#v", got, wantKinds)
	}
	seen := make(map[ForgeKind]bool)
	for _, definition := range forgeDefinitions {
		if definition.Kind == "" || seen[definition.Kind] {
			t.Fatalf("invalid or duplicate definition kind %q", definition.Kind)
		}
		seen[definition.Kind] = true
		if definition.DefaultAuth == "" || definition.Factory == nil {
			t.Fatalf("incomplete definition for %s", definition.Kind)
		}
		if defaultAuthKind(definition.Kind) != definition.DefaultAuth {
			t.Fatalf("default auth for %s = %q", definition.Kind, defaultAuthKind(definition.Kind))
		}
	}
	if kind, err := normalizeForgeKind(""); err != nil || kind != ForgeGitea {
		t.Fatalf("empty provider = %q, %v", kind, err)
	}
	_, err := normalizeForgeKind("unknown")
	if err == nil {
		t.Fatal("unknown provider was accepted")
	}
	for _, kind := range wantKinds {
		if !strings.Contains(err.Error(), string(kind)) {
			t.Fatalf("unknown-provider error omits %s: %v", kind, err)
		}
	}
}

func TestForgeRegistryFactoriesConstructDeclaredKindsWithoutNetwork(t *testing.T) {
	profiles := map[ForgeKind]profile{
		ForgeAzure:     {URL: "https://dev.azure.com/example", Token: "token"},
		ForgeBitbucket: {URL: "https://bitbucket.org", Token: "token"},
		ForgeGitea:     {URL: "https://gitea.example.test", Token: "token"},
		ForgeGitHub:    {URL: "https://github.com", Token: "token"},
		ForgeGitLab:    {URL: "https://gitlab.com", Token: "token"},
	}
	for _, definition := range forgeDefinitions {
		t.Run(string(definition.Kind), func(t *testing.T) {
			selected := profiles[definition.Kind]
			selected.Provider = definition.Kind
			remote, err := forgeFromProfile(selected)
			if err != nil {
				t.Fatal(err)
			}
			if remote.Kind() != definition.Kind {
				t.Fatalf("factory kind = %q, want %q", remote.Kind(), definition.Kind)
			}
			_, writer := remote.(ForgeCommitWriter)
			if remote.Capabilities().Push && !writer {
				t.Fatal("push-enabled factory result has no writer")
			}
			if remote.Capabilities().BranchCreate && !writer {
				t.Fatal("branch-create factory result has no writer")
			}
		})
	}
}
