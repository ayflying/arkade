package compose

import (
	"arkade/agent/internal/compose/migrate"
	"context"
	"errors"
	"sync"
	"testing"
)

func TestMigrationImportRejectsSameNameAndNeverStarts(t *testing.T) {
	m, _ := newTestManager(t)
	m.applier = func(context.Context, string, string, string) PrecheckResult { return PrecheckResult{OK: true} }
	req := migrate.ComposeArchive{Name: "new", Config: "services: {}", Env: "KEY=secret"}
	req.ExpectHash = HashProject(req.Config, req.Env)
	result, err := m.ImportMigration(t.Context(), req)
	if err != nil || result.ConfigHash != req.ExpectHash {
		t.Fatalf("import=%+v %v", result, err)
	}
	req.Config = "services: {x: {image: other}}"
	req.ExpectHash = HashProject(req.Config, req.Env)
	if _, err := m.ImportMigration(t.Context(), req); !errors.Is(err, migrate.ErrComposeExists) {
		t.Fatalf("overwrite accepted %v", err)
	}
	got, _ := m.ws.Read("new")
	if got != "services: {}" {
		t.Fatal("existing config overwritten")
	}
}
func TestMigrationImportRejectsEngineExternal(t *testing.T) {
	m, _ := newTestManager(t)
	m.SetLabelSource(&fakeLabels{items: []ContainerLabels{composeC("external", "web", stateRunning)}})
	if _, err := m.ImportMigration(t.Context(), migrate.ComposeArchive{Name: "external", Config: "services: {}"}); !errors.Is(err, migrate.ErrComposeExists) {
		t.Fatalf("external overwrite %v", err)
	}
	if m.ws.Exists("external") {
		t.Fatal("external config written")
	}
}
func TestMigrationConcurrentImportOneWinner(t *testing.T) {
	m, _ := newTestManager(t)
	m.applier = func(context.Context, string, string, string) PrecheckResult { return PrecheckResult{OK: true} }
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.ImportMigration(t.Context(), migrate.ComposeArchive{Name: "one", Config: "services: {}"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, migrate.ErrComposeExists) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}
func TestMigrationPlanReportsBusyWithoutRead(t *testing.T) {
	m, _ := newTestManager(t)
	sem := m.locks.semaphoreFor("busy")
	sem <- struct{}{}
	defer func() { <-sem }()
	plan, err := m.MigrationPlan(t.Context(), "busy")
	if err != nil || !plan.Busy || plan.Config != "" {
		t.Fatalf("busy=%+v %v", plan, err)
	}
}
