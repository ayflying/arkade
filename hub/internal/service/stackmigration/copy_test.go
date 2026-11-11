package stackmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/imagetask"
	"arkade/protocol/envelope"
)

type fakeImages struct {
	ImageBackend
	data                                []byte
	corrupt                             bool
	put, commit, cancel, release, close int
}

func (f *fakeImages) ExportArchive(context.Context, string, string) (agentlink.ArchiveRef, error) {
	h := sha256.Sum256(f.data)
	return agentlink.ArchiveRef{ExportID: "export", Reference: "image:tag", SizeBytes: int64(len(f.data)), SHA256: hex.EncodeToString(h[:])}, nil
}
func (f *fakeImages) GetArchiveChunk(context.Context, string, string, int64, int) ([]byte, bool, error) {
	if f.corrupt {
		return []byte("bad"), true, nil
	}
	return f.data, true, nil
}
func (f *fakeImages) CloseArchive(context.Context, string, string) (bool, string, error) {
	f.close++
	return true, "", nil
}
func (f *fakeImages) BeginLoad(context.Context, string, int64, string) (imagetask.RemoteTask, error) {
	return imagetask.RemoteTask{ID: "load", Status: "receiving"}, nil
}
func (f *fakeImages) PutChunk(context.Context, string, string, int64, []byte) error {
	f.put++
	return nil
}
func (f *fakeImages) CommitLoad(context.Context, string, string, string) (imagetask.RemoteTask, error) {
	f.commit++
	return imagetask.RemoteTask{ID: "load", Status: "succeeded"}, nil
}
func (f *fakeImages) Cancel(context.Context, string, string) error  { f.cancel++; return nil }
func (f *fakeImages) Release(context.Context, string, string) error { f.release++; return nil }
func TestImageCopyIntegrityAndCleanup(t *testing.T) {
	for _, bad := range []bool{false, true} {
		f := &fakeImages{data: []byte("archive-data"), corrupt: bad}
		b := New(Deps{Images: f})
		err := b.CopyImage(context.Background(), "source", "target", "image:tag", nil)
		if (err != nil) != bad {
			t.Fatal("integrity result", bad, err)
		}
		if f.close != 1 || f.cancel != 1 || f.release != 1 {
			t.Fatalf("leaked transfer: %+v", f)
		}
		if bad && f.commit != 0 {
			t.Fatal("committed corrupt archive")
		}
	}
}

type fakeVolumeOps struct {
	Ops
	data  []byte
	empty bool
	put   int
}

func (f *fakeVolumeOps) VolumeArchiveChunkGet(_ context.Context, _, _ string, _ int64, _ int) ([]byte, bool, *agentlink.ConnError) {
	if f.empty {
		return nil, false, nil
	}
	return f.data, true, nil
}
func TestVolumeCopyIntegrityAndNoProgressBound(t *testing.T) {
	data := []byte("volume")
	hash := sha256.Sum256(data)
	ref := imagetask.ArchiveRef{ExportID: "export", SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
	for _, tc := range []struct {
		data  []byte
		empty bool
		valid bool
	}{{data, false, true}, {[]byte("wrong!"), false, false}, {nil, true, false}} {
		f := &fakeVolumeOps{data: tc.data, empty: tc.empty}
		err := copyVolumeChunks(context.Background(), volumeArchive{f}, "source", ref, func(int64, []byte) error { f.put++; return nil }, nil)
		if (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
}

// Keep the fake signature tied to the real protocol during concurrent evolution.
var _ = envelope.VolumeImportSucceeded
