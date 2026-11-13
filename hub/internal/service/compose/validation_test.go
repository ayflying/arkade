package compose

import (
	"arkade/hub/internal/store"
	"arkade/protocol/composehash"
	"arkade/protocol/envelope"
	"strings"
	"testing"
	"time"
)

func TestHashValidationAndViews(t *testing.T) {
	if ProjectHash("abc", "") != composehash.Project("abc", "") || ProjectHash("abc", "") == "ba7816bf8f01cfea" {
		t.Fatal("project hash must use shared project domain even with empty env")
	}
	if ProjectHash("a", "bc") == ProjectHash("ab", "c") {
		t.Fatal("file boundary collision")
	}
	for _, name := range []string{"app", "a_b-1", "1"} {
		if !ValidName(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"", "../x", "App", "-x", "a/b", strings.Repeat("a", 65)} {
		if ValidName(name) {
			t.Fatal(name)
		}
	}
	if ValidateEnv("A=\x00") == nil || ValidateEnv(strings.Repeat("x", MaxContentBytes+1)) == nil || ValidateContent(" \n") == nil {
		t.Fatal("validation missing")
	}
	out := TruncateOutput(" prefix " + strings.Repeat("界", 4001))
	if !strings.HasPrefix(out, "…（输出过长，已截断前部）\n") || !strings.HasSuffix(out, strings.Repeat("界", 4000)) {
		t.Fatal("tail truncation changed")
	}
	rec := store.ComposeStack{AgentID: "agent", Name: "app", ConfigHash: "local", Version: 1, UpdatedAt: time.Unix(123, 0)}
	v := StackViewOf(rec, nil)
	if v.SyncedAt != 0 || v.Status != "offline" || v.Services == nil || v.UpdatedAt != 123 {
		t.Fatalf("offline %+v", v)
	}
	v = StackViewOf(rec, &envelope.ComposeProjectSummary{ConfigHash: "other"})
	if !v.Drift || v.Services == nil {
		t.Fatal(v)
	}
	views := ExternalViews([]envelope.ComposeProjectSummary{{Name: "z"}, {Name: "a"}, {Name: "known"}}, map[string]struct{}{"known": {}})
	if len(views) != 2 || views[0].Name != "a" || views[0].Services == nil || views[0].Status != "unknown" {
		t.Fatal(views)
	}
}
