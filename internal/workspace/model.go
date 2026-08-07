// Package workspace defines backend-independent workspace metadata.
package workspace

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gew/internal/forge"
)

type BackendKind string

const (
	Gew BackendKind = "gew"
	Git BackendKind = "git"
)

type HybridState struct {
	BranchRef      string `json:"branch_ref"`
	TrackingRef    string `json:"tracking_ref"`
	LastLocalOID   string `json:"last_local_oid,omitempty"`
	LastProviderID string `json:"last_provider_id,omitempty"`
	MigrationID    string `json:"migration_id,omitempty"`
}

type PreparedGitExport struct {
	Version          int      `json:"version"`
	LocalOID         string   `json:"local_oid"`
	ExpectedProvider string   `json:"expected_provider"`
	TargetBranch     string   `json:"target_branch"`
	Message          string   `json:"message"`
	Paths            []string `json:"paths"`
	Digest           string   `json:"digest"`
}

type GitExportReceipt struct {
	Version      int      `json:"version"`
	LocalOID     string   `json:"local_oid"`
	ProviderID   string   `json:"provider_id"`
	ProviderBase string   `json:"provider_base"`
	Message      string   `json:"message"`
	Paths        []string `json:"paths"`
	Linearized   bool     `json:"linearized,omitempty"`
}

func NormalizeBackend(kind BackendKind) (BackendKind, error) {
	if kind == "" {
		return Gew, nil
	}
	switch kind {
	case Gew, Git:
		return kind, nil
	default:
		return "", fmt.Errorf("unknown workspace backend %q", kind)
	}
}

func TrackingRef(provider forge.ForgeKind, branch string) (string, error) {
	if provider == "" {
		return "", errors.New("tracking ref requires a provider")
	}
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if branch == "" || strings.ContainsAny(branch, "\x00\r\n") || strings.Contains(branch, "..") || strings.HasSuffix(branch, ".lock") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return "", fmt.Errorf("invalid branch for private tracking ref %q", branch)
	}
	escaped := url.PathEscape(branch)
	if escaped == "." || escaped == "" {
		return "", fmt.Errorf("invalid branch for private tracking ref %q", branch)
	}
	return "refs/gew/remotes/" + string(provider) + "/" + escaped, nil
}
