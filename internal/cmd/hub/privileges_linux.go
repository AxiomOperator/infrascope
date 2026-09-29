//go:build linux

package main

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// dropPrivileges lets the container image start as root, hand the data
// directory to an unprivileged user and continue as that user (like the gosu
// entrypoints of common images). The image sets PUID/PGID (default 1000) and
// DATA_DIR; nothing happens unless PUID is set and the process runs as root.
//
// Taking ownership on every start keeps existing installs working: their
// volumes and bind mounts were written by root. Set PUID=0 to keep running as
// root. If the container lacks the capabilities to change ownership or user
// (e.g. cap_drop: ALL), the hub logs a warning and keeps running as root.
func dropPrivileges() {
	uidValue, ok := os.LookupEnv("PUID")
	if !ok || os.Getuid() != 0 {
		return
	}
	uid, err := strconv.Atoi(uidValue)
	if err != nil || uid < 0 {
		log.Printf("Ignoring invalid PUID %q; running as root", uidValue)
		return
	}
	if uid == 0 {
		return
	}
	gid := uid
	if gidValue := os.Getenv("PGID"); gidValue != "" {
		if gid, err = strconv.Atoi(gidValue); err != nil || gid < 0 {
			log.Printf("Ignoring invalid PGID %q; running as root", gidValue)
			return
		}
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "/beszel_data"
	}
	if err := chownTree(dataDir, uid, gid); err != nil {
		log.Printf("Warning: could not give %s to uid %d (%v); running as root", dataDir, uid, err)
		return
	}
	if err := switchUser(uid, gid); err != nil {
		log.Printf("Warning: could not switch to uid %d (%v); running as root", uid, err)
	}
}

// chownTree gives dir and everything in it to uid:gid, skipping entries that
// already have that owner. A missing dir is created.
func chownTree(dir string, uid, gid int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) == uid && int(stat.Gid) == gid {
			return nil
		}
		return os.Lchown(path, uid, gid)
	})
}

// switchUser changes the user and group of all threads of the process.
func switchUser(uid, gid int) error {
	if err := syscall.Setgroups(nil); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	return nil
}
