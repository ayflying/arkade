package agentlink

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"arkade/protocol/envelope"
)

func TestVolumeChunkBoundsAndIntegrity(t *testing.T) {
	encoded := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name string
		out  envelope.ImageChunkGetResult
		max  int
		bad  bool
	}{
		{"valid", envelope.ImageChunkGetResult{Offset: 7, DataBase64: encoded("tar"), EOF: true}, 3, false},
		{"offset", envelope.ImageChunkGetResult{Offset: 8, DataBase64: encoded("tar"), EOF: true}, 3, true},
		{"invalid base64", envelope.ImageChunkGetResult{Offset: 7, DataBase64: "!!!!", EOF: true}, 3, true},
		{"over requested bound", envelope.ImageChunkGetResult{Offset: 7, DataBase64: encoded("four"), EOF: true}, 3, true},
		{"empty not eof", envelope.ImageChunkGetResult{Offset: 7}, 3, true},
		{"empty eof", envelope.ImageChunkGetResult{Offset: 7, EOF: true}, 3, false},
		{"hard bound", envelope.ImageChunkGetResult{Offset: 7, DataBase64: encoded(strings.Repeat("x", ChunkSizeHardLimit+1)), EOF: true}, ChunkSizeHardLimit, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, eof, err := decodeVolumeChunk(tc.out, 7, tc.max)
			if (err != nil) != tc.bad {
				t.Fatalf("data=%d eof=%v err=%v", len(data), eof, err)
			}
			if err != nil && err.Code != envelope.CodeUnsupported {
				t.Fatal(err)
			}
		})
	}
	for _, received := range []int64{9, 11} {
		if err := validateVolumeAck(7, 3, received); err == nil {
			t.Fatalf("accepted ack %d", received)
		}
	}
	if err := validateVolumeAck(7, 3, 10); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeArchiveAndStatusValidation(t *testing.T) {
	hash := strings.Repeat("a", 64)
	if err := validateVolumeArchive("id", "volume", 1, hash); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, volume string
		size       int64
		hash       string
	}{{"", "v", 1, hash}, {"i", "", 1, hash}, {"i", "v", 0, hash}, {"i", "v", MaxVolumeArchiveSizeHard + 1, hash}, {"i", "v", 1, "bad"}} {
		if err := validateVolumeArchive(tc.id, tc.volume, tc.size, tc.hash); err == nil {
			t.Fatal("invalid archive accepted", tc)
		}
	}
	for _, state := range []string{envelope.VolumeImportPending, envelope.VolumeImportRunning, envelope.VolumeImportSucceeded, envelope.VolumeImportFailed, envelope.VolumeImportCanceled} {
		if err := validateVolumeStatus(envelope.VolumeImportResult{TaskID: "id", Volume: "v", Status: state}, "id"); err != nil {
			t.Fatal(err)
		}
	}
	for _, out := range []envelope.VolumeImportResult{{TaskID: "other", Volume: "v", Status: "succeeded"}, {TaskID: "id", Status: "succeeded"}, {TaskID: "id", Volume: "v", Status: "mystery"}, {TaskID: "id", Volume: "v", Status: "running", BytesDone: -1}} {
		if err := validateVolumeStatus(out, "id"); err == nil {
			t.Fatal("invalid status accepted", out)
		}
	}
}

func TestVolumeOpsRejectBadInputBeforeRPCAndPreserveOffline(t *testing.T) {
	ops := NewOps(NewRegistry())
	ctx := context.Background()
	for _, data := range [][]byte{nil, make([]byte, ChunkSizeHardLimit+1)} {
		if err := ops.VolumeImportChunkPut(ctx, "offline", "id", 0, data); err == nil || err.Code != envelope.CodeBadRequest {
			t.Fatal(err)
		}
	}
	if err := ops.VolumeImportChunkPut(ctx, "offline", "id", -1, []byte("x")); err == nil || err.Code != envelope.CodeBadRequest {
		t.Fatal(err)
	}
	if _, _, err := ops.VolumeArchiveChunkGet(ctx, "offline", "id", -1, 1); err == nil || err.Code != envelope.CodeBadRequest {
		t.Fatal(err)
	}
	if _, err := ops.ComposeImport(ctx, "offline", envelope.ComposeImportPayload{Overwrite: true}); err == nil || err.Code != envelope.CodeBadRequest {
		t.Fatal(err)
	}
	if _, err := ops.VolumeImportStatus(ctx, "offline", "id"); err == nil || err.Code != envelope.CodeAgentOffline {
		t.Fatal(err)
	}
	if _, err := ops.ComposeMigrationPlan(ctx, "offline", envelope.ComposeMigrationPlanPayload{Name: "demo"}); err == nil || err.Code != envelope.CodeAgentOffline {
		t.Fatal(err)
	}
}
