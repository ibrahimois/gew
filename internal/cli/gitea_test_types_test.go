package cli

type giteaRepository struct {
	DefaultBranch string `json:"default_branch"`
	Empty         bool   `json:"empty"`
}

type giteaTreeEntry struct {
	Path string `json:"path"`
	SHA  string `json:"sha"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	Size int64  `json:"size"`
}

type giteaTreeResponse struct {
	Tree      []giteaTreeEntry `json:"tree"`
	Truncated bool             `json:"truncated"`
}

type giteaBlobResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	SHA      string `json:"sha"`
}

type giteaCommitDetails struct {
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

type giteaChangeOperation struct {
	Operation string `json:"operation"`
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	SHA       string `json:"sha,omitempty"`
}

type giteaChangeFilesRequest struct {
	Branch    string                 `json:"branch"`
	NewBranch string                 `json:"new_branch,omitempty"`
	Message   string                 `json:"message"`
	Files     []giteaChangeOperation `json:"files"`
}
