package migrate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHelperCleanupFailureIsReturned(t *testing.T) {
	_, err := runArchiveHelper(t.Context(), func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			return nil, context.Canceled
		}
		return nil, errors.New("daemon unavailable")
	}, "alpine", "true")
	if !errors.Is(err, ErrHelperCleanup) {
		t.Fatalf("cleanup failure hidden %v", err)
	}
}
func TestHelperAlreadyAutoRemovedIsSafe(t *testing.T) {
	_, err := runArchiveHelper(t.Context(), func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, errors.New("gone")
		}
		return nil, nil
	}, "alpine", "true")
	if err != nil {
		t.Fatal(err)
	}
}
func TestCanceledCommitWaitingForGlobalSlotNeverCreates(t *testing.T) {
	volumeRestoreSem <- struct{}{}
	defer func() { <-volumeRestoreSem }()
	imp := NewImporter(t.TempDir(), func(context.Context, ...string) ([]byte, error) {
		t.Error("canceled waiting commit executed engine")
		return nil, nil
	})
	data := []byte("abc")
	snap, _, err := imp.Begin(t.Context(), "new", "p", 3, sha(data), "waiting")
	if err != nil {
		t.Fatal(err)
	}
	putAll(t, imp, snap.TaskID, data)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = imp.Commit(t.Context(), snap.TaskID, sha(data)) }()
	deadline := time.After(time.Second)
	for {
		imp.mu.Lock()
		task := imp.task[snap.TaskID]
		imp.mu.Unlock()
		task.mu.Lock()
		ready := task.cancel != nil
		task.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline:
			t.Fatal("commit did not establish cancel")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	canceled := make(chan error, 1)
	go func() { _, err := imp.Cancel(t.Context(), snap.TaskID); canceled <- err }()
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel stuck behind global slot")
	}
	<-done
}
func TestCommitRequestCancellationReleasesVolumeReservation(t *testing.T) {
	volumeRestoreSem <- struct{}{}
	defer func() { <-volumeRestoreSem }()
	imp := NewImporter(t.TempDir(), func(context.Context, ...string) ([]byte, error) {
		t.Error("canceled request executed engine")
		return nil, nil
	})
	data := []byte("abc")
	snap, _, err := imp.Begin(t.Context(), "new", "p", 3, sha(data), "request")
	if err != nil {
		t.Fatal(err)
	}
	putAll(t, imp, snap.TaskID, data)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	final, err := imp.Commit(ctx, snap.TaskID, sha(data))
	if !errors.Is(err, context.Canceled) || !importTerminal(final.Status) {
		t.Fatalf("cancel not terminal: %+v %v", final, err)
	}
	if _, _, err := imp.Begin(t.Context(), "new", "p", 3, sha(data), "replacement"); err != nil {
		t.Fatalf("volume reservation leaked: %v", err)
	}
}

func TestHelperNeverDeletesWrongOwner(t *testing.T) {
	_, err := runArchiveHelper(t.Context(), func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return []byte("other"), nil
		}
		if args[0] == "rm" {
			t.Fatal("removed foreign container")
		}
		if args[0] == "run" && !strings.Contains(strings.Join(args, " "), "--network none") {
			t.Fatal("helper has network")
		}
		return nil, nil
	}, "alpine", "true")
	if !errors.Is(err, ErrHelperCleanup) {
		t.Fatal(err)
	}
}
