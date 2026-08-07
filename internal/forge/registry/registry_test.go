package registry

import (
	"reflect"
	"strings"
	"testing"

	"gew/internal/forge"
)

func TestForgeRegistryCatalogContract(t *testing.T) {
	wantKinds := []forge.ForgeKind{forge.ForgeAzure, forge.ForgeBitbucket, forge.ForgeGitea, forge.ForgeGitHub, forge.ForgeGitLab}
	if got := Kinds(); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("Kinds() = %#v, want %#v", got, wantKinds)
	}
	seen := make(map[forge.ForgeKind]bool)
	for _, definition := range definitions {
		if definition.Kind == "" || seen[definition.Kind] {
			t.Fatalf("invalid or duplicate definition kind %q", definition.Kind)
		}
		seen[definition.Kind] = true
		if definition.DefaultAuth == "" || definition.Factory == nil {
			t.Fatalf("incomplete definition for %s", definition.Kind)
		}
		if DefaultAuthKind(definition.Kind) != definition.DefaultAuth {
			t.Fatalf("default auth for %s = %q", definition.Kind, DefaultAuthKind(definition.Kind))
		}
	}
	if kind, err := NormalizeKind(""); err != nil || kind != forge.ForgeGitea {
		t.Fatalf("empty provider = %q, %v", kind, err)
	}
	_, err := NormalizeKind("unknown")
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
	profiles := map[forge.ForgeKind]forge.Config{
		forge.ForgeAzure:     {URL: "https://dev.azure.com/example", Token: "token"},
		forge.ForgeBitbucket: {URL: "https://bitbucket.org", Token: "token"},
		forge.ForgeGitea:     {URL: "https://gitea.example.test", Token: "token"},
		forge.ForgeGitHub:    {URL: "https://github.com", Token: "token"},
		forge.ForgeGitLab:    {URL: "https://gitlab.com", Token: "token"},
	}
	for _, definition := range definitions {
		t.Run(string(definition.Kind), func(t *testing.T) {
			selected := profiles[definition.Kind]
			selected.Provider = definition.Kind
			remote, err := FromConfig(selected)
			if err != nil {
				t.Fatal(err)
			}
			if remote.Kind() != definition.Kind {
				t.Fatalf("factory kind = %q, want %q", remote.Kind(), definition.Kind)
			}
			_, writer := remote.(forge.ForgeCommitWriter)
			if remote.Capabilities().Push && !writer {
				t.Fatal("push-enabled factory result has no writer")
			}
			if remote.Capabilities().BranchCreate && !writer {
				t.Fatal("branch-create factory result has no writer")
			}
		})
	}
}
