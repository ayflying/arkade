package agentlink

import (
	"strings"
	"testing"

	"arkade/protocol/envelope"
	"crypto/sha256"
	"encoding/hex"
)

func TestComposeMigrationRealHashWireContract(t *testing.T) {
	config := "services:\n  app:\n    image: alpine:3.20\n"
	// Agent ComposeProjectHash uses the config SHA256 prefix when env is empty.
	sum := sha256.Sum256([]byte(config))
	hash := hex.EncodeToString(sum[:])[:16]
	if len(hash) != 16 {
		t.Fatalf("real compose hash length=%d", len(hash))
	}
	// Decode exact Agent wire shapes through the same decoder used by Ops RPC.
	var plan envelope.ComposeMigrationPlanResult
	if err := decodeInto([]byte(`{"name":"demo","config":"services: {}","config_hash":"`+hash+`","images":[],"volumes":[],"busy":false}`), &plan); err != nil {
		t.Fatal(err)
	}
	planReq := envelope.ComposeMigrationPlanPayload{Name: "demo"}
	if err := validateComposeMigrationPlan(planReq, plan); err != nil {
		t.Fatalf("real plan hash rejected: %v", err)
	}
	var imported envelope.ComposeImportResult
	if err := decodeInto([]byte(`{"name":"demo","config_hash":"`+hash+`"}`), &imported); err != nil {
		t.Fatal(err)
	}
	importReq := envelope.ComposeImportPayload{Name: "demo", ExpectHash: hash}
	if err := validateComposeImport(importReq, imported); err != nil {
		t.Fatalf("real import hash rejected: %v", err)
	}
	for _, invalid := range []string{"", strings.Repeat("a", 15), strings.Repeat("a", 17), strings.Repeat("a", 64), "0123456789abcdeg"} {
		plan.ConfigHash = invalid
		if err := validateComposeMigrationPlan(planReq, plan); err == nil {
			t.Errorf("plan accepted invalid hash %q", invalid)
		}
		imported.ConfigHash = invalid
		if err := validateComposeImport(importReq, imported); err == nil {
			t.Errorf("import accepted invalid hash %q", invalid)
		}
	}
	imported.ConfigHash = strings.Repeat("b", 16)
	if err := validateComposeImport(importReq, imported); err == nil {
		t.Fatal("mismatched hash accepted")
	}
	if !isHexSHA256(strings.Repeat("a", 64)) || isHexSHA256(hash) {
		t.Fatal("archive SHA256 contract changed")
	}
}
