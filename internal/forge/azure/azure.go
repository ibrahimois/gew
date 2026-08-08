package azure

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gew/internal/forge"
)

const (
	azureAPIVersion = "7.1"
	azureZeroOID    = "0000000000000000000000000000000000000000"
)

type azureForge struct {
	server       string
	organization string
	requester    *forge.HTTPRequester
}

var (
	_ forge.Forge             = (*azureForge)(nil)
	_ forge.ForgeCommitWriter = (*azureForge)(nil)
)

type azureRepository struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
	WebURL        string `json:"webUrl"`
	Project       struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
}

type azureCommit struct {
	CommitID string   `json:"commitId"`
	Comment  string   `json:"comment"`
	Parents  []string `json:"parents"`
}

type azureItem struct {
	ObjectID      string `json:"objectId"`
	GitObjectType string `json:"gitObjectType"`
	CommitID      string `json:"commitId"`
	Path          string `json:"path"`
	IsFolder      bool   `json:"isFolder"`
	ContentLength int64  `json:"contentLength"`
}

type azurePushResponse struct {
	Commits    []azureCommit `json:"commits"`
	RefUpdates []struct {
		Name        string `json:"name"`
		OldObjectID string `json:"oldObjectId"`
		NewObjectID string `json:"newObjectId"`
	} `json:"refUpdates"`
}

func New(p forge.Config) (*azureForge, error) {
	server, organization, err := normalizeAzureServer(p.URL)
	if err != nil {
		return nil, err
	}
	p.URL = server
	return newAzureForgeWithAPI(p, server, organization)
}

func newAzureForgeWithAPI(p forge.Config, apiBase, organization string) (*azureForge, error) {
	p.Provider = forge.ForgeAzure
	if p.AuthKind != forge.AuthBearer && p.AuthKind != forge.AuthPAT {
		return nil, fmt.Errorf("azure devops supports bearer or pat authentication, got %q", p.AuthKind)
	}
	auth := func(request *http.Request) {
		if p.AuthKind == forge.AuthPAT {
			request.SetBasicAuth("", p.Token)
			return
		}
		request.Header.Set("Authorization", "Bearer "+p.Token)
	}
	requester, err := forge.NewHTTPRequester(p, apiBase, auth, make(http.Header))
	if err != nil {
		return nil, err
	}
	return &azureForge{
		server: strings.TrimRight(apiBase, "/"), organization: organization,
		requester: requester,
	}, nil
}

func (a *azureForge) Kind() forge.ForgeKind { return forge.ForgeAzure }

func (a *azureForge) Capabilities() forge.ForgeCapabilities {
	return forge.ForgeCapabilities{BranchCreate: true, Push: true}
}

func (a *azureForge) Probe(ctx context.Context) error {
	var response struct {
		Count int `json:"count"`
	}
	return a.requester.DoJSON(ctx, http.MethodGet, azureQuery("/_apis/projects", map[string]string{"$top": "1"}), nil, &response)
}

func (a *azureForge) ResolveRepository(ctx context.Context, value string) (forge.RepositoryRef, forge.RepositoryInfo, error) {
	organization, project, repository, err := parseAzureRepository(value, a.organization)
	if err != nil {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, err
	}
	if !strings.EqualFold(organization, a.organization) {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, fmt.Errorf("azure repository organization %q does not match profile organization %q", organization, a.organization)
	}
	endpoint := "/" + url.PathEscape(project) + "/_apis/git/repositories/" + url.PathEscape(repository)
	var response azureRepository
	if err := a.requester.DoJSON(ctx, http.MethodGet, azureQuery(endpoint, nil), nil, &response); err != nil {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, err
	}
	if response.ID == "" || response.Name == "" || response.Project.ID == "" || response.Project.Name == "" {
		return forge.RepositoryRef{}, forge.RepositoryInfo{}, errors.New("azure devops returned incomplete repository identity")
	}
	canonical := organization + "/" + response.Project.Name + "/" + response.Name
	ref := forge.RepositoryRef{
		Forge: forge.ForgeAzure, Server: a.server, Namespace: organization, Project: response.Project.ID,
		Name: response.Name, RemoteID: response.ID, Canonical: canonical,
	}
	branch := strings.TrimPrefix(response.DefaultBranch, "refs/heads/")
	return ref, forge.RepositoryInfo{DefaultBranch: branch, Empty: response.DefaultBranch == ""}, nil
}

func (a *azureForge) Head(ctx context.Context, ref forge.RepositoryRef, branch string) (string, error) {
	fullRef := azureFullRef(branch)
	endpoint := azureRepoAPIPath(ref) + "/refs"
	var response struct {
		Value []struct {
			Name     string `json:"name"`
			ObjectID string `json:"objectId"`
		} `json:"value"`
	}
	query := map[string]string{"filter": fullRef, "$top": "2"}
	if err := a.requester.DoJSON(ctx, http.MethodGet, azureQuery(endpoint, query), nil, &response); err != nil {
		return "", err
	}
	for _, item := range response.Value {
		if item.Name == fullRef && item.ObjectID != "" {
			return item.ObjectID, nil
		}
	}
	return "", forge.ErrNotFound
}

func (a *azureForge) Tree(ctx context.Context, ref forge.RepositoryRef, commit string) (map[string]forge.RemoteFile, error) {
	if strings.TrimSpace(commit) == "" {
		return nil, errors.New("azure tree requires an exact commit ID")
	}
	query := map[string]string{
		"scopePath": "/", "recursionLevel": "Full", "includeContentMetadata": "true",
		"versionDescriptor.version": commit, "versionDescriptor.versionType": "commit",
	}
	var response struct {
		Value []azureItem `json:"value"`
	}
	if err := a.requester.DoJSON(ctx, http.MethodGet, azureQuery(azureRepoAPIPath(ref)+"/items", query), nil, &response); err != nil {
		return nil, err
	}
	result := make(map[string]forge.RemoteFile)
	for _, item := range response.Value {
		if item.IsFolder || item.GitObjectType == "tree" || item.Path == "/" {
			continue
		}
		if item.CommitID != "" && item.CommitID != commit {
			return nil, fmt.Errorf("azure item %s resolved to inconsistent commit %s", item.Path, item.CommitID)
		}
		cleaned, err := forge.ValidateRemotePath(item.Path)
		if err != nil {
			return nil, err
		}
		if item.ObjectID == "" {
			return nil, fmt.Errorf("azure item %s has no blob ID", cleaned)
		}
		result[cleaned] = forge.RemoteFile{BlobID: azureBlobID(commit, cleaned), Mode: 0o100644, Size: item.ContentLength}
	}
	return result, nil
}

func (a *azureForge) Blob(ctx context.Context, ref forge.RepositoryRef, file forge.RemoteFile) ([]byte, error) {
	commit, filePath, err := parseAzureBlobID(file.BlobID)
	if err != nil {
		return nil, err
	}
	query := map[string]string{
		"path": "/" + filePath, "$format": "octetStream",
		"versionDescriptor.version": commit, "versionDescriptor.versionType": "commit",
	}
	return a.requester.Download(ctx, azureQuery(azureRepoAPIPath(ref)+"/items", query))
}

func (a *azureForge) CommitDetails(ctx context.Context, ref forge.RepositoryRef, commit string) (forge.RemoteCommit, error) {
	var response azureCommit
	endpoint := azureRepoAPIPath(ref) + "/commits/" + url.PathEscape(commit)
	if err := a.requester.DoJSON(ctx, http.MethodGet, azureQuery(endpoint, nil), nil, &response); err != nil {
		return forge.RemoteCommit{}, err
	}
	result := forge.RemoteCommit{ID: response.CommitID, Message: response.Comment, ParentIDs: append([]string(nil), response.Parents...)}
	for skip := 0; ; skip += 1000 {
		var page struct {
			Value []struct {
				Item struct {
					Path string `json:"path"`
				} `json:"item"`
			} `json:"changes"`
		}
		changesEndpoint := endpoint + "/changes"
		query := map[string]string{"$top": "1000", "$skip": strconv.Itoa(skip)}
		if err := a.requester.DoJSON(ctx, http.MethodGet, azureQuery(changesEndpoint, query), nil, &page); err != nil {
			return forge.RemoteCommit{}, err
		}
		for _, changed := range page.Value {
			cleaned, err := forge.ValidateRemotePath(changed.Item.Path)
			if err != nil {
				return forge.RemoteCommit{}, err
			}
			result.Paths = append(result.Paths, cleaned)
		}
		if len(page.Value) < 1000 {
			break
		}
	}
	return result, nil
}

func (a *azureForge) ApplyCommit(ctx context.Context, request forge.ApplyCommitRequest) (forge.ApplyCommitResult, error) {
	if request.ExpectedHead == "" && request.NewBranch != "" {
		return forge.ApplyCommitResult{}, errors.New("azure new branch requires a base commit")
	}
	target := request.Branch
	oldObjectID := request.ExpectedHead
	if request.NewBranch != "" {
		target = request.NewBranch
	}
	if oldObjectID == "" {
		oldObjectID = azureZeroOID
	}
	changes := make([]map[string]any, 0, len(request.Changes))
	for _, change := range request.Changes {
		changeType, err := azureChangeType(change.Operation)
		if err != nil {
			return forge.ApplyCommitResult{}, err
		}
		encoded := map[string]any{"changeType": changeType, "item": map[string]string{"path": "/" + change.Path}}
		if changeType != "delete" {
			encoded["newContent"] = map[string]string{
				"content": base64.StdEncoding.EncodeToString(change.Content), "contentType": "base64encoded",
			}
		}
		changes = append(changes, encoded)
	}
	payload := map[string]any{
		"refUpdates": []map[string]string{{"name": azureFullRef(target), "oldObjectId": oldObjectID}},
		"commits":    []map[string]any{{"comment": request.Message, "changes": changes}},
	}
	var response azurePushResponse
	endpoint := azureQuery(azureRepoAPIPath(request.Repository)+"/pushes", nil)
	if err := a.requester.DoJSON(ctx, http.MethodPost, endpoint, payload, &response); err != nil {
		if forge.RemoteErrorHasStatus(err, http.StatusBadRequest, http.StatusConflict) {
			err = forge.ConfirmStaleHead(ctx, a, request.Repository, request.Branch, request.ExpectedHead, err)
		}
		return forge.ApplyCommitResult{}, err
	}
	if len(response.Commits) != 1 || len(response.RefUpdates) != 1 {
		return forge.ApplyCommitResult{}, errors.New("azure devops returned a malformed push response")
	}
	commit := response.Commits[0]
	update := response.RefUpdates[0]
	if commit.CommitID == "" || update.Name != azureFullRef(target) || update.NewObjectID != commit.CommitID {
		return forge.ApplyCommitResult{}, errors.New("azure devops push response did not confirm the requested ref update")
	}
	if request.NewBranch == "" && update.OldObjectID != "" && update.OldObjectID != oldObjectID {
		return forge.ApplyCommitResult{}, errors.New("azure devops push response reported an unexpected previous head")
	}
	if request.NewBranch != "" && update.OldObjectID != "" && update.OldObjectID != azureZeroOID && update.OldObjectID != oldObjectID {
		return forge.ApplyCommitResult{}, errors.New("azure devops new-branch response reported an unexpected previous head")
	}
	result := forge.ApplyCommitResult{CommitID: commit.CommitID, ParentIDs: append([]string(nil), commit.Parents...), ConditionalRef: true}
	if request.ExpectedHead != "" && len(result.ParentIDs) > 0 && result.ParentIDs[0] != request.ExpectedHead {
		return forge.ApplyCommitResult{}, errors.New("azure devops created the commit from an unexpected parent")
	}
	return result, nil
}

func azureRepoAPIPath(ref forge.RepositoryRef) string {
	return "/" + url.PathEscape(ref.Project) + "/_apis/git/repositories/" + url.PathEscape(ref.RemoteID)
}

func azureQuery(endpoint string, values map[string]string) string {
	query := make(url.Values)
	query.Set("api-version", azureAPIVersion)
	for key, value := range values {
		query.Set(key, value)
	}
	return endpoint + "?" + query.Encode()
}

func azureFullRef(branch string) string {
	branch = strings.TrimSpace(branch)
	if strings.HasPrefix(branch, "refs/heads/") {
		return branch
	}
	return "refs/heads/" + strings.TrimPrefix(branch, "/")
}

func azureBlobID(commit, filePath string) string { return commit + "\n" + filePath }

func parseAzureBlobID(value string) (string, string, error) {
	commit, filePath, found := strings.Cut(value, "\n")
	if !found || commit == "" {
		return "", "", errors.New("invalid azure remote file identity")
	}
	cleaned, err := forge.ValidateRemotePath(filePath)
	return commit, cleaned, err
}

func azureChangeType(operation string) (string, error) {
	switch operation {
	case "create":
		return "add", nil
	case "update":
		return "edit", nil
	case "delete":
		return "delete", nil
	default:
		return "", fmt.Errorf("unsupported azure change operation %q", operation)
	}
}

func normalizeAzureServer(raw string) (string, string, error) {
	server, err := forge.NormalizeServerURL(raw)
	if err != nil {
		return "", "", err
	}
	parsed, err := url.Parse(server)
	if err != nil {
		return "", "", err
	}
	host := strings.ToLower(parsed.Hostname())
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	var organization string
	switch {
	case host == "dev.azure.com":
		if len(parts) != 1 || parts[0] == "" {
			return "", "", errors.New("azure profile URL must be https://dev.azure.com/ORGANIZATION")
		}
		organization = parts[0]
	case strings.HasSuffix(host, ".visualstudio.com"):
		if len(parts) > 1 || (len(parts) == 1 && parts[0] != "") {
			return "", "", errors.New("legacy Azure profile URL must not contain a project path")
		}
		organization = strings.TrimSuffix(host, ".visualstudio.com")
	default:
		return "", "", errors.New("azure adapter supports Azure DevOps Services only")
	}
	if organization == "" {
		return "", "", errors.New("azure organization is empty")
	}
	return "https://dev.azure.com/" + url.PathEscape(organization), organization, nil
}

func parseAzureRepository(value, profileOrganization string) (string, string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", "", errors.New("azure repository is empty")
	}
	organization := profileOrganization
	repositoryPath := value
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() {
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", "", "", errors.New("azure repository URL must not contain a query or fragment")
		}
		host := strings.ToLower(parsed.Hostname())
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		switch {
		case host == "dev.azure.com":
			if len(parts) != 4 || parts[0] == "" || parts[2] != "_git" {
				return "", "", "", errors.New("azure repository URL must be https://dev.azure.com/ORGANIZATION/PROJECT/_git/REPOSITORY")
			}
			organization = parts[0]
			repositoryPath = parts[1] + "/" + parts[3]
		case strings.HasSuffix(host, ".visualstudio.com"):
			if len(parts) != 3 || parts[1] != "_git" {
				return "", "", "", errors.New("legacy Azure repository URL must contain PROJECT/_git/REPOSITORY")
			}
			organization = strings.TrimSuffix(host, ".visualstudio.com")
			repositoryPath = parts[0] + "/" + parts[2]
		default:
			return "", "", "", errors.New("azure repository URL must use dev.azure.com or visualstudio.com")
		}
	}
	parts := strings.Split(strings.Trim(repositoryPath, "/"), "/")
	if len(parts) != 2 || organization == "" || parts[0] == "" || parts[1] == "" {
		return "", "", "", errors.New("azure repository must be PROJECT/REPOSITORY")
	}
	repository := strings.TrimSuffix(parts[1], ".git")
	if repository == "" {
		return "", "", "", errors.New("azure repository name is empty")
	}
	return organization, parts[0], repository, nil
}
