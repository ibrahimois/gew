package main

import (
	"fmt"
	"sort"
	"strings"
)

type forgeFactory func(profile) (Forge, error)

var forgeFactories = map[ForgeKind]forgeFactory{
	ForgeGitea:     func(p profile) (Forge, error) { return newGiteaForge(p) },
	ForgeGitHub:    func(p profile) (Forge, error) { return newGitHubForge(p) },
	ForgeGitLab:    func(p profile) (Forge, error) { return newGitLabForge(p) },
	ForgeBitbucket: func(p profile) (Forge, error) { return newBitbucketForge(p) },
	ForgeAzure:     func(p profile) (Forge, error) { return newAzureForge(p) },
}

func registeredForgeKinds() []ForgeKind {
	kinds := make([]ForgeKind, 0, len(forgeFactories))
	for kind := range forgeFactories {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

func normalizeForgeKind(value string) (ForgeKind, error) {
	kind := ForgeKind(strings.ToLower(strings.TrimSpace(value)))
	if kind == "" {
		kind = ForgeGitea
	}
	if _, exists := forgeFactories[kind]; !exists {
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
	if p.AuthKind == "" {
		p.AuthKind = defaultAuthKind(kind)
	}
	factory := forgeFactories[kind]
	return factory(p)
}

func defaultAuthKind(kind ForgeKind) AuthKind {
	switch kind {
	case ForgeGitHub, ForgeGitLab, ForgeBitbucket, ForgeAzure:
		return AuthBearer
	default:
		return AuthToken
	}
}
