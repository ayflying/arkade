package migrate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestConcurrentBeginSameVolumeOneReservation(t *testing.T) {
	imp, _ := newTestImporter(t)
	start := make(chan struct{})
	results := make(chan error, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := imp.Begin(t.Context(), "one", "p", 3, sha([]byte("abc")), fmt.Sprintf("task%d", i))
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrTargetVolumeExists) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("same-volume Begin winners=%d", winners)
	}
}
func TestExternalCreateWinsBeforeInspectNeverWrites(t *testing.T) {
	calls := []string{}
	imp := NewImporter(t.TempDir(), func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args[0]+" "+args[1])
		if args[0] == "volume" && args[1] == "create" {
			return []byte("raced"), nil
		}
		if args[0] == "volume" && args[1] == "inspect" {
			return []byte(`[{"Name":"raced","Labels":{"external":"true"}}]`), nil
		}
		t.Fatal("helper ran against external volume")
		return nil, nil
	})
	data := []byte("abc")
	snap, _, err := imp.Begin(t.Context(), "raced", "p", 3, sha(data), "task")
	if err != nil {
		t.Fatal(err)
	}
	putAll(t, imp, snap.TaskID, data)
	_, err = imp.Commit(t.Context(), snap.TaskID, sha(data))
	if !errors.Is(err, ErrTargetVolumeExists) {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("engine calls=%v", calls)
	}
}
