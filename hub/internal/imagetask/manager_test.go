package imagetask

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu                   sync.Mutex
	data                 []byte
	uploaded             []byte
	released, canceled   []string
	fail, pending, block bool
	entered              chan struct{}
	unblock              chan struct{}
	badSize              bool
}

func (f *fakeBackend) result(id string) RemoteTask {
	h := sha256.Sum256(f.data)
	r := RemoteTask{ID: id, Status: "succeeded", SizeBytes: int64(len(f.data)), SHA256: hex.EncodeToString(h[:])}
	if f.pending {
		r.Status = "running"
	}
	if f.badSize {
		r.SizeBytes++
	}
	return r
}
func (f *fakeBackend) StartPull(ctx context.Context, a, r string) (RemoteTask, error) {
	return f.StartSave(ctx, a, r)
}
func (f *fakeBackend) StartSave(ctx context.Context, a, r string) (RemoteTask, error) {
	if f.block {
		close(f.entered)
		<-f.unblock
	}
	if f.fail {
		return RemoteTask{ID: "save"}, errors.New("password=secret")
	}
	return f.result("save"), nil
}
func (f *fakeBackend) BeginLoad(ctx context.Context, a string, n int64, s string) (RemoteTask, error) {
	f.mu.Lock()
	f.uploaded = nil
	f.mu.Unlock()
	return RemoteTask{ID: "load", Status: "running"}, nil
}
func (f *fakeBackend) PutChunk(ctx context.Context, a, id string, o int64, b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o != int64(len(f.uploaded)) {
		return errors.New("offset")
	}
	f.uploaded = append(f.uploaded, b...)
	return ctx.Err()
}
func (f *fakeBackend) GetChunk(ctx context.Context, a, id string, o int64, n int) ([]byte, bool, error) {
	if o > int64(len(f.data)) {
		return nil, true, nil
	}
	end := int(o) + n
	if end > len(f.data) {
		end = len(f.data)
	}
	return f.data[int(o):end], end == len(f.data), ctx.Err()
}
func (f *fakeBackend) CommitLoad(ctx context.Context, a, id, s string) (RemoteTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := sha256.Sum256(f.uploaded)
	if hex.EncodeToString(h[:]) != s {
		return RemoteTask{}, errors.New("sha")
	}
	return RemoteTask{ID: id, Status: "succeeded"}, ctx.Err()
}
func (f *fakeBackend) Status(ctx context.Context, a, id string) (RemoteTask, error) {
	return f.result(id), ctx.Err()
}
func (f *fakeBackend) Cancel(ctx context.Context, a, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, a+":"+id)
	return nil
}
func (f *fakeBackend) Release(ctx context.Context, a, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, a+":"+id)
	return nil
}
func await(t *testing.T, m *Manager, id string) Task {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task, e := m.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if terminal(task.Status) {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task did not finish")
	return Task{}
}
func waitRelease(t *testing.T, f *fakeBackend, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		count := len(f.released)
		f.mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("archive not released")
}

func TestExportDownloadAndSnapshot(t *testing.T) {
	f := &fakeBackend{data: bytes.Repeat([]byte("abc"), ChunkSize)}
	m := New(f)
	defer m.Close()
	task, e := m.Start("export", "a", "", "image", "99")
	if e != nil {
		t.Fatal(e)
	}
	task = await(t, m, task.ID)
	if !task.DownloadReady {
		t.Fatal(task)
	}
	encoded, _ := json.Marshal(task)
	if strings.Contains(strings.ToLower(string(encoded)), "owner") {
		t.Fatal(string(encoded))
	}
	var out bytes.Buffer
	if e = m.Download(context.Background(), task.ID, &out); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(out.Bytes(), f.data) {
		t.Fatal("data differs")
	}
	waitRelease(t, f, 1)
	if e = m.Download(context.Background(), task.ID, &out); e != ErrNotReady {
		t.Fatal(e)
	}
}
func TestMigrateAndUpload(t *testing.T) {
	for _, upload := range []bool{false, true} {
		t.Run(map[bool]string{false: "migrate", true: "upload"}[upload], func(t *testing.T) {
			f := &fakeBackend{data: bytes.Repeat([]byte("x"), ChunkSize+19)}
			m := New(f)
			defer m.Close()
			var task Task
			var err error
			if upload {
				task, err = m.Upload(context.Background(), "b", "7", bytes.NewReader(f.data), int64(len(f.data)))
			} else {
				task, err = m.Start("migrate", "a", "b", "image", "7")
			}
			if err != nil {
				t.Fatal(err)
			}
			task = await(t, m, task.ID)
			if task.Status != "succeeded" {
				t.Fatal(task)
			}
			f.mu.Lock()
			equal := bytes.Equal(f.uploaded, f.data)
			f.mu.Unlock()
			if !equal {
				t.Fatal("data differs")
			}
			if upload {
				waitRelease(t, f, 1)
			} else {
				waitRelease(t, f, 2)
			}
		})
	}
}
func TestLimitsAndClosed(t *testing.T) {
	f := &fakeBackend{pending: true}
	m := New(f)
	for i := 0; i < MaxActive; i++ {
		if _, e := m.Start("pull", "a", "", "image", "user-1"); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := m.Start("pull", "a", "", "image", "user-1"); e != ErrLimit {
		t.Fatal(e)
	}
	m.Close()
	if _, e := m.Start("pull", "a", "", "image", "user-1"); e != ErrClosed {
		t.Fatal(e)
	}
	if _, e := m.Upload(context.Background(), "a", "user-1", strings.NewReader(""), MaxArchiveSize+1); e != ErrInvalid {
		t.Fatal(e)
	}
}
func TestFailureSanitizedAndReleased(t *testing.T) {
	f := &fakeBackend{fail: true}
	m := New(f)
	defer m.Close()
	task, _ := m.Start("export", "a", "", "image", "user-1")
	task = await(t, m, task.ID)
	if task.Status != "failed" || strings.Contains(task.Message, "secret") {
		t.Fatal(task)
	}
	waitRelease(t, f, 1)
}
func TestBadArchiveAndUploadLength(t *testing.T) {
	f := &fakeBackend{data: []byte("x"), badSize: true}
	m := New(f)
	defer m.Close()
	task, _ := m.Start("migrate", "a", "b", "image", "user-1")
	if got := await(t, m, task.ID); got.Status != "failed" {
		t.Fatal(got)
	}
	waitRelease(t, f, 2)
	for _, data := range []string{"", "xx"} {
		if _, e := m.Upload(context.Background(), "a", "user-1", strings.NewReader(data), 1); e != ErrIntegrity {
			t.Fatal(e)
		}
	}
}
func TestCancelLateRemoteAndCloseRace(t *testing.T) {
	for _, closeManager := range []bool{false, true} {
		f := &fakeBackend{block: true, entered: make(chan struct{}), unblock: make(chan struct{})}
		m := New(f)
		task, _ := m.Start("export", "a", "", "image", "user-1")
		<-f.entered
		var wg sync.WaitGroup
		for n := 0; n < 8; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if closeManager {
					m.Close()
				} else {
					m.Cancel(task.ID)
				}
				m.List()
				m.Get(task.ID)
			}()
		}
		wg.Wait()
		close(f.unblock)
		waitRelease(t, f, 1)
		got, _ := m.Get(task.ID)
		if got.Status != "canceled" || got.DownloadReady {
			t.Fatal(got)
		}
		m.Close()
	}
}
func TestTimeoutAndExportExpiry(t *testing.T) {
	for _, pending := range []bool{true, false} {
		f := &fakeBackend{pending: pending}
		m := New(f)
		m.timeout = 20 * time.Millisecond
		m.pollInterval = time.Millisecond
		task, _ := m.Start("export", "a", "", "image", "user-1")
		waitRelease(t, f, 1)
		got := await(t, m, task.ID)
		if got.DownloadReady {
			t.Fatal(got)
		}
		if pending && got.Status != "failed" {
			t.Fatal(got)
		}
		m.Close()
	}
}
func TestUploadRequestCanceled(t *testing.T) {
	f := &fakeBackend{}
	m := New(f)
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task, e := m.Upload(ctx, "a", "user-1", strings.NewReader("data"), 4)
	if e == nil {
		t.Fatal("expected cancellation")
	}
	if got := await(t, m, task.ID); got.Status != "canceled" {
		t.Fatal(got)
	}
	waitRelease(t, f, 1)
}
func TestSnapshotsDoNotAlias(t *testing.T) {
	f := &fakeBackend{pending: true}
	m := New(f)
	defer m.Close()
	j, e := m.create("pull", "a", "", "1")
	if e != nil {
		t.Fatal(e)
	}
	n := int64(10)
	m.update(j, "uploading", 5, &n)
	a, _ := m.Get(j.task.ID)
	*a.Progress = 999
	*a.BytesTotal = 999
	b, _ := m.Get(j.task.ID)
	if *b.Progress != .5 || *b.BytesTotal != 10 {
		t.Fatal(b)
	}
}

func TestHistoryBound(t *testing.T) {
	f := &fakeBackend{}
	m := New(f)
	defer m.Close()
	for i := 0; i < MaxHistory+20; i++ {
		j, e := m.create("pull", "a", "", "1")
		if e != nil {
			t.Fatal(e)
		}
		m.finish(j, nil)
	}
	if len(m.List()) != MaxHistory {
		t.Fatal(len(m.List()))
	}
}
func TestRemoteFailureAndChecksum(t *testing.T) {
	f := &fakeBackend{}
	m := New(f)
	defer m.Close()
	j, _ := m.create("pull", "a", "", "1")
	if _, e := m.wait(j, "a", RemoteTask{ID: "x", Status: "failed", Message: "password=secret"}); e != ErrBackend {
		t.Fatal(e)
	}
	f.data = []byte("tampered")
	r := f.result("save")
	r.SHA256 = strings.Repeat("0", 64)
	if e := m.copyArchive(context.Background(), j, "a", r, func(_ int64, b []byte) error { return nil }); e != ErrIntegrity {
		t.Fatal(e)
	}
	m.finish(j, ErrBackend)
}
func TestUploadDetachedAndDownloadShortWriter(t *testing.T) {
	f := &fakeBackend{data: []byte("data")}
	m := New(f)
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	task, e := m.Upload(ctx, "a", "user-1", bytes.NewReader(f.data), 4)
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	if got := await(t, m, task.ID); got.Status != "succeeded" {
		t.Fatal(got)
	}
	exported, e := m.Start("export", "a", "", "image", "user-1")
	if e != nil {
		t.Fatal(e)
	}
	got := await(t, m, exported.ID)
	if !got.DownloadReady || got.ArchiveSize != int64(len(f.data)) || got.OwnerID != "user-1" {
		t.Fatal(got)
	}
	// The upload's temporary resource is released independently of this export.
	waitRelease(t, f, 1)
	if e := m.Download(context.Background(), exported.ID, shortWriter{}); e == nil {
		t.Fatal("short write accepted")
	}
	got, _ = m.Get(exported.ID)
	if !got.DownloadReady {
		t.Fatal("failed download must remain retryable", got)
	}
	f.mu.Lock()
	releases := len(f.released)
	f.mu.Unlock()
	if releases != 1 {
		t.Fatalf("failed download released its archive: %d releases", releases)
	}
	var downloaded bytes.Buffer
	if e := m.Download(context.Background(), exported.ID, &downloaded); e != nil {
		t.Fatal("retry failed", e)
	}
	if !bytes.Equal(downloaded.Bytes(), f.data) {
		t.Fatal("retry returned different data")
	}
	waitRelease(t, f, 2)
	got, _ = m.Get(exported.ID)
	if got.DownloadReady {
		t.Fatal("successful retry must consume the archive", got)
	}
	if e := m.Download(context.Background(), exported.ID, &downloaded); e != ErrNotReady {
		t.Fatal("successful download must be single-use", e)
	}
}

type shortWriter struct{}

func (shortWriter) Write(b []byte) (int, error) { return 0, nil }

var _ io.Reader = (*bytes.Reader)(nil)
