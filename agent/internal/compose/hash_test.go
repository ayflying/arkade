package compose

import (
	"arkade/protocol/composehash"
	"testing"
)

func TestHashProjectUsesSharedContract(t *testing.T) {
	for _, v := range []struct{ content, env string }{{"services: {}\n", ""}, {"a", "bc"}, {"ab", "c"}} {
		if got, want := HashProject(v.content, v.env), composehash.Project(v.content, v.env); got != want {
			t.Fatalf("project hash %s want %s", got, want)
		}
	}
}
