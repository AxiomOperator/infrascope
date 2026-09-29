//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestChownTreeCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "data.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Chowning to the current owner is always permitted.
	uid, gid := os.Getuid(), os.Getgid()
	if err := chownTree(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "sub", "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	if stat := info.Sys().(*syscall.Stat_t); int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("owner = %d:%d", stat.Uid, stat.Gid)
	}
	missing := filepath.Join(t.TempDir(), "new")
	if err := chownTree(missing, uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Fatal("missing data dir must be created", err)
	}
}

func TestDropPrivilegesNoopWhenNotRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	t.Setenv("PUID", "1000")
	t.Setenv("DATA_DIR", filepath.Join(t.TempDir(), "data"))
	dropPrivileges()
	if _, err := os.Stat(os.Getenv("DATA_DIR")); !os.IsNotExist(err) {
		t.Fatal("non-root processes must not touch the data dir")
	}
}
