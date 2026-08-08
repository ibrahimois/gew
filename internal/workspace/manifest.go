package workspace

// ManifestEntry is the durable, provider-neutral identity of a tracked path.
// Size is -1 when an older state file did not record it.
type ManifestEntry struct {
	BlobSHA string `json:"blob_sha,omitempty"`
	Hash    string `json:"hash"`
	Mode    uint32 `json:"mode,omitempty"`
	Size    int64  `json:"size,omitempty"`
}

type Manifest map[string]ManifestEntry
