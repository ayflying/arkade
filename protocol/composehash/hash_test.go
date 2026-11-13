package composehash

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestProjectDomainVectors(t *testing.T) {
	for _, v := range []struct{ content, env, input string }{
		{"abc", "", "arkade.compose.project.v1\x00content\x003\x00abcenv\x000\x00"},
		{"a", "bc", "arkade.compose.project.v1\x00content\x001\x00aenv\x002\x00bc"},
		{"ab", "c", "arkade.compose.project.v1\x00content\x002\x00abenv\x001\x00c"},
	} {
		sum := sha256.Sum256([]byte(v.input))
		want := hex.EncodeToString(sum[:])[:16]
		if got := Project(v.content, v.env); got != want {
			t.Fatalf("project domain vector: got %s want %s", got, want)
		}
	}
	if Project("abc", "") == "ba7816bf8f01cfea" {
		t.Fatal("project hash regressed to file hash")
	}
	if Project("a", "bc") == Project("ab", "c") {
		t.Fatal("ambiguous file boundary")
	}
}
