package compose

import (
	"slices"
	"strings"
	"testing"
)

func TestMergeEnvPreservesUnchangedBytes(t *testing.T) {
	original := "\ufeff# settings\r\nPASSWORD='keep\r\nNOT_A_KEY=inside'\r\nexport LOG_LEVEL = info # note\r\nX=${OTHER}"
	got, keys, err := MergeEnv(original, map[string]string{"LOG_LEVEL": "debug", "TZ": "Asia/Shanghai"})
	want := "\ufeff# settings\r\nPASSWORD='keep\r\nNOT_A_KEY=inside'\r\nexport LOG_LEVEL = \"debug\" # note\r\nX=${OTHER}\r\nTZ=\"Asia/Shanghai\"\r\n"
	if err != nil || got != want || !slices.Equal(keys, []string{"LOG_LEVEL", "TZ"}) {
		t.Fatalf("unexpected merge: %q, %v, %v", got, keys, err)
	}
}

func TestMergeEnvLiteralAndNoOp(t *testing.T) {
	value := "${SECRET} # value\nINJECTED=yes\\\""
	got, keys, err := MergeEnv("X=${SECRET}\n", map[string]string{"X": value})
	if err != nil || got != "X=\"$${SECRET} # value\\nINJECTED=yes\\\\\\\"\"\n" || !slices.Equal(keys, []string{"X"}) {
		t.Fatalf("unexpected literal: %q, %v, %v", got, keys, err)
	}
	next, keys, err := MergeEnv(got, map[string]string{"X": value})
	if err != nil || next != got || len(keys) != 0 {
		t.Fatalf("literal repeated update should be a no-op: %q, %v, %v", next, keys, err)
	}
}

func TestMergeEnvRejectsAmbiguousOrInvalidInput(t *testing.T) {
	for _, tc := range []struct{ existing, key, value string }{
		{"X=private-marker\nX=other\n", "Y", "valid"},
		{"X='private-marker\n", "Y", "valid"},
		{"X=private-marker\n", "BAD-NAME", "valid"},
		{"X=private-marker\n", "Y", "private-marker\x00"},
	} {
		got, keys, err := MergeEnv(tc.existing, map[string]string{tc.key: tc.value})
		if err == nil || got != "" || len(keys) != 0 || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("expected safe failure with no partial output")
		}
	}
}
