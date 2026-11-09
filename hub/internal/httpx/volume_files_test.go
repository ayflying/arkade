package httpx

import (
	"arkade/protocol/envelope"
	"encoding/base64"
	"net/http"
	"testing"
)

func TestVolumeFileValidation(t *testing.T) {
	valid := envelope.VolumeFilesPayload{Volume: "test-volume", Operation: "list"}
	if !validVolumeRequest(valid) {
		t.Fatal("root list rejected")
	}
	for _, path := range []string{"../etc", "a/../b", "/etc", "a\\b", "a//b", "a/./b", "a\x00b"} {
		p := valid
		p.Path = path
		if validVolumeRequest(p) {
			t.Fatalf("path accepted %q", path)
		}
	}
	for _, op := range []string{"delete", "read", "write", "mkdir", "rename"} {
		p := valid
		p.Operation = op
		if validVolumeRequest(p) {
			t.Fatalf("root mutation accepted %s", op)
		}
	}
	p := envelope.VolumeFilesPayload{Volume: "test-volume", Path: "hello.txt", Operation: "write", CreateOnly: true}
	if !validVolumeRequest(p) {
		t.Fatal("empty new file rejected")
	}
	p.CreateOnly = false
	if validVolumeRequest(p) {
		t.Fatal("blind overwrite accepted")
	}
	p.ExpectedSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if !validVolumeRequest(p) {
		t.Fatal("conditional write rejected")
	}
	p.DataBase64 = base64.StdEncoding.EncodeToString(make([]byte, maxVolumeFileSize+1))
	if validVolumeRequest(p) {
		t.Fatal("oversize accepted")
	}
}

func TestVolumeFilesHTTPPermissionAndPayload(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, n int) any {
		return envelope.VolumeFilesResult{Path: "", Entries: []envelope.VolumeFileEntry{{Name: "hello.txt", Kind: "file", Size: 4}}}
	})
	admin := e.loginAsFor(t, a.url, "admin", testPassword)
	path := a.url + "/api/agents/" + a.id + "/volume-files"
	good := `{"volume":"test-volume","operation":"list","path":""}`
	resp, b := doHTTP(t, http.MethodPost, path, good, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("unauth %d %s", resp.StatusCode, b)
	}
	e.createUser(t, e.login(t), "volume-ungranted", testPassword, "user")
	limited := e.loginAsFor(t, a.url, "volume-ungranted", testPassword)
	resp, b = doHTTP(t, http.MethodPost, path, good, limited)
	if resp.StatusCode != 403 {
		t.Fatalf("ungranted %d %s", resp.StatusCode, b)
	}
	for _, bad := range []string{`{"volume":"test-volume","operation":"list","path":"../"}`, good + good, `{"volume":"test-volume","operation":"delete","path":""}`, `{"volume":"test-volume","operation":"list","command":"id"}`} {
		resp, b = doHTTP(t, http.MethodPost, path, bad, admin)
		if resp.StatusCode != 400 {
			t.Fatalf("bad payload %d %s", resp.StatusCode, b)
		}
	}
	if len(a.requests()) != 0 {
		t.Fatal("invalid/forbidden reached Agent")
	}
	resp, b = doHTTP(t, http.MethodPost, path, good, admin)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("success %d %s", resp.StatusCode, b)
	}
	requests := a.requests()
	if len(requests) != 1 || requests[0].Method != envelope.MethodVolumeFiles {
		t.Fatalf("unexpected RPC %+v", requests)
	}
}
