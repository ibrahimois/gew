package main

import (
	"fmt"
	"sort"
	"strings"
)

type forgeFactory func(profile) (Forge, error)

type forgeDefinition struct {
	Kind        ForgeKind
	DefaultAuth AuthKind
	Factory     forgeFactory
}

var forgeDefinitions = []forgeDefinition{
	{Kind: ForgeAzure, DefaultAuth: AuthBearer, Factory: func(p profile) (Forge, error) { return newAzureForge(p) }},
	{Kind: ForgeBitbucket, DefaultAuth: AuthBearer, Factory: func(p profile) (Forge, error) { return newBitbucketForge(p) }},
	{Kind: ForgeGitea, DefaultAuth: AuthToken, Factory: func(p profile) (Forge, error) { return newGiteaForge(p) }},
	{Kind: ForgeGitHub, DefaultAuth: AuthBearer, Factory: func(p profile) (Forge, error) { return newGitHubForge(p) }},
	{Kind: ForgeGitLab, DefaultAuth: AuthBearer, Factory: func(p profile) (Forge, error) { return newGitLabForge(p) }},
}

func registeredForgeKinds() []ForgeKind {
	kinds := make([]ForgeKind, 0, len(forgeDefinitions))
	for _, definition := range forgeDefinitions {
		kinds = append(kinds, definition.Kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

func normalizeForgeKind(value string) (ForgeKind, error) {
	kind := ForgeKind(strings.ToLower(strings.TrimSpace(value)))
	if kind == "" {
		kind = ForgeGitea
	}
	if _, exists := lookupForgeDefinition(kind); !exists {
		return "", fmt.Errorf("unknown provider %q (available: %s)", value, joinForgeKinds(registeredForgeKinds()))
	}
	return kind, nil
}

func joinForgeKinds(kinds []ForgeKind) string {
	values := make([]string, len(kinds))
	for index, kind := range kinds {
		values[index] = string(kind)
	}
	return strings.Join(values, ", ")
}

func forgeFromProfile(p profile) (Forge, error) {
	kind, err := normalizeForgeKind(string(p.Provider))
	if err != nil {
		return nil, err
	}
	p.Provider = kind
	definition, _ := lookupForgeDefinition(kind)
	if p.AuthKind == "" {
		p.AuthKind = definition.DefaultAuth
	}
	return definition.Factory(p)
}

func defaultAuthKind(kind ForgeKind) AuthKind {
	definition, exists := lookupForgeDefinition(kind)
	if !exists {
		return ""
	}
	return definition.DefaultAuth
}

func lookupForgeDefinition(kind ForgeKind) (forgeDefinition, bool) {
	for _, definition := range forgeDefinitions {
		if definition.Kind == kind {
			return definition, true
		}
	}
	return forgeDefinition{}, false
}
