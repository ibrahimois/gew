package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gew/internal/version"
)

const stateVersion = 4

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
	Backend    WorkspaceBackendKind `json:"backend,omitempty"`
	Provider   ForgeKind            `json:"provider"`
	Remote     RepositoryRef        `json:"remote"`
	Server     string               `json:"server,omitempty"`
	Owner      string               `json:"owner,omitempty"`
	Repository string               `json:"repository,omitempty"`
	Branch     string               `json:"branch"`
	BaseCommit string               `json:"base_commit"`
	Files      map[string]fileState `json:"files"`
	Queue      []string             `json:"queue,omitempty"`
	History    []string             `json:"history,omitempty"`
	LocalHead  string               `json:"local_head,omitempty"`
	Hybrid     *hybridState         `json:"hybrid,omitempty"`
}

type change struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type app struct {
	out    io.Writer
	errOut io.Writer
}

func Run(args []string, output, errorOutput io.Writer) error {
	return (app{out: output, errOut: errorOutput}).run(args)
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
		fmt.Fprintf(a.out, "gew %s\n", version.Current)
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
	case "migrate":
		return a.migrate(args[1:])
	case "push":
		return a.push(args[1:])
	default:
		return fmt.Errorf("unknown command %q; run 'gew help'", args[0])
	}
}

func (a app) usage() {
	fmt.Fprint(a.out, `gew - a small REST-only workspace client for hosted Git forges

Usage:
  gew login [--provider PROVIDER] [--name NAME] [--token TOKEN] [--auth-kind KIND] [--username USER] [--insecure] URL
  gew doctor
  gew clone [--branch BRANCH] [--backend gew|git] OWNER/REPO [DIRECTORY]
  gew status [--json]
  gew add [-A|--all] PATH...
  gew reset [PATH...]
  gew diff [--staged]
  gew commit -m MESSAGE [--author-name NAME --author-email EMAIL]
  gew log [--oneline]
  gew pull [--ff-only]
  gew merge (--abort | --continue [-m MESSAGE])
  gew migrate --to git [--dry-run] [--author-name NAME --author-email EMAIL]
  gew push [--new-branch BRANCH]
  gew version

Environment:
	GEW_SERVER   Override the configured provider URL
  GEW_TOKEN    Override the configured access token
	GEW_PROVIDER Override the configured provider kind
	GEW_AUTH_KIND Override the configured authentication kind
	GEW_USERNAME Override the configured authentication username
  GEW_PROFILE  Select a saved login profile
  GEW_CONFIG   Override the config file path
  GEW_AUTHOR_NAME  Local Git commit author name for the hybrid backend
  GEW_AUTHOR_EMAIL Local Git commit author email for the hybrid backend

gew uses Git-like staging and local queued commits, backed by forge REST APIs.
The default gew backend uses .gew only. The opt-in git backend uses a local
.git object database while all remote access still goes through forge REST APIs.
`)
}

func (a app) login(args []string) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	name := flags.String("name", "default", "profile name")
	providerFlag := flags.String("provider", string(ForgeGitea), "provider kind")
	tokenFlag := flags.String("token", "", "access token (prompted if omitted)")
	authKindFlag := flags.String("auth-kind", "", "authentication kind")
	username := flags.String("username", "", "authentication username")
	insecure := flags.Bool("insecure", false, "skip TLS verification")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: gew login [--provider PROVIDER] [--name NAME] [--token TOKEN] [--auth-kind KIND] [--username USER] [--insecure] URL")
	}
	kind, err := normalizeForgeKind(*providerFlag)
	if err != nil {
		return err
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

	authKind := AuthKind(strings.TrimSpace(*authKindFlag))
	if authKind == "" {
		authKind = defaultAuthKind(kind)
	}
	p := profile{Provider: kind, URL: server, Token: token, AuthKind: authKind, Username: strings.TrimSpace(*username), Insecure: *insecure}
	remote, err := forgeFromProfile(p)
	if err != nil {
		return err
	}
	if err := remote.Probe(context.Background()); err != nil {
		return fmt.Errorf("connection test failed: %w", err)
	}

	cfg, cfgPath, err := readConfig()
	if err != nil {
		return err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]profile)
	}
	cfg.Current = *name
	cfg.Profiles[*name] = p
	if err := writeConfig(cfgPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Saved profile %q for %s (%s).\n", *name, server, kind)
	if *insecure {
		fmt.Fprintln(a.errOut, "Warning: TLS certificate verification is disabled for this profile.")
	}
	return nil
}

func (a app) doctor(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: gew doctor")
	}
	remote, p, err := forgeFromConfig()
	if err != nil {
		return err
	}
	if err := remote.Probe(context.Background()); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Connected to %s\nProvider: %s\nAuthentication: credentials accepted\n", p.URL, remote.Kind())
	return nil
}

func (a app) clone(args []string) error {
	flags := flag.NewFlagSet("clone", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	branchFlag := flags.String("branch", "", "branch to download")
	backendFlag := flags.String("backend", string(WorkspaceGew), "local workspace backend (gew or git)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		return errors.New("usage: gew clone [--branch BRANCH] [--backend gew|git] OWNER/REPO [DIRECTORY]")
	}
	backend, err := normalizeWorkspaceBackend(WorkspaceBackendKind(*backendFlag))
	if err != nil {
		return err
	}
	remote, _, err := forgeFromConfig()
	if err != nil {
		return err
	}
	repositoryRef, repoInfo, err := remote.ResolveRepository(context.Background(), flags.Arg(0))
	if err != nil {
		return err
	}
	destination := repositoryRef.Name
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
			Version: stateVersion, Provider: remote.Kind(), Remote: repositoryRef,
			Backend: backend, Branch: branch, BaseCommit: "", Files: make(map[string]fileState),
		}
		state.syncLegacyIdentity()
		if backend == WorkspaceGit {
			if err := initializeGitWorkspace(absDestination, &state, true); err != nil {
				_ = os.RemoveAll(filepath.Join(absDestination, ".git"))
				return err
			}
		}
		if err := saveState(absDestination, state); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Prepared empty %s workspace on %s in %s\n", repositoryRef.DisplayName(), branch, absDestination)
		return nil
	}
	commit, err := remote.Head(context.Background(), repositoryRef, branch)
	if err != nil {
		return err
	}
	archive, err := forgeSnapshot(context.Background(), remote, repositoryRef, commit)
	if err != nil {
		return err
	}
	remoteFiles, err := remote.Tree(context.Background(), repositoryRef, commit)
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
		Version: stateVersion, Provider: remote.Kind(), Remote: repositoryRef,
		Backend: backend, Branch: branch, BaseCommit: commit, Files: mergeFileMetadata(localFiles, remoteBlobIDs(remoteFiles)),
	}
	state.syncLegacyIdentity()
	if backend == WorkspaceGit {
		if err := initializeGitWorkspace(absDestination, &state, false); err != nil {
			_ = os.RemoveAll(filepath.Join(absDestination, ".git"))
			return err
		}
	}
	if err := saveState(absDestination, state); err != nil {
		return err
	}
	if err := ensureBaselineObjects(absDestination, state.Files); err != nil {
		return err
	}
	if backend == WorkspaceGit && state.Hybrid.LastLocalOID != "" {
		paths := make([]string, 0, len(state.Files))
		for filePath := range state.Files {
			paths = append(paths, filePath)
		}
		sort.Strings(paths)
		if err := saveGitExportReceipt(absDestination, gitExportReceipt{
			Version: exportReceiptVersion, LocalOID: state.Hybrid.LastLocalOID, ProviderID: commit,
			ProviderBase: commit, Message: "imported remote snapshot", Paths: paths,
		}); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.out, "Downloaded %s (%s at %.12s) into %s\n", repositoryRef.DisplayName(), branch, commit, absDestination)
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
	if state.Backend == WorkspaceGit {
		return a.gitStatus(root, state, *asJSON)
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
		}{state.Remote.DisplayName(), state.Branch, state.BaseCommit, len(state.Queue), stagedChanges, unstagedChanges, mergeState != nil, nil}
		if mergeState != nil {
			payload.Conflicts = mergeState.Conflicts
		}
		encoder := json.NewEncoder(a.out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	}
	fmt.Fprintf(a.out, "On branch %s\nRepository: %s (%s)\n", state.Branch, state.Remote.DisplayName(), state.Provider)
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
	if state.Backend == WorkspaceGit {
		remote, err := forgeForWorkspace(state)
		if err != nil {
			return err
		}
		return a.gitPull(root, state, remote, *ffOnly)
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
	remote, err := forgeForWorkspace(state)
	if err != nil {
		return err
	}
	commit, err := remote.Head(context.Background(), state.Remote, state.Branch)
	if err != nil {
		if isRemoteNotFound(err) && state.BaseCommit == "" {
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
		return a.mergeRemote(root, state, remote, commit, len(changes) != 0)
	}
	archive, err := forgeSnapshot(context.Background(), remote, state.Remote, commit)
	if err != nil {
		return err
	}
	remoteFiles, err := remote.Tree(context.Background(), state.Remote, commit)
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
	state.Files = mergeFileMetadata(localFiles, remoteBlobIDs(remoteFiles))
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
	if state.Backend == WorkspaceGit {
		return a.gitPush(root, state, *newBranch)
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
	remote, err := forgeForWorkspace(state)
	if err != nil {
		return err
	}
	writer, err := forgeWriter(remote, *newBranch != "")
	if err != nil {
		return err
	}
	remoteCommit, err := remote.Head(context.Background(), state.Remote, state.Branch)
	if err != nil {
		if isRemoteNotFound(err) && state.BaseCommit == "" {
			remoteCommit = ""
		} else {
			return err
		}
	}
	remoteFiles := make(map[string]RemoteFile)
	pushed := 0
	if remoteCommit != state.BaseCommit {
		reconciled, reconciledFiles, reconcileErr := reconcileAppliedCommit(remote, writer, root, &state, state.Branch, remoteCommit)
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
		remoteFiles, err = remote.Tree(context.Background(), state.Remote, remoteCommit)
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
			request := ApplyCommitRequest{
				Repository: state.Remote, Branch: state.Branch, ExpectedHead: remoteCommit,
				Message: commit.Message, Changes: operations,
			}
			if pushed == 0 && *newBranch != "" {
				request.NewBranch = targetBranch
			}
			if pushed > 0 {
				request.Branch = targetBranch
			}
			result, applyErr := writer.ApplyCommit(context.Background(), request)
			if applyErr != nil {
				if errors.Is(applyErr, ErrStaleHead) {
					return fmt.Errorf("remote branch advanced; run 'gew pull' before pushing: %w", applyErr)
				}
				return applyErr
			}
			remoteCommit = result.CommitID
		}
		newRemoteCommit, err := remote.Head(context.Background(), state.Remote, targetBranch)
		if err != nil {
			return fmt.Errorf("commit %.12s may have been submitted, but refreshing branch state failed: %w", commit.ID, err)
		}
		if remoteCommit != "" && newRemoteCommit != remoteCommit {
			return fmt.Errorf("provider reported commit %.12s, but branch %s points to %.12s", remoteCommit, targetBranch, newRemoteCommit)
		}
		remoteFiles, err = remote.Tree(context.Background(), state.Remote, newRemoteCommit)
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
		remoteCommit = newRemoteCommit
		if err := saveState(root, state); err != nil {
			return err
		}
		pushed++
		fmt.Fprintf(a.out, "Pushed %.12s -> %.12s  %s\n", commit.ID, newRemoteCommit, firstLine(commit.Message))
	}
	for filePath, metadata := range state.Files {
		metadata.BlobSHA = remoteFiles[filePath].BlobID
		state.Files[filePath] = metadata
	}
	if err := saveState(root, state); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Pushed %d commit(s) to %s.\n", pushed, targetBranch)
	return nil
}

func operationsFromCommit(root string, commit localCommit, remoteFiles map[string]RemoteFile) ([]RemoteChange, error) {
	operations := make([]RemoteChange, 0, len(commit.Changes))
	for _, item := range commit.Changes {
		remoteFile, exists := remoteFiles[item.Path]
		switch item.Kind {
		case "deleted":
			if !exists {
				continue
			}
			operations = append(operations, RemoteChange{Operation: "delete", Path: item.Path, BlobID: remoteFile.BlobID, LastCommitID: remoteFile.LastCommitID, Mode: item.Mode})
		case "created", "modified":
			content, err := os.ReadFile(objectPath(root, item.Object))
			if err != nil {
				return nil, fmt.Errorf("read staged object for %s: %w", item.Path, err)
			}
			operation := "create"
			if exists {
				operation = "update"
			}
			operations = append(operations, RemoteChange{
				Operation: operation, Path: item.Path, BlobID: remoteFile.BlobID,
				LastCommitID: remoteFile.LastCommitID, Content: content, Mode: item.Mode,
			})
		default:
			return nil, fmt.Errorf("unsupported local commit change kind %q", item.Kind)
		}
	}
	return operations, nil
}

func reconcileAppliedCommit(remote Forge, inspector ForgeCommitInspector, root string, state *workspaceState, branch, remoteHead string) (bool, map[string]RemoteFile, error) {
	if len(state.Queue) == 0 {
		return false, nil, nil
	}
	commit, err := loadLocalCommit(root, state.Queue[0])
	if err != nil {
		return false, nil, err
	}
	details, err := inspector.CommitDetails(context.Background(), state.Remote, remoteHead)
	if err != nil {
		return false, nil, err
	}
	if strings.TrimSpace(details.Message) != strings.TrimSpace(commit.Message) {
		return false, nil, nil
	}
	if state.BaseCommit == "" {
		if len(details.ParentIDs) != 0 {
			return false, nil, nil
		}
	} else if len(details.ParentIDs) != 1 || details.ParentIDs[0] != state.BaseCommit {
		return false, nil, nil
	}
	expectedPaths := make(map[string]struct{}, len(commit.Changes))
	for _, item := range commit.Changes {
		expectedPaths[item.Path] = struct{}{}
	}
	if len(details.Paths) != len(expectedPaths) {
		return false, nil, nil
	}
	for _, filePath := range details.Paths {
		if _, exists := expectedPaths[filePath]; !exists {
			return false, nil, nil
		}
	}
	remoteFiles, err := remote.Tree(context.Background(), state.Remote, remoteHead)
	if err != nil {
		return false, nil, err
	}
	for _, item := range commit.Changes {
		remoteFile, exists := remoteFiles[item.Path]
		if item.Kind == "deleted" {
			if exists {
				return false, nil, nil
			}
			continue
		}
		if !exists {
			return false, nil, nil
		}
		remoteContent, err := remote.Blob(context.Background(), state.Remote, remoteFile)
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
			metadata.BlobSHA = remoteFiles[filePath].BlobID
			state.Files[filePath] = metadata
		}
	}
	if err := saveState(root, *state); err != nil {
		return false, nil, err
	}
	return true, remoteFiles, nil
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
	for name, saved := range cfg.Profiles {
		if saved.Provider == "" {
			saved.Provider = ForgeGitea
		}
		if saved.AuthKind == "" {
			saved.AuthKind = defaultAuthKind(saved.Provider)
		}
		cfg.Profiles[name] = saved
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
		kind, err := normalizeForgeKind(os.Getenv("GEW_PROVIDER"))
		if err != nil {
			return profile{}, err
		}
		authKind := AuthKind(strings.TrimSpace(os.Getenv("GEW_AUTH_KIND")))
		if authKind == "" {
			authKind = defaultAuthKind(kind)
		}
		return profile{Provider: kind, URL: server, Token: tokenEnv, AuthKind: authKind, Username: strings.TrimSpace(os.Getenv("GEW_USERNAME"))}, nil
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
		return profile{}, errors.New("not logged in; run 'gew login --provider PROVIDER URL'")
	}
	if selected.Provider == "" {
		selected.Provider = ForgeGitea
	}
	if selected.AuthKind == "" {
		selected.AuthKind = defaultAuthKind(selected.Provider)
	}
	return selected, nil
}

func forgeFromConfig() (Forge, profile, error) {
	p, err := profileFromConfig()
	if err != nil {
		return nil, profile{}, err
	}
	remote, err := forgeFromProfile(p)
	return remote, p, err
}

func forgeForWorkspace(state workspaceState) (Forge, error) {
	state.syncLegacyIdentity()
	p, err := profileFromConfig()
	if err != nil {
		return nil, err
	}
	if p.Provider == "" {
		p.Provider = ForgeGitea
	}
	if p.Provider != state.Provider {
		return nil, fmt.Errorf("active profile uses %s, but this workspace belongs to %s", p.Provider, state.Provider)
	}
	if strings.TrimRight(p.URL, "/") != strings.TrimRight(state.Remote.Server, "/") {
		return nil, fmt.Errorf("active profile points to %s, but this workspace belongs to %s", p.URL, state.Remote.Server)
	}
	return forgeFromProfile(p)
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

func remoteBlobIDs(files map[string]RemoteFile) map[string]string {
	result := make(map[string]string, len(files))
	for filePath, file := range files {
		result[filePath] = file.BlobID
	}
	return result
}

func (state *workspaceState) syncLegacyIdentity() {
	if state.Provider == "" {
		state.Provider = ForgeGitea
	}
	if state.Remote.Forge == "" {
		state.Remote = RepositoryRef{
			Forge: state.Provider, Server: state.Server, Namespace: state.Owner,
			Name: state.Repository,
		}
	}
	if state.Remote.Server == "" {
		state.Remote.Server = state.Server
	}
	if state.Remote.Forge == "" {
		state.Remote.Forge = state.Provider
	}
	if state.Remote.Canonical == "" && state.Remote.Namespace != "" && state.Remote.Name != "" {
		state.Remote.Canonical = state.Remote.Namespace + "/" + state.Remote.Name
	}
	state.Provider = state.Remote.Forge
	state.Server = state.Remote.Server
	state.Owner = state.Remote.Namespace
	state.Repository = state.Remote.Name
}

func saveState(root string, state workspaceState) error {
	state.Version = stateVersion
	backend, err := normalizeWorkspaceBackend(state.Backend)
	if err != nil {
		return err
	}
	state.Backend = backend
	state.syncLegacyIdentity()
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
			if state.Version < 1 || state.Version > stateVersion {
				return "", workspaceState{}, fmt.Errorf("unsupported workspace state version %d", state.Version)
			}
			backend, err := normalizeWorkspaceBackend(state.Backend)
			if err != nil {
				return "", workspaceState{}, err
			}
			state.Backend = backend
			state.syncLegacyIdentity()
			if state.Files == nil {
				state.Files = make(map[string]fileState)
			}
			if state.Backend == WorkspaceGit {
				if err := validateHybridState(current, &state); err != nil {
					return "", workspaceState{}, err
				}
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
