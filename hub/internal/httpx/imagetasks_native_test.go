package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

func TestNativeImageTasksStrictJSONAndQueryIsolation(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	for _, body := range []string{`{"reference":"alpine","unknown":1}`, `{"reference":7}`, `{"agent_id":"other","reference":"alpine"}`, `null`, ``} {
		resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/pull?reference=alpine", body, cookies)
		if resp.StatusCode != 400 {
			t.Fatalf("%q: %d %s", body, resp.StatusCode, raw)
		}
	}
	if len(a.requests()) != 0 {
		t.Fatal("invalid request sent RPC")
	}
	resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/pull?reference=alpine", `{}`, cookies)
	if resp.StatusCode != 400 {
		t.Fatalf("query supplied body reference: %d %s", resp.StatusCode, raw)
	}
}

func TestNativeImageTaskMissingLengthEmptyResponse(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	// A non-buffer reader forces chunked transfer with no Content-Length header.
	req, err := http.NewRequest(http.MethodPost, a.url+"/api/agents/"+a.id+"/images/import", io.NopCloser(strings.NewReader("archive")))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusLengthRequired || len(body) != 0 {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if len(a.requests()) != 0 {
		t.Fatal("missing length initiated RPC")
	}
}

func TestNativeImageTaskSlowUploadCancellation(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		switch req.Method {
		case envelope.MethodImageLoadBegin:
			return envelope.ImageTask{TaskID: "slow", Status: "running"}
		case envelope.MethodImageTaskCancel:
			return envelope.ImageTaskCancelResult{TaskID: "slow", Canceled: true}
		case envelope.MethodImageTaskRelease:
			return envelope.ImageTaskReleaseResult{TaskID: "slow", Released: true}
		}
		return nil
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/import", reader)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.Header.Set("X-Archive-Size", "1024")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	waitFor(t, 3*time.Second, func() bool {
		for _, call := range a.requests() {
			if call.Method == envelope.MethodImageLoadBegin {
				return true
			}
		}
		return false
	}, "slow upload begin")
	cancel()
	_ = writer.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled upload succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not unblock")
	}
	waitFor(t, 3*time.Second, func() bool {
		for _, call := range a.requests() {
			if call.Method == envelope.MethodImageTaskRelease {
				return true
			}
		}
		return false
	}, "canceled upload remote cleanup")
	for _, call := range a.requests() {
		if call.Method == envelope.MethodImageLoadCommit {
			t.Fatal("canceled upload committed archive")
		}
	}
}

func TestNativeImageTaskStreamRoundTripAndExpiry(t *testing.T) {
	e := newTestEnv(t)
	archive := []byte("raw tar bytes: not JSON and not multipart")
	digest := sha256.Sum256(archive)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		switch req.Method {
		case envelope.MethodImageSave:
			return envelope.ImageTask{TaskID: "save", Status: "succeeded", SizeBytes: int64(len(archive)), SHA256: hex.EncodeToString(digest[:])}
		case envelope.MethodImageChunkGet:
			return envelope.ImageChunkGetResult{TaskID: "save", DataBase64: base64.StdEncoding.EncodeToString(archive), Offset: 0, EOF: true, SizeBytes: int64(len(archive))}
		case envelope.MethodImageLoadBegin:
			return envelope.ImageTask{TaskID: "load", Status: "running"}
		case envelope.MethodImageChunkPut:
			var payload envelope.ImageChunkPutPayload
			if err := envelope.DecodePayload(&req, &payload); err != nil {
				t.Error(err)
			}
			b, err := base64.StdEncoding.DecodeString(payload.DataBase64)
			if err != nil || string(b) != string(archive) {
				t.Errorf("archive changed %q %v", b, err)
			}
			return envelope.ImageChunkPutResult{TaskID: "load", ReceivedBytes: int64(len(archive))}
		case envelope.MethodImageLoadCommit:
			return envelope.ImageTask{TaskID: "load", Status: "succeeded"}
		case envelope.MethodImageTaskRelease:
			return envelope.ImageTaskReleaseResult{Released: true}
		}
		return nil
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	// JSON content-type is deliberate: GF must not pre-read/parse the archive.
	req, err := http.NewRequest(http.MethodPost, a.url+"/api/agents/"+a.id+"/images/import", strings.NewReader(string(archive)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 202 {
		t.Fatalf("upload %d %s", resp.StatusCode, raw)
	}
	resp, raw = doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/export", `{"reference":"alpine"}`, cookies)
	if resp.StatusCode != 202 {
		t.Fatalf("export %d %s", resp.StatusCode, raw)
	}
	var created struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	path := a.url + "/api/image-tasks/" + created.Task.ID
	waitFor(t, 3*time.Second, func() bool {
		response, body := doHTTP(t, http.MethodGet, path, "", cookies)
		var task struct {
			Task struct {
				Ready bool `json:"download_ready"`
			} `json:"task"`
		}
		_ = json.Unmarshal(body, &task)
		return response.StatusCode == 200 && task.Task.Ready
	}, "export archive ready")
	resp, raw = doHTTP(t, http.MethodGet, path+"/download", "", cookies)
	if resp.StatusCode != 200 || string(raw) != string(archive) {
		t.Fatalf("download %d %q", resp.StatusCode, raw)
	}
	for key, want := range map[string]string{"Content-Type": "application/x-tar", "Cache-Control": "no-store", "Content-Disposition": `attachment; filename="arkade-image.tar"`} {
		if got := resp.Header.Get(key); got != want {
			t.Errorf("%s=%q want %q", key, got, want)
		}
	}
	if resp.ContentLength != int64(len(archive)) {
		t.Fatalf("length %d", resp.ContentLength)
	}
	resp, raw = doHTTP(t, http.MethodGet, path+"/download", "", cookies)
	if resp.StatusCode != 400 || !strings.Contains(string(raw), "导出未完成或下载已过期") {
		t.Fatalf("second download %d %s", resp.StatusCode, raw)
	}
}
