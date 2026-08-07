// Package registry maps configured forge kinds to adapter constructors.
package registry

import (
	"fmt"
	"sort"
	"strings"

	"gew/internal/forge"
	"gew/internal/forge/azure"
	"gew/internal/forge/bitbucket"
	"gew/internal/forge/gitea"
	"gew/internal/forge/github"
	"gew/internal/forge/gitlab"
)

type factory func(forge.Config) (forge.Forge, error)

type definition struct {
	Kind        forge.ForgeKind
	DefaultAuth forge.AuthKind
	Factory     factory
}

var definitions = []definition{
	{Kind: forge.ForgeAzure, DefaultAuth: forge.AuthBearer, Factory: func(p forge.Config) (forge.Forge, error) { return azure.New(p) }},
	{Kind: forge.ForgeBitbucket, DefaultAuth: forge.AuthBearer, Factory: func(p forge.Config) (forge.Forge, error) { return bitbucket.New(p) }},
	{Kind: forge.ForgeGitea, DefaultAuth: forge.AuthToken, Factory: func(p forge.Config) (forge.Forge, error) { return gitea.New(p) }},
	{Kind: forge.ForgeGitHub, DefaultAuth: forge.AuthBearer, Factory: func(p forge.Config) (forge.Forge, error) { return github.New(p) }},
	{Kind: forge.ForgeGitLab, DefaultAuth: forge.AuthBearer, Factory: func(p forge.Config) (forge.Forge, error) { return gitlab.New(p) }},
}

func Kinds() []forge.ForgeKind {
	kinds := make([]forge.ForgeKind, 0, len(definitions))
	for _, definition := range definitions {
		kinds = append(kinds, definition.Kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

func NormalizeKind(value string) (forge.ForgeKind, error) {
	kind := forge.ForgeKind(strings.ToLower(strings.TrimSpace(value)))
	if kind == "" {
		kind = forge.ForgeGitea
	}
	if _, exists := lookup(kind); !exists {
		return "", fmt.Errorf("unknown provider %q (available: %s)", value, joinKinds(Kinds()))
	}
	return kind, nil
}

func joinKinds(kinds []forge.ForgeKind) string {
	values := make([]string, len(kinds))
	for index, kind := range kinds {
		values[index] = string(kind)
	}
	return strings.Join(values, ", ")
}

func FromConfig(p forge.Config) (forge.Forge, error) {
	kind, err := NormalizeKind(string(p.Provider))
	if err != nil {
		return nil, err
	}
	p.Provider = kind
	definition, _ := lookup(kind)
	if p.AuthKind == "" {
		p.AuthKind = definition.DefaultAuth
	}
	return definition.Factory(p)
}

func DefaultAuthKind(kind forge.ForgeKind) forge.AuthKind {
	definition, exists := lookup(kind)
	if !exists {
		return ""
	}
	return definition.DefaultAuth
}

func lookup(kind forge.ForgeKind) (definition, bool) {
	for _, definition := range definitions {
		if definition.Kind == kind {
			return definition, true
		}
	}
	return definition{}, false
}
