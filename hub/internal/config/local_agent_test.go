package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovedLocalAgentTokenTOMLRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "removed.toml")
	if err := os.WriteFile(path, []byte("[auth]\nlocal_agent_token='private-token-value'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "未知项") || !strings.Contains(err.Error(), "auth.local_agent_token") {
		t.Fatalf("removed key accepted: %v", err)
	}
	if strings.Contains(err.Error(), "private-token-value") {
		t.Fatal("unknown key error leaked value")
	}
}
