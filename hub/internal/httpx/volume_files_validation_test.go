package httpx

import (
	"encoding/base64"
	"strings"
	"testing"

	"arkade/protocol/envelope"
)

func TestVolumeFilePathBoundaries(t *testing.T) {
	for _, p := range []string{"", "."} {
		if !validVolumePath(p, true) || validVolumePath(p, false) {
			t.Errorf("root boundary %q", p)
		}
	}
	for _, p := range []string{"a", "config/app.json", "中文 文件.txt", "-option"} {
		if !validVolumePath(p, false) {
			t.Errorf("valid path rejected %q", p)
		}
	}
	for _, p := range []string{"/etc/passwd", "../etc", "a/../../etc", "a/./b", "a//b", "a/", "a\\b", "a\x00b", "a\nb", "a\rb", strings.Repeat("x", 4097)} {
		if validVolumePath(p, true) {
			t.Errorf("unsafe path accepted %q", p)
		}
	}
}
func TestVolumeFileWriteRequiresCASOrCreateOnly(t *testing.T) {
	validHash := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		p    envelope.VolumeFilesPayload
		want bool
	}{
		{"list root", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "list", Path: ""}, true},
		{"root delete", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "delete", Path: ""}, false},
		{"create empty", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "write", Path: "new", CreateOnly: true}, true},
		{"replace without hash", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "write", Path: "config"}, false},
		{"replace with hash", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "write", Path: "config", ExpectedSHA256: validHash}, true},
		{"create plus hash", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "write", Path: "config", ExpectedSHA256: validHash, CreateOnly: true}, false},
		{"invalid hash", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "write", Path: "config", ExpectedSHA256: strings.Repeat("g", 64)}, false},
		{"invalid base64", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "write", Path: "config", CreateOnly: true, DataBase64: "!!!"}, false},
		{"data on read", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "read", Path: "config", DataBase64: "eA=="}, false},
		{"arbitrary volume path", envelope.VolumeFilesPayload{Volume: "../../etc", Operation: "list"}, false},
		{"same rename", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "rename", Path: "a", Destination: "a"}, false},
		{"rename escape", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "rename", Path: "a", Destination: "../b"}, false},
		{"valid rename", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "rename", Path: "a", Destination: "folder/b"}, true},
		{"unknown operation", envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "exec", Path: "a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validVolumeRequest(tc.p); got != tc.want {
				t.Fatalf("valid=%v want=%v", got, tc.want)
			}
		})
	}
	p := envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "write", Path: "large", CreateOnly: true, DataBase64: base64.StdEncoding.EncodeToString(make([]byte, maxVolumeFileSize))}
	if !validVolumeRequest(p) {
		t.Fatal("exact size limit rejected")
	}
	p.DataBase64 = base64.StdEncoding.EncodeToString(make([]byte, maxVolumeFileSize+1))
	if validVolumeRequest(p) {
		t.Fatal("size limit bypassed")
	}
}
