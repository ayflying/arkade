package imagetask

import (
	"context"
	"io"
	"testing"
)

func TestFailedDownloadExpiresOrCloses(t *testing.T) {
	for _, closeManager := range []bool{false, true} {
		name := "expiry"
		if closeManager {
			name = "close"
		}
		t.Run(name, func(t *testing.T) {
			f := &fakeBackend{data: []byte("archive")}
			m := New(f)
			defer m.Close()
			// Trigger the same deadline callback deterministically rather than
			// racing a short timer against export completion on a loaded host.
			task, err := m.Start("export", "a", "", "image", "user-1")
			if err != nil {
				t.Fatal(err)
			}
			if got := await(t, m, task.ID); !got.DownloadReady {
				t.Fatal(got)
			}
			if err := m.Download(context.Background(), task.ID, shortWriter{}); err == nil {
				t.Fatal("short write accepted")
			}
			if closeManager {
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				m.mu.Lock()
				j := m.jobs[task.ID]
				m.mu.Unlock()
				m.expire(j)
			}
			waitRelease(t, f, 1)
			if got, _ := m.Get(task.ID); got.DownloadReady {
				t.Fatal("archive remains downloadable after disposal", got)
			}
			if err := m.Download(context.Background(), task.ID, io.Discard); err != ErrNotReady {
				t.Fatal(err)
			}
			if closeManager {
				if _, err := m.Start("export", "a", "", "image", "user-1"); err != ErrClosed {
					t.Fatal("closed manager accepted new work", err)
				}
			}
		})
	}
}

func TestCanceledDownloadRetainsArchive(t *testing.T) {
	f := &fakeBackend{data: []byte("archive")}
	m := New(f)
	defer m.Close()
	task, err := m.Start("export", "a", "", "image", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	await(t, m, task.ID)
	// A writer can cancel the request during transfer. The verified-success
	// path must not consume an archive whose transfer was canceled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &cancelDownloadWriter{cancel: cancel}
	if err := m.Download(ctx, task.ID, writer); err == nil {
		t.Fatal("canceled download accepted")
	}
	if got, _ := m.Get(task.ID); !got.DownloadReady {
		t.Fatal("client cancellation consumed archive", got)
	}
	if err := m.Download(context.Background(), task.ID, io.Discard); err != nil {
		t.Fatal("retry after client cancellation failed", err)
	}
}

type cancelDownloadWriter struct {
	cancel context.CancelFunc
}

func (w *cancelDownloadWriter) Write(b []byte) (int, error) {
	w.cancel()
	return 0, context.Canceled
}
