//go:build !windows

// Non-Windows implementations. They exist so the scanner and the UI can be
// developed and tested on Linux; the real program targets Windows.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func platformInit() {}

func platformName() string { return "other" }

func appDataDir() string {
	if d := os.Getenv("DD_APPDATA"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "diskdetective")
}

func listDir(dir string, buf []Entry) ([]Entry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrPermission):
			return buf, errDenied
		case errors.Is(err, fs.ErrNotExist):
			return buf, errNotFound
		}
		if len(ents) == 0 {
			return buf, errOther
		}
	}
	for _, de := range ents {
		e := Entry{Name: de.Name()}
		if de.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, de.Name())); err == nil && st.IsDir() {
				e.IsDir = true
				e.SkipRecurse = true
			}
			buf = append(buf, e)
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		e.MTime = info.ModTime().Unix()
		if de.IsDir() {
			e.IsDir = true
		} else if info.Mode().IsRegular() {
			e.Logical = info.Size()
			e.Size = info.Size()
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blocks*512 < info.Size()/2 && os.Getenv("DD_SPARSE") != "" {
				e.Size = st.Blocks * 512
			}
		}
		buf = append(buf, e)
	}
	return buf, nil
}

func fakeDrives() []string {
	if v := os.Getenv("DD_DRIVES"); v != "" {
		return strings.Split(v, ";")
	}
	return []string{"/"}
}

func isDriveRoot(p string) bool {
	for _, d := range fakeDrives() {
		if filepath.Clean(d) == filepath.Clean(p) {
			return true
		}
	}
	return false
}

func diskUsage(root string) (total, free int64, ok bool) {
	for i, d := range fakeDrives() {
		if filepath.Clean(d) != filepath.Clean(root) {
			continue
		}
		if v := os.Getenv(fmt.Sprintf("DD_FAKE_TOTAL_%d", i)); v != "" {
			var t, f int64
			if _, err := fmt.Sscanf(v, "%d/%d", &t, &f); err == nil {
				return t, f, true
			}
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return 0, 0, false
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize), true
}

func listDrives() []driveInfo {
	var out []driveInfo
	for i, d := range fakeDrives() {
		total, free, ok := diskUsage(d)
		if !ok {
			continue
		}
		label := os.Getenv(fmt.Sprintf("DD_DRIVE_LABEL_%d", i))
		out = append(out, driveInfo{Path: d, Label: label, FS: "ext4", Total: total, Free: free, Type: "fixed"})
	}
	return out
}

func isAdmin() bool { return os.Getenv("DD_ADMIN") == "1" }

func relaunchAsAdmin() bool { return false }

func knownFolder(which string) string {
	home, _ := os.UserHomeDir()
	switch which {
	case "downloads":
		if v := os.Getenv("DD_DOWNLOADS"); v != "" {
			return v
		}
		return filepath.Join(home, "Downloads")
	case "documents":
		if v := os.Getenv("DD_DOCUMENTS"); v != "" {
			return v
		}
		return filepath.Join(home, "Documents")
	}
	return ""
}

func steamPath() string { return os.Getenv("DD_STEAM") }

func recycleBinParts() ([]JunkPart, bool) {
	dir := os.Getenv("DD_TRASH")
	if dir == "" || !dirExists(dir) {
		return nil, false
	}
	b, f, _ := sumDir(dir, nil, nil)
	if b == 0 {
		return nil, true
	}
	return []JunkPart{{Label: "C: 드라이브 휴지통", Path: "shell:RecycleBinFolder", Bytes: b, Files: f}}, true
}

func fileID(path string) (string, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%x:%x", s.Dev, s.Ino), true
	}
	return "", false
}

func openInExplorer(path string, selectFile bool) error {
	logf("open %q select=%v", path, selectFile)
	if os.Getenv("DD_NO_EXEC") != "" {
		return nil
	}
	target := path
	if selectFile {
		target = filepath.Dir(path)
	}
	return exec.Command("xdg-open", target).Start()
}

func launchTool(what string) error {
	logf("launch %s", what)
	return nil
}

func canPickFolder() bool { return false }

func pickFolder(owner uintptr, title string) string { return "" }

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
