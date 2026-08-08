package forge

import (
	"context"
	"io"
)

type RemoteRelease struct {
	ID           string
	TagName      string
	TargetCommit string
	Title        string
	Notes        string
	URL          string
	Draft        bool
	Prerelease   bool
}

type RemoteReleaseAsset struct {
	ID   string
	Name string
	Size int64
	URL  string
}

type CreateReleaseRequest struct {
	Repository   RepositoryRef
	TagName      string
	TargetCommit string
	Title        string
	Notes        string
	Draft        bool
	Prerelease   bool
	Latest       bool
}

// ForgeReleasePublisher is an optional mutation role. Implementations must
// never replace or delete an existing release or asset.
type ForgeReleasePublisher interface {
	FindReleaseByTag(context.Context, RepositoryRef, string) (RemoteRelease, error)
	CreateRelease(context.Context, CreateReleaseRequest) (RemoteRelease, error)
	ListReleaseAssets(context.Context, RepositoryRef, string) ([]RemoteReleaseAsset, error)
	UploadReleaseAsset(context.Context, RepositoryRef, string, string, int64, io.Reader) (RemoteReleaseAsset, error)
	DownloadReleaseAsset(context.Context, RepositoryRef, RemoteReleaseAsset) (io.ReadCloser, error)
}

func ReleasePublisher(remote Forge) (ForgeReleasePublisher, error) {
	publisher, ok := remote.(ForgeReleasePublisher)
	if !ok {
		return nil, ErrUnsupported
	}
	return publisher, nil
}
