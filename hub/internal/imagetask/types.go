// Package imagetask owns ephemeral Hub image jobs, independent of transport and HTTP.
package imagetask

import (
	"context"
	"errors"
)

const (
	MaxArchiveSize int64 = 2 << 30
	ChunkSize            = 256 << 10
	MaxActive            = 4
	MaxHistory           = 128
)

var (
	ErrNotFound  = errors.New("image task not found")
	ErrClosed    = errors.New("image task manager closed")
	ErrLimit     = errors.New("image task limit reached")
	ErrInvalid   = errors.New("invalid image task request")
	ErrBackend   = errors.New("remote image operation failed")
	ErrCanceled  = errors.New("image task canceled")
	ErrTimeout   = errors.New("image task timed out")
	ErrIntegrity = errors.New("archive size or checksum mismatch")
	ErrNotReady  = errors.New("image archive not ready")
)

type RemoteTask struct {
	ID, Status, Phase, Message string
	Progress                   *float64
	BytesDone                  int64
	BytesTotal                 *int64
	SizeBytes                  int64
	SHA256                     string
}

// Backend must honor ctx. Errors and remote messages are deliberately never exposed.
// GetChunk's bool reports EOF. CommitLoad receives the final streamed SHA-256;
// BeginLoad permits an empty digest when the upload has not yet been read.
type Backend interface {
	StartPull(ctx context.Context, agent, reference string) (RemoteTask, error)
	StartSave(ctx context.Context, agent, reference string) (RemoteTask, error)
	BeginLoad(ctx context.Context, agent string, size int64, sha256 string) (RemoteTask, error)
	PutChunk(ctx context.Context, agent, remoteID string, offset int64, data []byte) error
	GetChunk(ctx context.Context, agent, remoteID string, offset int64, max int) ([]byte, bool, error)
	CommitLoad(ctx context.Context, agent, remoteID, sha256 string) (RemoteTask, error)
	Status(ctx context.Context, agent, remoteID string) (RemoteTask, error)
	Cancel(ctx context.Context, agent, remoteID string) error
	Release(ctx context.Context, agent, remoteID string) error
}

type Task struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	AgentID       string   `json:"agent_id"`
	TargetAgentID string   `json:"target_agent_id,omitempty"`
	Status        string   `json:"status"`
	Phase         string   `json:"phase"`
	Message       string   `json:"message"`
	Progress      *float64 `json:"progress"`
	BytesDone     int64    `json:"bytes_done"`
	BytesTotal    *int64   `json:"bytes_total"`
	CreatedAt     string   `json:"created_at"`
	FinishedAt    string   `json:"finished_at,omitempty"`
	DownloadReady bool     `json:"download_ready"`
	OwnerID       string   `json:"-"`
	ArchiveSize   int64    `json:"-"`
}
