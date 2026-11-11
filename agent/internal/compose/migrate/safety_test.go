package migrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestExistingTargetVolumeNeverExtracts(t *testing.T) {
	var runs atomic.Int32
	imp := NewImporter(t.TempDir(), func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "volume" {
			return []byte("existing\n"), nil
		}
		runs.Add(1)
		return nil, nil
	})
	data := []byte("archive")
	snap, _, err := imp.Begin(t.Context(), "existing", "p", int64(len(data)), sha(data), "task")
	if err != nil {
		t.Fatal(err)
	}
	putAll(t, imp, snap.TaskID, data)
	_, err = imp.Commit(t.Context(), snap.TaskID, sha(data))
	if !errors.Is(err, ErrTargetVolumeExists) || runs.Load() != 0 {
		t.Fatalf("existing volume extracted: %v %d", err, runs.Load())
	}
}
func TestConcurrentCommitExtractsExactlyOnce(t *testing.T) {
	var runs atomic.Int32
	fd := &fakeDocker{}
	imp := NewImporter(t.TempDir(), func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			runs.Add(1)
		}
		return fd.run(ctx, args...)
	})
	data := []byte("archive")
	snap, _, err := imp.Begin(t.Context(), "new", "p", int64(len(data)), sha(data), "task")
	if err != nil {
		t.Fatal(err)
	}
	putAll(t, imp, snap.TaskID, data)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = imp.Commit(t.Context(), snap.TaskID, sha(data)) }()
	}
	wg.Wait()
	if runs.Load() != 1 {
		t.Fatalf("extractions=%d", runs.Load())
	}
}
func TestCancelReceivingCleansOnlyArchive(t *testing.T) {
	root := t.TempDir()
	imp := NewImporter(root, func(context.Context, ...string) ([]byte, error) { t.Fatal("cancel ran engine"); return nil, nil })
	snap, _, err := imp.Begin(t.Context(), "new", "p", 3, sha([]byte("abc")), "task")
	if err != nil {
		t.Fatal(err)
	}
	putAll(t, imp, snap.TaskID, []byte("abc"))
	snap, err = imp.Cancel(t.Context(), snap.TaskID)
	if err != nil || snap.Status != ImportCanceled {
		t.Fatalf("cancel=%+v %v", snap, err)
	}
	if _, err := os.Stat(filepath.Join(root, "task")); !os.IsNotExist(err) {
		t.Fatalf("temporary directory retained %v", err)
	}
}
func TestMissingSourceNeverCreatesVolume(t *testing.T) {
	var runs int
	exp := NewVolumeExporter(t.TempDir(), func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "volume" {
			return []byte("No such volume"), errors.New("not found")
		}
		runs++
		return nil, nil
	})
	if _, err := exp.Export(t.Context(), "missing", "p"); err == nil || runs != 0 {
		t.Fatalf("missing source accepted: %v runs=%d", err, runs)
	}
}
func TestCancelRunningExtraction(t *testing.T) {
	entered := make(chan struct{})
	removed := make(chan struct{})
	fd := &fakeDocker{}
	imp := NewImporter(t.TempDir(), func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "volume" || args[0] == "inspect" {
			return fd.run(ctx, args...)
		}
		if args[0] == "rm" {
			if ctx.Err() != nil {
				t.Error("cleanup inherited canceled context")
			}
			close(removed)
			return nil, nil
		}
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	data := []byte("abc")
	snap, _, err := imp.Begin(t.Context(), "new", "p", 3, sha(data), "task")
	if err != nil {
		t.Fatal(err)
	}
	putAll(t, imp, snap.TaskID, data)
	done := make(chan error, 1)
	go func() { _, err := imp.Commit(t.Context(), snap.TaskID, sha(data)); done <- err }()
	<-entered
	canceled, err := imp.Cancel(t.Context(), snap.TaskID)
	if err != nil || canceled.Status != ImportCanceled {
		t.Fatalf("cancel=%+v %v", canceled, err)
	}
	select {
	case <-removed:
	default:
		t.Fatal("cancel returned before daemon helper removed")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("extract not interrupted %v", err)
	}
}

func TestRetryChunkMustMatch(t *testing.T) {
	imp, _ := newTestImporter(t)
	snap, _, err := imp.Begin(t.Context(), "new", "p", 3, sha([]byte("abc")), "task")
	if err != nil {
		t.Fatal(err)
	}
	putAll(t, imp, snap.TaskID, []byte("abc"))
	if _, err := imp.PutChunk(t.Context(), snap.TaskID, 0, []byte("xyz")); !errors.Is(err, ErrImportParamMismatch) {
		t.Fatalf("changed retry accepted %v", err)
	}
}
