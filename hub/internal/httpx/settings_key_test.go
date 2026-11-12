package httpx

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSettingsKeyPersistenceAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	first, err := loadSettingsKey(path, "")
	if err != nil || len(first) != 32 {
		t.Fatalf("create key: %v", err)
	}
	second, err := loadSettingsKey(path, "")
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("restart changed key: %v", err)
	}
	if err := os.WriteFile(path+".settings.key", []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettingsKey(path, ""); err == nil {
		t.Fatal("silently replaced corrupt key")
	}
	raw, err := os.ReadFile(path + ".settings.key")
	if err != nil || string(raw) != "corrupt" {
		t.Fatal("corrupt key was overwritten")
	}
}
func TestSettingsKeyConfiguredPathAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets", "identity.key")
	first, err := loadSettingsKey(filepath.Join(dir, "hub.db"), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hub.db.settings.key")); !os.IsNotExist(err) {
		t.Fatal("ignored configured key path")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm() != 0600 {
			t.Fatalf("key permission: %v", info.Mode())
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSettingsKey("", path); err == nil {
			t.Fatal("accepted globally readable key")
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(dir, "alias.key")
		if err := os.Symlink(path, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSettingsKey("", alias); err == nil {
			t.Fatal("accepted symlink key")
		}
	}
	again, err := loadSettingsKey("", path)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatalf("configured key changed: %v", err)
	}
}
