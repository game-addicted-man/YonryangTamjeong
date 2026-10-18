package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is one item returned by listDir (platform specific).
type Entry struct {
	Name        string
	IsDir       bool
	Size        int64 // bytes actually used on disk (0 for cloud-only placeholders)
	Logical     int64 // logical file length
	MTime       int64 // last write time, unix seconds
	SkipRecurse bool  // junction / symbolic link directory: never follow
	CloudOnly   bool  // OneDrive-style placeholder that is not stored locally
}

var (
	errDenied   = errors.New("access denied")
	errNotFound = errors.New("not found")
	errOther    = errors.New("cannot read")
)

// ---- file categories ----

const (
	TVideo = iota
	TImage
	TAudio
	TDocument
	TArchive
	TInstaller
	TDiskImage
	TGame
	TRecycle
	TSystem
	TOther
	NTypes
)

var extType = map[string]int{}

func init() {
	add := func(t int, exts string) {
		for _, e := range strings.Fields(exts) {
			extType[e] = t
		}
	}
	add(TVideo, "mp4 mkv avi mov wmv flv webm ts m2ts mts m4v mpg mpeg 3gp vob rmvb asf")
	add(TImage, "jpg jpeg png gif bmp heic heif webp tif tiff raw cr2 cr3 nef arw dng psd orf rw2 jfif")
	add(TAudio, "mp3 flac wav aac m4a ogg wma opus ape aiff aif mid midi")
	add(TDocument, "pdf doc docx xls xlsx ppt pptx hwp hwpx txt rtf odt ods odp csv md epub cell show key pages numbers")
	add(TArchive, "zip rar 7z alz egg tar gz bz2 xz tgz cab zst lz4 lzh")
	add(TInstaller, "exe msi msix msixbundle appx appxbundle apk")
	add(TDiskImage, "iso img vhd vhdx vmdk vdi dmg wim esd qcow2 avhdx")
}

func extOf(name string) string {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 || dot == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[dot+1:])
}

func typeByName(name string) int {
	if t, ok := extType[extOf(name)]; ok {
		return t
	}
	return TOther
}

// ---- paths ----

const sepChar = os.PathSeparator

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	last := dir[len(dir)-1]
	if last == '\\' || last == '/' {
		return dir + name
	}
	return dir + string(os.PathSeparator) + name
}

// normPath makes a path comparable (case-insensitive on Windows, no trailing separator).
func normPath(p string) string {
	p = filepath.Clean(p)
	if os.PathSeparator == '\\' {
		p = strings.ToLower(p)
	}
	return strings.TrimRight(p, `\/`)
}

func isUnder(child, parent string) bool {
	c, p := normPath(child), normPath(parent)
	if c == p {
		return true
	}
	return strings.HasPrefix(c, p+string(os.PathSeparator))
}

// globMatch is a case-insensitive wildcard match supporting * and ?.
func globMatch(pattern, text string) bool {
	p := []rune(strings.ToLower(pattern))
	t := []rune(strings.ToLower(text))
	pi, ti, starP, starT := 0, 0, -1, 0
	for ti < len(t) {
		if pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]) {
			pi++
			ti++
		} else if pi < len(p) && p[pi] == '*' {
			starP = pi
			pi++
			starT = ti
		} else if starP >= 0 {
			pi = starP + 1
			starT++
			ti = starT
		} else {
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func hasWildcard(s string) bool { return strings.ContainsAny(s, "*?") }

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// ---- logging (best effort, to %LOCALAPPDATA%\DiskDetective\log.txt) ----

var (
	logMu   sync.Mutex
	logFile *os.File
)

func initLog() {
	dir := appDataDir()
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "log.txt")
	if st, err := os.Stat(path); err == nil && st.Size() > 512*1024 {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		logFile = f
	}
}

func logf(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, args...) + "\n"
	if logFile != nil {
		_, _ = logFile.WriteString(line)
	}
	if os.Getenv("DD_DEBUG") != "" {
		fmt.Fprint(os.Stderr, line)
	}
}
