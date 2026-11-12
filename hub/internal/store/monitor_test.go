package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"arkade/hub/internal/model/do"
	"github.com/gogf/gf/v2/database/gdb"
)

func TestMonitorHistoryPruning(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, status := range []string{"pending", "firing", "resolved", "closed"} {
		for _, seen := range []int64{10, 100} {
			v := MonitorIncident{ID: fmt.Sprintf("%s-%d", status, seen), Fingerprint: fmt.Sprintf("%s-%d", status, seen), Status: status, LastSeen: seen}
			if err := db.UpsertMonitorIncident(ctx, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, ts := range []int64{10, 100} {
		if err := db.AddMonitorDelivery(ctx, MonitorDelivery{ID: fmt.Sprint(ts), CreatedAt: ts}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PruneMonitorHistory(ctx, 100); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListMonitorIncidents(ctx, 200)
	if err != nil || len(rows) != 6 {
		t.Fatalf("history count %d %v", len(rows), err)
	}
	for _, v := range rows {
		if v.LastSeen < 100 && (v.Status == "resolved" || v.Status == "closed") {
			t.Fatalf("old resolved retained %+v", v)
		}
	}
	open, err := db.ListOpenMonitorIncidents(ctx)
	if err != nil || len(open) != 4 {
		t.Fatalf("active lost %d %v", len(open), err)
	}
	deliveries, err := db.ListMonitorDeliveries(ctx, 200)
	if err != nil || len(deliveries) != 1 || deliveries[0].CreatedAt != 100 {
		t.Fatalf("delivery cutoff %+v %v", deliveries, err)
	}
}

func TestMonitorJSONFieldNames(t *testing.T) {
	for _, v := range []any{MonitorIncident{AgentID: "a", FirstSeen: 1, DetailJSON: "{}"}, MonitorDelivery{IncidentID: "i", CreatedAt: 2}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "ID") || strings.Contains(string(b), "Seen") || strings.Contains(string(b), "At") {
			t.Fatalf("non-snake case JSON %s", b)
		}
	}
}

func TestMonitorMetadataWarmup(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	withTimeout(t, "monitor metadata", func() error {
		return db.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
			if _, err := tx.Model(do.MonitorConfig{}).Fields("config_json", "version").Where("id", 1).One(); err != nil {
				return err
			}
			if _, err := tx.Model(do.MonitorIncidents{}).Data(do.MonitorIncidents{Id: "warm", Fingerprint: "warm"}).Insert(); err != nil {
				return err
			}
			_, err := tx.Model(do.MonitorDeliveries{}).Data(do.MonitorDeliveries{Id: "warm", CreatedAt: int64(0)}).Insert()
			return err
		})
	})
}

func TestMonitorConfigCASOpaqueAndBounds(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	got, err := db.GetMonitorConfig(ctx)
	if err != nil || got.ConfigJSON != "{}" || got.SecretsCiphertext != "" || got.Version != 1 {
		t.Fatalf("seed %+v %v", got, err)
	}
	value := MonitorConfig{ConfigJSON: "opaque non-JSON is caller-owned", SecretsCiphertext: "ciphertext", Version: 99}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := db.UpdateMonitorConfig(ctx, value, 1)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("CAS: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("CAS wins %d", wins.Load())
	}
	got, err = db.GetMonitorConfig(ctx)
	if err != nil || got.Version != 2 || got.ConfigJSON != value.ConfigJSON || got.SecretsCiphertext != value.SecretsCiphertext {
		t.Fatalf("updated %+v %v", got, err)
	}
	for _, v := range []MonitorConfig{{ConfigJSON: strings.Repeat("x", MaxMonitorConfigBytes+1)}, {SecretsCiphertext: strings.Repeat("x", MaxMonitorSecretsBytes+1)}} {
		if err := db.UpdateMonitorConfig(ctx, v, 2); err == nil {
			t.Fatal("oversize payload accepted")
		}
	}
	got, _ = db.GetMonitorConfig(ctx)
	if got.Version != 2 {
		t.Fatalf("rejected config changed version: %d", got.Version)
	}
}

func TestMonitorIncidentStableFingerprintAndHistory(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v := MonitorIncident{ID: "first", Fingerprint: "stable", AgentID: "absent-agent", ContainerID: "c", Kind: "cpu", Status: "pending", FirstSeen: 10, LastSeen: 11, DetailJSON: "opaque"}
	if err := db.UpsertMonitorIncident(ctx, v); err != nil {
		t.Fatal(err)
	}
	v.ID = "ignored-new-id"
	v.Status = "firing"
	v.LastSeen = 20
	v.LastNotified = 19
	v.DetailJSON = `{"state":true}`
	if err := db.UpsertMonitorIncident(ctx, v); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListMonitorIncidents(ctx, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != "first" || rows[0].FirstSeen != 10 || rows[0].LastSeen != 20 || rows[0].DetailJSON != v.DetailJSON {
		t.Fatalf("stable row %+v %v", rows, err)
	}
	v.Status = "resolved"
	v.ResolvedAt = 30
	if err := db.UpsertMonitorIncident(ctx, v); err != nil {
		t.Fatal(err)
	}
	open, err := db.ListOpenMonitorIncidents(ctx)
	if err != nil || len(open) != 0 {
		t.Fatalf("open resolved %+v %v", open, err)
	}
	v.Status = "pending"
	v.FirstSeen = 40
	v.LastSeen = 41
	v.ResolvedAt = 0
	if err := db.UpsertMonitorIncident(ctx, v); err != nil {
		t.Fatal(err)
	}
	rows, _ = db.ListMonitorIncidents(ctx, 1)
	if rows[0].FirstSeen != 40 || rows[0].ID != "first" || rows[0].ResolvedAt != 0 {
		t.Fatalf("new episode %+v", rows[0])
	}
	for i := 0; i < 205; i++ {
		v.ID = fmt.Sprintf("i%03d", i)
		v.Fingerprint = v.ID
		v.LastSeen = int64(100 + i)
		if err := db.UpsertMonitorIncident(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = db.ListMonitorIncidents(ctx, 999)
	if err != nil || len(rows) != 200 || rows[0].ID != "i204" {
		t.Fatalf("history cap/order %d %v", len(rows), err)
	}
	open, err = db.ListOpenMonitorIncidents(ctx)
	if err != nil || len(open) != 206 {
		t.Fatalf("open history was truncated: %d %v", len(open), err)
	}
	v.DetailJSON = strings.Repeat("x", MaxMonitorDetailBytes+1)
	if err := db.UpsertMonitorIncident(ctx, v); err == nil {
		t.Fatal("oversize detail accepted")
	}
}

func TestMonitorDeliveriesHistory(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for i := 0; i < 205; i++ {
		v := MonitorDelivery{ID: fmt.Sprintf("d%03d", i), IncidentID: "absent-history", Channel: "mail", Event: "firing", Status: "failed", Error: "redacted", CreatedAt: int64(i)}
		if err := db.AddMonitorDelivery(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.ListMonitorDeliveries(ctx, 500)
	if err != nil || len(rows) != 200 || rows[0].ID != "d204" || rows[0].CreatedAt != 204 || rows[0].Error != "redacted" {
		t.Fatalf("delivery history %+v %v", rows, err)
	}
	rows, err = db.ListMonitorDeliveries(ctx, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("limit %d %v", len(rows), err)
	}
	if err := db.AddMonitorDelivery(ctx, MonitorDelivery{ID: "d204"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate delivery %v", err)
	}
	if err := db.AddMonitorDelivery(ctx, MonitorDelivery{ID: "large", Error: strings.Repeat("x", MaxMonitorErrorBytes+1)}); err == nil {
		t.Fatal("oversize error accepted")
	}
}
