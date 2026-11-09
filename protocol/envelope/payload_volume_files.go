package envelope

// VolumeFilesPayload is bounded control/data I/O, not an arbitrary command runner.
// Path and Destination are slash-separated paths relative to an existing named volume.
type VolumeFilesPayload struct {
	Volume         string `json:"volume"`
	Operation      string `json:"operation"` // list, read, write, mkdir, rename, delete
	Path           string `json:"path"`
	Destination    string `json:"destination,omitempty"`
	DataBase64     string `json:"data_base64,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"` // required when replacing a file
	CreateOnly     bool   `json:"create_only,omitempty"`
}
type VolumeFileEntry struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // directory, file, symlink, unsupported
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
	Mode       string `json:"mode"`
}
type VolumeFilesResult struct {
	Path       string            `json:"path"`
	Entries    []VolumeFileEntry `json:"entries,omitempty"`
	DataBase64 string            `json:"data_base64,omitempty"`
	SHA256     string            `json:"sha256,omitempty"`
	Size       int64             `json:"size,omitempty"`
	Message    string            `json:"message,omitempty"`
}
