package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	toolVersion  = "0.3.0"
	stateVersion = 2
)

type profile struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Insecure bool   `json:"insecure,omitempty"`
}

type config struct {
	Current  string             `json:"current"`
	Profiles map[string]profile `json:"profiles"`
}

type fileState struct {
	BlobSHA string `json:"blob_sha,omitempty"`
	Hash    string `json:"hash"`
	Mode    uint32 `json:"mode,omitempty"`
}

type workspaceState struct {
	Version    int                  `json:"version"`
	Server     string               `json:"server"`
	Owner      string               `json:"owner"`
	Repository string               `json:"repository"`
	Branch     string               `json:"branch"`
	BaseCommit string               `json:"base_commit"`
	Files      map[string]fileState `json:"files"`
	Queue      []string             `json:"queue,omitempty"`
	History    []string             `json:"history,omitempty"`
	LocalHead  string               `json:"local_head,omitempty"`
}

type client struct {
	baseURL string
	token   string
	http    *http.Client
}

type apiError struct {
	Status int
	Method string
	URL    string
	Body   string
}

func (e *apiError) Error() string {
	message := strings.TrimSpace(e.Body)
	if message == "" {
		message = http.StatusText(e.Status)
	}
	return fmt.Sprintf("Gitea API %s %s returned %d: %s", e.Method, e.URL, e.Status, message)
}

func isAPINotFound(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

type repository struct {
	DefaultBranch string `json:"default_branch"`
	Empty         bool   `json:"empty"`
}

type branchResponse struct {
	Name   string `json:"name"`
	Commit struct {
		ID  string `json:"id"`
		SHA string `json:"sha"`
	} `json:"commit"`
}

func (b branchResponse) commitSHA() string {
	if b.Commit.ID != "" {
		return b.Commit.ID
	}
	return b.Commit.SHA
}

type treeEntry struct {
	Path string `json:"path"`
	SHA  string `json:"sha"`
	Type string `json:"type"`
	Mode string `json:"mode"`
}

type treeResponse struct {
	Tree       []treeEntry `json:"tree"`
	Truncated  bool        `json:"truncated"`
	Page       int         `json:"page"`
	TotalCount int         `json:"total_count"`
}

type blobResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	SHA      string `json:"sha"`
}

type commitDetails struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Files []struct {
		Filename string `json:"filename"`
	} `json:"files"`
}

type change struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type changeOperation struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	SHA       string `json:"sha,omitempty"`
}

type changeFilesRequest struct {
	Branch    string            `json:"branch"`
	NewBranch string            `json:"new_branch,omitempty"`
	Message   string            `json:"message"`
	Files     []changeOperation `json:"files"`
}

type app struct {
	out    io.Writer
	errOut io.Writer
}

func main() {
	a := app{out: os.Stdout, errOut: os.Stderr}
	if err := a.run(os.Args[1:]); err != nil {
		fmt.Fprintf(a.errOut, "gew: %v\n", err)
		os.Exit(1)
	}
}

func (a app) run(args []string) error {
	if len(args) == 0 {
		a.usage()
		return nil
	}

	switch args[0] {
	case "help", "-h", "--help":
		a.usage()
		return nil
	case "version", "--version":
		fmt.Fprintf(a.out, "gew %s\n", toolVersion)
		return nil
	case "login":
		return a.login(args[1:])
	case "doctor":
		return a.doctor(args[1:])
	case "clone":
		return a.clone(args[1:])
	case "status":
		return a.status(args[1:])
	case "add":
		return a.add(args[1:])
	case "reset":
		return a.reset(args[1:])
	case "diff":
		return a.diff(args[1:])
	case "commit":
		return a.commit(args[1:])
	case "log":
		return a.log(args[1:])
	case "pull":
		return a.pull(args[1:])
	case "merge":
		return a.merge(args[1:])
	case "push":
		return a.push(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run 'gew help'", args[0])
	}
}

func (a app) usage() {
	fmt.Fprint(a.out, `gew - a small REST-only workspace client for Gitea

Usage:
  gew login [--name NAME] [--token TOKEN] [--insecure] URL
  gew doctor
  gew clone [--branch BRANCH] OWNER/REPO [DIRECTORY]
  gew status [--json]
  gew add [-A|--all] PATH...
  gew reset [PATH...]
  gew diff [--staged]
  gew commit -m MESSAGE
  gew log [--oneline]
  gew pull [--ff-only]
  gew merge (--abort | --continue [-m MESSAGE])
  gew push [--new-branch BRANCH]
  gew version

Environment:
  GEW_SERVER   Override the configured Gitea URL
  GEW_TOKEN    Override the configured access token
  GEW_PROFILE  Select a saved login profile
  GEW_CONFIG   Override the config file path

gew uses Git-like staging and local queued commits, backed by Gitea's REST API.
It does not implement Git's object database, rebase, or native merge commits.
`)
}

func (a app) login(args []string) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	name := flags.String("name", "default", "profile name")
	tokenFlag := flags.String("token", "", "access token (prompted if omitted)")
	insecure := flags.Bool("insecure", false, "skip TLS verification")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: gew login [--name NAME] [--token TOKEN] [--insecure] URL")
	}

	server, err := normalizeServerURL(flags.Arg(0))
	if err != nil {
		return err
	}
	token := strings.TrimSpace(*tokenFlag)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GEW_TOKEN"))
	}
	if token == "" {
		return errors.New("access token required; pass --token or set GEW_TOKEN")
	}

	cfg, cfgPath, err := readConfig()
	if err != nil {
		return err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]profile)
	}
	cfg.Current = *name
	cfg.Profiles[*name] = profile{URL: server, Token: token, Insecure: *insecure}
	if err := writeConfig(cfgPath, cfg); err != nil {
		return err
	}

	c := newClient(cfg.Profiles[*name])
	var version struct {
		Version string `json:"version"`
	}
	if err := c.getJSON("/api/v1/version", &version); err != nil {
		return fmt.Errorf("profile saved, but connection test failed: %w", err)
	}
	fmt.Fprintf(a.out, "Saved profile %q for %s (Gitea %s).\n", *name, server, version.Version)
	if *insecure {
		fmt.Fprintln(a.errOut, "Warning: TLS certificate verification is disabled for this profile.")
	}
	return nil
}

func (a app) doctor(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: gew doctor")
	}
	c, err := clientFromConfig()
	if err != nil {
		return err
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := c.getJSON("/api/v1/version", &version); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Connected to %s\nGitea version: %s\nAuthentication: token accepted\n", c.baseURL, version.Version)
	return nil
}

func (a app) clone(args []string) error {
	flags := flag.NewFlagSet("clone", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	branchFlag := flags.String("branch", "", "branch to download")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		return errors.New("usage: gew clone [--branch BRANCH] OWNER/REPO [DIRECTORY]")
	}
	owner, repo, err := parseRepository(flags.Arg(0))
	if err != nil {
		return err
	}
	destination := repo
	if flags.NArg() == 2 {
		destination = flags.Arg(1)
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if err := ensureEmptyDestination(absDestination); err != nil {
		return err
	}
	c, err := clientFromConfig()
	if err != nil {
		return err
	}

	var repoInfo repository
	if err := c.getJSON(repoAPIPath(owner, repo), &repoInfo); err != nil {
		return err
	}
	branch := *branchFlag
	if branch == "" {
		branch = repoInfo.DefaultBranch
	}
	if branch == "" {
		branch = "main"
	}
	if repoInfo.Empty {
		if err := os.MkdirAll(absDestination, 0o755); err != nil {
			return err
		}
		state := workspaceState{
			Version: stateVersion, Server: c.baseURL, Owner: owner, Repository: repo,
			Branch: branch, BaseCommit: "", Files: make(map[string]fileState),
		}
		if err := saveState(absDestination, state); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Prepared empty %s/%s workspace on %s in %s\n", owner, repo, branch, absDestination)
		return nil
	}
	commit, err := c.branchCommit(owner, repo, branch)
	if err != nil {
		return err
	}
	archive, err := c.download(archiveAPIPath(owner, repo, branch))
	if err != nil {
		return err
	}
	remoteFiles, err := c.tree(owner, repo, commit)
	if err != nil {
		return err
	}

	created := false
	if _, statErr := os.Stat(absDestination); errors.Is(statErr, os.ErrNotExist) {
		if err := os.MkdirAll(absDestination, 0o755); err != nil {
			return err
		}
		created = true
	}
	if err := extractArchive(archive, absDestination); err != nil {
		if created {
			_ = os.Remove(absDestination)
		}
		return err
	}
	localFiles, err := scanWorkspace(absDestination)
	if err != nil {
		return err
	}
	state := workspaceState{
		Version: stateVersion, Server: c.baseURL, Owner: owner, Repository: repo,
		Branch: branch, BaseCommit: commit, Files: mergeFileMetadata(localFiles, remoteFiles),
	}
	if err := saveState(absDestination, state); err != nil {
		return err
	}
	if err := ensureBaselineObjects(absDestination, state.Files); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Downloaded %s/%s (%s at %.12s) into %s\n", owner, repo, branch, commit, absDestination)
	return nil
}

func (a app) status(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: gew status [--json]")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	index, err := loadIndex(root)
	if err != nil {
		return err
	}
	stagedChanges := changesBetween(state.Files, effectiveIndexFiles(state.Files, index))
	current, err := scanWorkspace(root)
	if err != nil {
		return err
	}
	unstagedChanges := changesBetween(effectiveIndexFiles(state.Files, index), current)
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if *asJSON {
		payload := struct {
			Repository string          `json:"repository"`
			Branch     string          `json:"branch"`
			BaseCommit string          `json:"base_commit"`
			Unpushed   int             `json:"unpushed_commits"`
			Staged     []change        `json:"staged"`
			Unstaged   []change        `json:"unstaged"`
			Merging    bool            `json:"merging"`
			Conflicts  []mergeConflict `json:"conflicts,omitempty"`
		}{state.Owner + "/" + state.Repository, state.Branch, state.BaseCommit, len(state.Queue), stagedChanges, unstagedChanges, mergeState != nil, nil}
		if mergeState != nil {
			payload.Conflicts = mergeState.Conflicts
		}
		encoder := json.NewEncoder(a.out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	}
	fmt.Fprintf(a.out, "On branch %s\nRepository: %s/%s\n", state.Branch, state.Owner, state.Repository)
	if len(state.Queue) > 0 {
		fmt.Fprintf(a.out, "Your branch has %d unpushed gew commit(s).\n", len(state.Queue))
	}
	if mergeState != nil {
		fmt.Fprintf(a.out, "A merge with remote %.12s is in progress (%d conflict(s)).\n", mergeState.RemoteCommit, len(mergeState.Conflicts))
		fmt.Fprintln(a.out, "Resolve conflicts and run 'gew merge --continue', or run 'gew merge --abort'.")
	}
	if len(stagedChanges) == 0 && len(unstagedChanges) == 0 && len(state.Queue) == 0 && mergeState == nil {
		fmt.Fprintln(a.out, "Workspace is clean.")
		return nil
	}
	if len(stagedChanges) > 0 {
		fmt.Fprintln(a.out, "\nChanges to be committed:")
		for _, item := range stagedChanges {
			fmt.Fprintf(a.out, "  %-8s %s\n", item.Kind, item.Path)
		}
	}
	if len(unstagedChanges) > 0 {
		fmt.Fprintln(a.out, "\nChanges not staged for commit:")
		for _, item := range unstagedChanges {
			fmt.Fprintf(a.out, "  %-8s %s\n", item.Kind, item.Path)
		}
	}
	return nil
}

func (a app) pull(args []string) error {
	flags := flag.NewFlagSet("pull", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	ffOnly := flags.Bool("ff-only", false, "refuse local merges")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: gew pull [--ff-only]")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	index, err := loadIndex(root)
	if err != nil {
		return err
	}
	if len(index.Entries) != 0 {
		return errors.New("workspace has staged changes; commit or reset them before pulling")
	}
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if mergeState != nil {
		return errors.New("a merge is already in progress; continue or abort it first")
	}
	changes, err := workspaceChanges(root, state)
	if err != nil {
		return err
	}
	if len(state.Queue) != 0 && len(changes) != 0 {
		return errors.New("workspace has both unpushed commits and additional unstaged changes; commit or restore the unstaged changes first")
	}
	c, err := clientForWorkspace(state)
	if err != nil {
		return err
	}
	commit, err := c.branchCommit(state.Owner, state.Repository, state.Branch)
	if err != nil {
		if isAPINotFound(err) && state.BaseCommit == "" {
			fmt.Fprintln(a.out, "Already up to date (remote repository is empty).")
			return nil
		}
		return err
	}
	if commit == state.BaseCommit {
		fmt.Fprintln(a.out, "Already up to date.")
		return nil
	}
	if len(changes) != 0 || len(state.Queue) != 0 {
		if *ffOnly {
			return errors.New("fast-forward pull is not possible with local changes or unpushed commits")
		}
		return a.mergeRemote(root, state, c, commit, len(changes) != 0)
	}
	archive, err := c.download(archiveAPIPath(state.Owner, state.Repository, state.Branch))
	if err != nil {
		return err
	}
	remoteFiles, err := c.tree(state.Owner, state.Repository, commit)
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp("", "gew-pull-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := extractArchive(archive, stage); err != nil {
		return err
	}
	if _, err := scanWorkspace(stage); err != nil {
		return fmt.Errorf("validate downloaded snapshot: %w", err)
	}
	if err := replaceTrackedFiles(root, stage, state.Files); err != nil {
		return err
	}
	localFiles, err := scanWorkspace(root)
	if err != nil {
		return err
	}
	state.BaseCommit = commit
	state.Files = mergeFileMetadata(localFiles, remoteFiles)
	if err := saveState(root, state); err != nil {
		return err
	}
	if err := ensureBaselineObjects(root, state.Files); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Updated %s to %.12s.\n", state.Branch, commit)
	return nil
}

func (a app) push(args []string) error {
	flags := flag.NewFlagSet("push", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	newBranch := flags.String("new-branch", "", "commit changes to a new branch")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: gew push [--new-branch BRANCH]")
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if mergeState != nil {
		return errors.New("a merge is in progress; continue or abort it before pushing")
	}
	if len(state.Queue) == 0 {
		fmt.Fprintln(a.out, "Everything up to date. No local commits to push.")
		return nil
	}
	c, err := clientForWorkspace(state)
	if err != nil {
		return err
	}
	remoteCommit, err := c.branchCommit(state.Owner, state.Repository, state.Branch)
	if err != nil {
		if isAPINotFound(err) && state.BaseCommit == "" {
			remoteCommit = ""
		} else {
			return err
		}
	}
	remoteFiles := make(map[string]string)
	pushed := 0
	if remoteCommit != state.BaseCommit {
		reconciled, reconciledFiles, reconcileErr := reconcileAppliedCommit(c, root, &state, state.Branch, remoteCommit)
		if reconcileErr != nil {
			return reconcileErr
		}
		if !reconciled {
			return fmt.Errorf("remote branch advanced from %.12s to %.12s; run 'gew pull' before pushing", state.BaseCommit, remoteCommit)
		}
		remoteFiles = reconciledFiles
		pushed++
		fmt.Fprintf(a.out, "Reconciled already-applied commit at %.12s after an ambiguous prior push.\n", remoteCommit)
	} else if remoteCommit != "" {
		remoteFiles, err = c.tree(state.Owner, state.Repository, remoteCommit)
		if err != nil {
			return err
		}
	}
	targetBranch := state.Branch
	if *newBranch != "" {
		targetBranch = *newBranch
	}
	for len(state.Queue) > 0 {
		commitID := state.Queue[0]
		commit, err := loadLocalCommit(root, commitID)
		if err != nil {
			return err
		}
		operations, err := operationsFromCommit(root, commit, remoteFiles)
		if err != nil {
			return err
		}
		if len(operations) > 0 {
			payload := changeFilesRequest{Branch: state.Branch, Message: commit.Message, Files: operations}
			if pushed == 0 && *newBranch != "" {
				payload.NewBranch = targetBranch
			}
			if pushed > 0 || (*newBranch != "" && pushed == 0) {
				if pushed > 0 {
					payload.Branch = targetBranch
				}
			}
			var response json.RawMessage
			err = c.doJSON(http.MethodPost, repoAPIPath(state.Owner, state.Repository)+"/contents", payload, &response)
			if err != nil {
				var apiErr *apiError
				if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusMethodNotAllowed) {
					return fmt.Errorf("%w; this Gitea version may not support atomic multi-file changes", err)
				}
				return err
			}
		}
		newRemoteCommit, err := c.branchCommit(state.Owner, state.Repository, targetBranch)
		if err != nil {
			return fmt.Errorf("commit %.12s may have been submitted, but refreshing branch state failed: %w", commit.ID, err)
		}
		remoteFiles, err = c.tree(state.Owner, state.Repository, newRemoteCommit)
		if err != nil {
			return fmt.Errorf("commit %.12s was submitted, but refreshing file state failed: %w", commit.ID, err)
		}
		now := time.Now().UTC()
		commit.RemoteSHA = newRemoteCommit
		commit.PushedAt = &now
		if err := saveLocalCommit(root, commit); err != nil {
			return err
		}
		state.Queue = state.Queue[1:]
		state.Branch = targetBranch
		state.BaseCommit = newRemoteCommit
		if err := saveState(root, state); err != nil {
			return err
		}
		pushed++
		fmt.Fprintf(a.out, "Pushed %.12s -> %.12s  %s\n", commit.ID, newRemoteCommit, firstLine(commit.Message))
	}
	for filePath, metadata := range state.Files {
		metadata.BlobSHA = remoteFiles[filePath]
		state.Files[filePath] = metadata
	}
	if err := saveState(root, state); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Pushed %d commit(s) to %s.\n", pushed, targetBranch)
	return nil
}

func operationsFromCommit(root string, commit localCommit, remoteFiles map[string]string) ([]changeOperation, error) {
	operations := make([]changeOperation, 0, len(commit.Changes))
	for _, item := range commit.Changes {
		remoteSHA, exists := remoteFiles[item.Path]
		switch item.Kind {
		case "deleted":
			if !exists {
				continue
			}
			operations = append(operations, changeOperation{Operation: "delete", Path: item.Path, SHA: remoteSHA})
		case "created", "modified":
			content, err := os.ReadFile(objectPath(root, item.Object))
			if err != nil {
				return nil, fmt.Errorf("read staged object for %s: %w", item.Path, err)
			}
			operation := "create"
			if exists {
				operation = "update"
			}
			operations = append(operations, changeOperation{
				Operation: operation, Path: item.Path, SHA: remoteSHA,
				Content: base64.StdEncoding.EncodeToString(content),
			})
		default:
			return nil, fmt.Errorf("unsupported local commit change kind %q", item.Kind)
		}
	}
	return operations, nil
}

func reconcileAppliedCommit(c *client, root string, state *workspaceState, branch, remoteHead string) (bool, map[string]string, error) {
	if len(state.Queue) == 0 {
		return false, nil, nil
	}
	commit, err := loadLocalCommit(root, state.Queue[0])
	if err != nil {
		return false, nil, err
	}
	details, err := c.commit(state.Owner, state.Repository, remoteHead)
	if err != nil {
		return false, nil, err
	}
	if strings.TrimSpace(details.Commit.Message) != strings.TrimSpace(commit.Message) {
		return false, nil, nil
	}
	if state.BaseCommit == "" {
		if len(details.Parents) != 0 {
			return false, nil, nil
		}
	} else if len(details.Parents) != 1 || details.Parents[0].SHA != state.BaseCommit {
		return false, nil, nil
	}
	expectedPaths := make(map[string]struct{}, len(commit.Changes))
	for _, item := range commit.Changes {
		expectedPaths[item.Path] = struct{}{}
	}
	if len(details.Files) != len(expectedPaths) {
		return false, nil, nil
	}
	for _, file := range details.Files {
		if _, exists := expectedPaths[file.Filename]; !exists {
			return false, nil, nil
		}
	}
	remoteFiles, err := c.tree(state.Owner, state.Repository, remoteHead)
	if err != nil {
		return false, nil, err
	}
	for _, item := range commit.Changes {
		remoteBlob, exists := remoteFiles[item.Path]
		if item.Kind == "deleted" {
			if exists {
				return false, nil, nil
			}
			continue
		}
		if !exists {
			return false, nil, nil
		}
		remoteContent, err := c.blob(state.Owner, state.Repository, remoteBlob)
		if err != nil {
			return false, nil, err
		}
		localContent, err := os.ReadFile(objectPath(root, item.Object))
		if err != nil {
			return false, nil, err
		}
		if !bytes.Equal(remoteContent, localContent) {
			return false, nil, nil
		}
	}
	now := time.Now().UTC()
	commit.RemoteSHA = remoteHead
	commit.PushedAt = &now
	if err := saveLocalCommit(root, commit); err != nil {
		return false, nil, err
	}
	state.Queue = state.Queue[1:]
	state.Branch = branch
	state.BaseCommit = remoteHead
	if len(state.Queue) == 0 {
		for filePath, metadata := range state.Files {
			metadata.BlobSHA = remoteFiles[filePath]
			state.Files[filePath] = metadata
		}
	}
	if err := saveState(root, *state); err != nil {
		return false, nil, err
	}
	return true, remoteFiles, nil
}

func normalizeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Gitea URL %q; include http:// or https://", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("Gitea URL must use http or https")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func configFilePath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("GEW_CONFIG")); override != "" {
		return filepath.Abs(override)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(dir, "gew", "config.json"), nil
}

func readConfig() (config, string, error) {
	configPath, err := configFilePath()
	if err != nil {
		return config{}, "", err
	}
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return config{Profiles: make(map[string]profile)}, configPath, nil
	}
	if err != nil {
		return config{}, "", fmt.Errorf("read config: %w", err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config{}, "", fmt.Errorf("parse config: %w", err)
	}
	return cfg, configPath, nil
}

func writeConfig(configPath string, cfg config) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(configPath, append(data, '\n'), 0o600)
}

func profileFromConfig() (profile, error) {
	serverEnv := strings.TrimSpace(os.Getenv("GEW_SERVER"))
	tokenEnv := strings.TrimSpace(os.Getenv("GEW_TOKEN"))
	if serverEnv != "" || tokenEnv != "" {
		if serverEnv == "" || tokenEnv == "" {
			return profile{}, errors.New("GEW_SERVER and GEW_TOKEN must be set together")
		}
		server, err := normalizeServerURL(serverEnv)
		if err != nil {
			return profile{}, err
		}
		return profile{URL: server, Token: tokenEnv}, nil
	}
	cfg, _, err := readConfig()
	if err != nil {
		return profile{}, err
	}
	name := strings.TrimSpace(os.Getenv("GEW_PROFILE"))
	if name == "" {
		name = cfg.Current
	}
	selected, ok := cfg.Profiles[name]
	if !ok || selected.URL == "" || selected.Token == "" {
		return profile{}, errors.New("not logged in; run 'gew login https://gitea.example.com'")
	}
	return selected, nil
}

func clientFromConfig() (*client, error) {
	p, err := profileFromConfig()
	if err != nil {
		return nil, err
	}
	return newClient(p), nil
}

func clientForWorkspace(state workspaceState) (*client, error) {
	p, err := profileFromConfig()
	if err != nil {
		return nil, err
	}
	if strings.TrimRight(p.URL, "/") != strings.TrimRight(state.Server, "/") {
		return nil, fmt.Errorf("active profile points to %s, but this workspace belongs to %s", p.URL, state.Server)
	}
	return newClient(p), nil
}

func newClient(p profile) *client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if p.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicitly requested by user profile
	}
	return &client{
		baseURL: strings.TrimRight(p.URL, "/"), token: p.Token,
		http: &http.Client{Transport: transport, Timeout: 90 * time.Second},
	}
}

func (c *client) doJSON(method, apiPath string, requestBody any, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.baseURL+apiPath, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("User-Agent", "gew/"+toolVersion)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{Status: resp.StatusCode, Method: method, URL: apiPath, Body: string(data)}
	}
	if responseBody != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, responseBody); err != nil {
			return fmt.Errorf("decode response from %s: %w", apiPath, err)
		}
	}
	return nil
}

func (c *client) getJSON(apiPath string, responseBody any) error {
	return c.doJSON(http.MethodGet, apiPath, nil, responseBody)
}

func (c *client) download(apiPath string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+apiPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("User-Agent", "gew/"+toolVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, &apiError{Status: resp.StatusCode, Method: http.MethodGet, URL: apiPath, Body: string(data)}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (c *client) branchCommit(owner, repo, branch string) (string, error) {
	var response branchResponse
	endpoint := repoAPIPath(owner, repo) + "/branches/" + url.PathEscape(branch)
	if err := c.getJSON(endpoint, &response); err != nil {
		return "", err
	}
	commit := response.commitSHA()
	if commit == "" {
		return "", fmt.Errorf("Gitea returned no commit SHA for branch %q", branch)
	}
	return commit, nil
}

func (c *client) tree(owner, repo, commit string) (map[string]string, error) {
	result := make(map[string]string)
	for pageNumber := 1; ; pageNumber++ {
		endpoint := fmt.Sprintf("%s/git/trees/%s?recursive=true&page=%d&per_page=1000", repoAPIPath(owner, repo), url.PathEscape(commit), pageNumber)
		var response treeResponse
		if err := c.getJSON(endpoint, &response); err != nil {
			return nil, err
		}
		for _, entry := range response.Tree {
			if entry.Type == "blob" {
				result[path.Clean(entry.Path)] = entry.SHA
			}
		}
		if !response.Truncated || len(response.Tree) == 0 {
			break
		}
	}
	return result, nil
}

func (c *client) blob(owner, repo, sha string) ([]byte, error) {
	var response blobResponse
	endpoint := repoAPIPath(owner, repo) + "/git/blobs/" + url.PathEscape(sha)
	if err := c.getJSON(endpoint, &response); err != nil {
		return nil, err
	}
	if response.Encoding != "" && response.Encoding != "base64" {
		return nil, fmt.Errorf("unsupported blob encoding %q", response.Encoding)
	}
	content := strings.ReplaceAll(response.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, fmt.Errorf("decode blob %s: %w", sha, err)
	}
	return decoded, nil
}

func (c *client) commit(owner, repo, sha string) (commitDetails, error) {
	var response commitDetails
	endpoint := repoAPIPath(owner, repo) + "/git/commits/" + url.PathEscape(sha) + "?stat=false&verification=false&files=true"
	if err := c.getJSON(endpoint, &response); err != nil {
		return commitDetails{}, err
	}
	return response, nil
}

func repoAPIPath(owner, repo string) string {
	return "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func archiveAPIPath(owner, repo, branch string) string {
	return repoAPIPath(owner, repo) + "/archive/" + url.PathEscape(branch) + ".zip"
}

func parseRepository(value string) (string, string, error) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("repository must be written as OWNER/REPO")
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

func ensureEmptyDestination(destination string) error {
	entries, err := os.ReadDir(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("destination %s is not empty", destination)
	}
	return nil
}

func extractArchive(data []byte, destination string) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open repository archive: %w", err)
	}
	prefix := commonArchiveRoot(reader.File)
	destinationClean := filepath.Clean(destination)
	for _, archived := range reader.File {
		name := strings.TrimPrefix(strings.ReplaceAll(archived.Name, "\\", "/"), prefix)
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			continue
		}
		cleanName := path.Clean(name)
		if cleanName == "." || strings.HasPrefix(cleanName, "../") || path.IsAbs(cleanName) {
			return fmt.Errorf("unsafe path %q in repository archive", archived.Name)
		}
		if archived.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("repository contains symlink %q; symlinks are not supported by this MVP", cleanName)
		}
		target := filepath.Join(destinationClean, filepath.FromSlash(cleanName))
		if target != destinationClean && !strings.HasPrefix(target, destinationClean+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path %q in repository archive", archived.Name)
		}
		if archived.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source, err := archived.Open()
		if err != nil {
			return err
		}
		mode := archived.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		targetFile, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(targetFile, source)
		closeErr := targetFile.Close()
		sourceErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if sourceErr != nil {
			return sourceErr
		}
	}
	return nil
}

func commonArchiveRoot(files []*zip.File) string {
	root := ""
	for _, file := range files {
		name := strings.TrimPrefix(strings.ReplaceAll(file.Name, "\\", "/"), "/")
		if name == "" {
			continue
		}
		first, _, found := strings.Cut(name, "/")
		if !found {
			return ""
		}
		if root == "" {
			root = first
		} else if root != first {
			return ""
		}
	}
	if root == "" {
		return ""
	}
	return root + "/"
}

func scanWorkspace(root string) (map[string]fileState, error) {
	files := make(map[string]fileState)
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		first := strings.SplitN(relative, "/", 2)[0]
		if first == ".gew" || first == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relative == ".DS_Store" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %s is not supported", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file %s is not supported", relative)
		}
		hash, err := hashFile(filePath)
		if err != nil {
			return err
		}
		files[relative] = fileState{Hash: hash, Mode: uint32(info.Mode().Perm())}
		return nil
	})
	return files, err
}

func hashFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func mergeFileMetadata(local map[string]fileState, remote map[string]string) map[string]fileState {
	merged := make(map[string]fileState, len(local))
	for filePath, metadata := range local {
		metadata.BlobSHA = remote[filePath]
		merged[filePath] = metadata
	}
	return merged
}

func saveState(root string, state workspaceState) error {
	state.Version = stateVersion
	directory := filepath.Join(root, ".gew")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(directory, "state.json"), append(data, '\n'), 0o600)
}

func atomicWrite(destination string, data []byte, mode fs.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".gew-write-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, destination)
}

func findWorkspace() (string, workspaceState, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", workspaceState{}, err
	}
	for {
		statePath := filepath.Join(current, ".gew", "state.json")
		data, readErr := os.ReadFile(statePath)
		if readErr == nil {
			var state workspaceState
			if err := json.Unmarshal(data, &state); err != nil {
				return "", workspaceState{}, fmt.Errorf("parse %s: %w", statePath, err)
			}
			if state.Version != 1 && state.Version != stateVersion {
				return "", workspaceState{}, fmt.Errorf("unsupported workspace state version %d", state.Version)
			}
			if state.Version == 1 {
				state.Version = stateVersion
			}
			if state.Files == nil {
				state.Files = make(map[string]fileState)
			}
			return current, state, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", workspaceState{}, readErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", workspaceState{}, errors.New("not inside a gew workspace")
		}
		current = parent
	}
}

func workspaceChanges(root string, state workspaceState) ([]change, error) {
	current, err := scanWorkspace(root)
	if err != nil {
		return nil, err
	}
	changes := make([]change, 0)
	for filePath, metadata := range current {
		old, exists := state.Files[filePath]
		if !exists {
			changes = append(changes, change{Kind: "created", Path: filePath})
		} else if old.Hash != metadata.Hash {
			changes = append(changes, change{Kind: "modified", Path: filePath})
		}
	}
	for filePath := range state.Files {
		if _, exists := current[filePath]; !exists {
			changes = append(changes, change{Kind: "deleted", Path: filePath})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Path < changes[j].Path
	})
	return changes, nil
}

func operationsForChanges(root string, state workspaceState, changes []change) ([]changeOperation, error) {
	operations := make([]changeOperation, 0, len(changes))
	for _, item := range changes {
		operation := changeOperation{Path: item.Path}
		switch item.Kind {
		case "created":
			operation.Operation = "create"
		case "modified":
			operation.Operation = "update"
			operation.SHA = state.Files[item.Path].BlobSHA
			if operation.SHA == "" {
				return nil, fmt.Errorf("missing remote blob SHA for %s; pull a fresh snapshot", item.Path)
			}
		case "deleted":
			operation.Operation = "delete"
			operation.SHA = state.Files[item.Path].BlobSHA
			if operation.SHA == "" {
				return nil, fmt.Errorf("missing remote blob SHA for %s; pull a fresh snapshot", item.Path)
			}
		default:
			return nil, fmt.Errorf("unsupported change type %q", item.Kind)
		}
		if item.Kind != "deleted" {
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(item.Path)))
			if err != nil {
				return nil, err
			}
			operation.Content = base64.StdEncoding.EncodeToString(content)
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func replaceTrackedFiles(root, stage string, oldFiles map[string]fileState) error {
	for filePath := range oldFiles {
		target := filepath.Join(root, filepath.FromSlash(filePath))
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove old file %s: %w", filePath, err)
		}
	}
	return filepath.WalkDir(stage, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(stage, source)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(root, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		defer input.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}
