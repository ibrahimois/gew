package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	maxReleaseNotes = int64(1 << 20)
	maxReleaseAsset = int64(1 << 30)
)

type localReleaseAsset struct {
	Path   string
	Name   string
	Size   int64
	SHA256 string
}

func (a app) releaseCreateOperation(ctx context.Context, options releaseCreateOptions) error {
	if err := validateReleaseTag(options.Tag); err != nil {
		return err
	}
	if strings.TrimSpace(options.Title) == "" {
		return errors.New("release title must not be blank")
	}
	notes, assets, err := validateReleaseFiles(options.NotesFile, options.Assets)
	if err != nil {
		return err
	}
	root, state, err := findWorkspace()
	if err != nil {
		return err
	}
	if err := requireCleanReleaseWorkspace(root, state); err != nil {
		return err
	}
	remote, err := forgeForWorkspace(state)
	if err != nil {
		return err
	}
	publisher, err := forgeReleasePublisher(remote)
	if err != nil {
		return fmt.Errorf("%s releases: %w", state.Provider, err)
	}
	remoteHead, err := remote.Head(ctx, state.Remote, state.Branch)
	if err != nil {
		return err
	}
	if remoteHead != state.BaseCommit {
		return fmt.Errorf("remote head %.12s does not match synchronized workspace %.12s; pull or push before releasing", remoteHead, state.BaseCommit)
	}

	want := CreateReleaseRequest{
		Repository: state.Remote, TagName: options.Tag, TargetCommit: state.BaseCommit,
		Title: options.Title, Notes: string(notes), Draft: options.Draft, Prerelease: options.Prerelease,
		Latest: !options.Draft && !options.Prerelease,
	}
	release, findErr := publisher.FindReleaseByTag(ctx, state.Remote, options.Tag)
	if findErr == nil {
		if !options.Resume {
			return fmt.Errorf("release %q already exists; use --resume only if it should match exactly", options.Tag)
		}
		if err := verifyReleaseIdentity(release, want); err != nil {
			return err
		}
	} else if !isRemoteNotFound(findErr) {
		return findErr
	} else {
		release, err = publisher.CreateRelease(ctx, want)
		if err != nil {
			observed, reconcileErr := publisher.FindReleaseByTag(ctx, state.Remote, options.Tag)
			if reconcileErr != nil {
				return errors.Join(err, fmt.Errorf("reconcile release create: %w", reconcileErr))
			}
			if identityErr := verifyReleaseIdentity(observed, want); identityErr != nil {
				return errors.Join(err, identityErr)
			}
			release = observed
		}
		if err := verifyReleaseIdentity(release, want); err != nil {
			return err
		}
	}

	uploaded, skipped := 0, 0
	for _, asset := range assets {
		matched, exists, err := findMatchingRemoteAsset(ctx, publisher, state.Remote, release.ID, asset)
		if err != nil {
			return err
		}
		if exists {
			if !matched {
				return fmt.Errorf("release asset %q already exists with different bytes", asset.Name)
			}
			skipped++
			continue
		}
		if err := uploadReleaseAsset(ctx, publisher, state.Remote, release.ID, asset); err != nil {
			matched, exists, reconcileErr := findMatchingRemoteAsset(ctx, publisher, state.Remote, release.ID, asset)
			if reconcileErr != nil {
				return errors.Join(err, fmt.Errorf("reconcile asset %q: %w", asset.Name, reconcileErr))
			}
			if exists {
				if !matched {
					return errors.Join(err, fmt.Errorf("release asset %q appeared with different bytes", asset.Name))
				}
				uploaded++
				continue
			}
			// A remote listing proved that the first mutation did not create the
			// asset, so one fresh upload is safe.
			if retryErr := uploadReleaseAsset(ctx, publisher, state.Remote, release.ID, asset); retryErr != nil {
				matched, exists, finalErr := findMatchingRemoteAsset(ctx, publisher, state.Remote, release.ID, asset)
				if finalErr == nil && exists && matched {
					uploaded++
					continue
				}
				return errors.Join(err, retryErr, finalErr)
			}
		}
		uploaded++
	}
	fmt.Fprintf(a.out, "Published %s (%d uploaded, %d verified existing asset(s))\n%s\n", options.Tag, uploaded, skipped, release.URL)
	return nil
}

func validateReleaseTag(tag string) error {
	if tag == "" || strings.TrimSpace(tag) != tag {
		return errors.New("release tag must not be blank or have surrounding whitespace")
	}
	for _, character := range tag {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return fmt.Errorf("release tag %q contains whitespace or control characters", tag)
		}
	}
	if strings.HasPrefix(tag, "/") || strings.HasSuffix(tag, "/") || strings.Contains(tag, "//") ||
		strings.Contains(tag, "..") || strings.Contains(tag, "@{") || strings.ContainsAny(tag, "~^:?*[\\") {
		return fmt.Errorf("release tag %q is not a safe Git ref name", tag)
	}
	for _, component := range strings.Split(tag, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("release tag %q is not a safe Git ref name", tag)
		}
	}
	return nil
}

func validateReleaseFiles(notesPath string, assetPaths []string) ([]byte, []localReleaseAsset, error) {
	notesInfo, err := os.Lstat(notesPath)
	if err != nil {
		return nil, nil, fmt.Errorf("release notes: %w", err)
	}
	if notesInfo.Mode()&os.ModeSymlink != 0 || !notesInfo.Mode().IsRegular() || notesInfo.Size() > maxReleaseNotes {
		return nil, nil, errors.New("release notes must be a regular non-symlink file no larger than 1 MiB")
	}
	notesFile, err := os.Open(notesPath)
	if err != nil {
		return nil, nil, err
	}
	notes, err := readBounded(notesFile, maxReleaseNotes)
	closeErr := notesFile.Close()
	if err != nil {
		return nil, nil, err
	}
	if closeErr != nil {
		return nil, nil, closeErr
	}
	if len(assetPaths) == 0 {
		return nil, nil, errors.New("at least one --asset is required")
	}
	seen := make(map[string]bool)
	assets := make([]localReleaseAsset, 0, len(assetPaths))
	for _, assetPath := range assetPaths {
		info, err := os.Lstat(assetPath)
		if err != nil {
			return nil, nil, fmt.Errorf("release asset %q: %w", assetPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxReleaseAsset {
			return nil, nil, fmt.Errorf("release asset %q must be a regular non-symlink file no larger than 1 GiB", assetPath)
		}
		name := filepath.Base(assetPath)
		if seen[name] {
			return nil, nil, fmt.Errorf("release asset basename %q is duplicated", name)
		}
		seen[name] = true
		digest, err := hashReleaseFile(assetPath, maxReleaseAsset)
		if err != nil {
			return nil, nil, err
		}
		assets = append(assets, localReleaseAsset{Path: assetPath, Name: name, Size: info.Size(), SHA256: digest})
	}
	return notes, assets, nil
}

func requireCleanReleaseWorkspace(root string, state workspaceState) error {
	mergeState, err := loadMergeState(root)
	if err != nil {
		return err
	}
	if mergeState != nil {
		return errors.New("cannot release while a merge is in progress")
	}
	if state.Backend == WorkspaceGit {
		prepared, err := loadPreparedGitExport(root)
		if err != nil {
			return err
		}
		if prepared != nil {
			return errors.New("cannot release while a Git export is prepared")
		}
		repository, worktree, err := openGitWorkspace(root, state)
		if err != nil {
			return err
		}
		head, index, worktreeFiles, err := gitSnapshots(repository, worktree)
		if err != nil {
			return err
		}
		pending, err := pendingGitCommits(repository, state.Hybrid.TrackingRef)
		if err != nil {
			return err
		}
		if len(pending) != 0 || len(byteSnapshotChanges(head, index)) != 0 || len(byteSnapshotChanges(index, worktreeFiles)) != 0 {
			return errors.New("release requires a clean synchronized workspace with no unpushed commits")
		}
		return nil
	}
	index, err := loadIndex(root)
	if err != nil {
		return err
	}
	changes, err := workspaceChanges(root, state)
	if err != nil {
		return err
	}
	if len(state.Queue) != 0 || len(index.Entries) != 0 || len(changes) != 0 {
		return errors.New("release requires a clean synchronized workspace with no staged, unstaged, or unpushed changes")
	}
	return nil
}

func verifyReleaseIdentity(got RemoteRelease, want CreateReleaseRequest) error {
	if got.TagName != want.TagName || got.TargetCommit != want.TargetCommit || got.Title != want.Title || got.Notes != want.Notes || got.Draft != want.Draft || got.Prerelease != want.Prerelease {
		return fmt.Errorf("existing release %q does not exactly match tag target, title, notes, draft, and prerelease state", want.TagName)
	}
	if got.ID == "" {
		return errors.New("provider returned an empty release ID")
	}
	return nil
}

func uploadReleaseAsset(ctx context.Context, publisher ForgeReleasePublisher, ref RepositoryRef, releaseID string, asset localReleaseAsset) error {
	file, err := os.Open(asset.Path)
	if err != nil {
		return err
	}
	_, uploadErr := publisher.UploadReleaseAsset(ctx, ref, releaseID, asset.Name, asset.Size, file)
	closeErr := file.Close()
	return errors.Join(uploadErr, closeErr)
}

func findMatchingRemoteAsset(ctx context.Context, publisher ForgeReleasePublisher, ref RepositoryRef, releaseID string, local localReleaseAsset) (bool, bool, error) {
	assets, err := publisher.ListReleaseAssets(ctx, ref, releaseID)
	if err != nil {
		return false, false, err
	}
	var found *RemoteReleaseAsset
	for index := range assets {
		if assets[index].Name != local.Name {
			continue
		}
		if found != nil {
			return false, true, fmt.Errorf("release has duplicate assets named %q", local.Name)
		}
		found = &assets[index]
	}
	if found == nil {
		return false, false, nil
	}
	if found.Size != local.Size {
		return false, true, nil
	}
	reader, err := publisher.DownloadReleaseAsset(ctx, ref, *found)
	if err != nil {
		return false, true, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(reader, maxReleaseAsset+1))
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil {
		return false, true, errors.Join(copyErr, closeErr)
	}
	if written != local.Size {
		return false, true, nil
	}
	return hex.EncodeToString(hash.Sum(nil)) == local.SHA256, true, nil
}

func hashReleaseFile(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", fmt.Errorf("file %q exceeds %d bytes", path, limit)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
