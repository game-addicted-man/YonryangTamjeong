//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modShell32  = windows.NewLazySystemDLL("shell32.dll")
	modOle32    = windows.NewLazySystemDLL("ole32.dll")

	procFindFirstFileExW       = modKernel32.NewProc("FindFirstFileExW")
	procFindNextFileW          = modKernel32.NewProc("FindNextFileW")
	procFindClose              = modKernel32.NewProc("FindClose")
	procGetCompressedFileSizeW = modKernel32.NewProc("GetCompressedFileSizeW")
	procSHQueryRecycleBinW     = modShell32.NewProc("SHQueryRecycleBinW")
	procSHBrowseForFolderW     = modShell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListEx  = modShell32.NewProc("SHGetPathFromIDListEx")
	procCoTaskMemFree          = modOle32.NewProc("CoTaskMemFree")
)

// win32FindData mirrors WIN32_FIND_DATAW exactly (592 bytes). The struct in
// x/sys/windows has shortened name buffers, so we declare our own.
type win32FindData struct {
	FileAttributes    uint32
	CreationTime      windows.Filetime
	LastAccessTime    windows.Filetime
	LastWriteTime     windows.Filetime
	FileSizeHigh      uint32
	FileSizeLow       uint32
	Reserved0         uint32 // reparse tag when FILE_ATTRIBUTE_REPARSE_POINT is set
	Reserved1         uint32
	FileName          [260]uint16
	AlternateFileName [14]uint16
}

const (
	attrDirectory    = 0x10
	attrSparse       = 0x200
	attrReparse      = 0x400
	attrCompressed   = 0x800
	attrOffline      = 0x1000
	attrRecallOnOpen = 0x40000
	attrRecallOnData = 0x400000

	reparseNameSurrogate = 0x20000000 // junctions, symlinks: they point somewhere else
	tagWOF               = 0x80000017 // CompactOS / compact.exe compressed files
	tagDedup             = 0x80000013

	findExInfoBasic       = 1
	findExSearchNameMatch = 0
	findFirstExLargeFetch = 2
)

func platformInit() {
	// Never show "There is no disk in the drive" dialogs while probing drives.
	windows.SetErrorMode(windows.SEM_FAILCRITICALERRORS | windows.SEM_NOOPENFILEERRORBOX)
}

func platformName() string { return "windows" }

func appDataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "DiskDetective")
}

// longPath adds the \\?\ prefix so paths longer than 260 characters work.
func longPath(p string) string {
	if strings.HasPrefix(p, `\\?\`) {
		return p
	}
	if strings.HasPrefix(p, `\\`) {
		return `\\?\UNC\` + p[2:]
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return `\\?\` + strings.ReplaceAll(p, "/", `\`)
	}
	return p
}

func filetimeToUnix(ft windows.Filetime) int64 {
	t := int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
	if t <= 0 {
		return 0
	}
	return (t - 116444736000000000) / 10000000
}

func listDir(dir string, buf []Entry) ([]Entry, error) {
	pattern := longPath(dir)
	if !strings.HasSuffix(pattern, `\`) {
		pattern += `\`
	}
	pattern += "*"
	p, err := windows.UTF16PtrFromString(pattern)
	if err != nil {
		return buf, errOther
	}
	var fd win32FindData
	h, _, e1 := procFindFirstFileExW.Call(
		uintptr(unsafe.Pointer(p)),
		findExInfoBasic,
		uintptr(unsafe.Pointer(&fd)),
		findExSearchNameMatch,
		0,
		findFirstExLargeFetch,
	)
	if windows.Handle(h) == windows.InvalidHandle {
		var errno syscall.Errno
		if errors.As(e1, &errno) {
			switch errno {
			case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_NO_MORE_FILES:
				return buf, nil // empty directory (e.g. an empty drive root)
			case windows.ERROR_ACCESS_DENIED, windows.ERROR_PRIVILEGE_NOT_HELD:
				return buf, errDenied
			case windows.ERROR_PATH_NOT_FOUND, windows.ERROR_DIRECTORY, windows.ERROR_INVALID_NAME:
				return buf, errNotFound
			}
		}
		return buf, errOther
	}
	defer procFindClose.Call(h)
	for {
		name := windows.UTF16ToString(fd.FileName[:])
		if name != "." && name != ".." && name != "" {
			buf = append(buf, makeEntry(dir, name, &fd))
		}
		r, _, _ := procFindNextFileW.Call(h, uintptr(unsafe.Pointer(&fd)))
		if r == 0 {
			break
		}
	}
	return buf, nil
}

func makeEntry(dir, name string, fd *win32FindData) Entry {
	e := Entry{Name: name, MTime: filetimeToUnix(fd.LastWriteTime)}
	a := fd.FileAttributes
	if a&attrDirectory != 0 {
		e.IsDir = true
		if a&attrReparse != 0 && fd.Reserved0&reparseNameSurrogate != 0 {
			e.SkipRecurse = true
		}
		return e
	}
	size := int64(fd.FileSizeHigh)<<32 | int64(fd.FileSizeLow)
	e.Logical = size
	switch {
	case a&(attrOffline|attrRecallOnOpen|attrRecallOnData) != 0:
		e.CloudOnly = true // OneDrive "online only" file: takes no local space
	case a&(attrSparse|attrCompressed) != 0 || (a&attrReparse != 0 && (fd.Reserved0 == tagWOF || fd.Reserved0 == tagDedup)):
		e.Size = compressedSize(joinPath(dir, name), size)
	case a&attrReparse != 0 && fd.Reserved0&reparseNameSurrogate != 0:
		e.Size = 0 // file symlink: the target is counted where it lives
		e.Logical = 0
	default:
		e.Size = size
	}
	return e
}

func compressedSize(path string, fallback int64) int64 {
	p, err := windows.UTF16PtrFromString(longPath(path))
	if err != nil {
		return fallback
	}
	var high uint32
	low, _, e := procGetCompressedFileSizeW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&high)))
	if uint32(low) == 0xFFFFFFFF {
		var errno syscall.Errno
		if errors.As(e, &errno) && errno != 0 {
			return fallback
		}
	}
	v := int64(high)<<32 | int64(uint32(low))
	if v < 0 || v > fallback*2+1<<20 {
		return fallback
	}
	return v
}

func isDriveRoot(p string) bool {
	return len(p) == 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

func diskUsage(root string) (total, free int64, ok bool) {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0, false
	}
	var avail, tot, totFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &tot, &totFree); err != nil {
		return 0, 0, false
	}
	return int64(tot), int64(totFree), true
}

func listDrives() []driveInfo {
	var out []driveInfo
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return out
	}
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		dt := windows.GetDriveType(rp)
		if dt != windows.DRIVE_FIXED && dt != windows.DRIVE_REMOVABLE {
			continue
		}
		total, free, ok := diskUsage(root)
		if !ok || total <= 0 {
			continue
		}
		label := make([]uint16, windows.MAX_PATH+1)
		fs := make([]uint16, windows.MAX_PATH+1)
		_ = windows.GetVolumeInformation(rp, &label[0], uint32(len(label)), nil, nil, nil, &fs[0], uint32(len(fs)))
		kind := "fixed"
		if dt == windows.DRIVE_REMOVABLE {
			kind = "removable"
		}
		out = append(out, driveInfo{Path: root, Label: windows.UTF16ToString(label), FS: windows.UTF16ToString(fs),
			Total: total, Free: free, Type: kind})
	}
	return out
}

func isAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func relaunchAsAdmin() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	var args []string
	for _, a := range os.Args[1:] {
		if a == "-browser" || a == "--browser" { // keep the window mode
			args = append(args, a)
		}
	}
	argp, _ := windows.UTF16PtrFromString(strings.Join(args, " "))
	cwd, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	err = windows.ShellExecute(0, verb, file, argp, cwd, windows.SW_SHOWNORMAL)
	if err != nil {
		logf("runas failed: %v", err)
		return false
	}
	return true
}

func knownFolder(which string) string {
	var id *windows.KNOWNFOLDERID
	fallback := ""
	profile := os.Getenv("USERPROFILE")
	switch which {
	case "downloads":
		id = windows.FOLDERID_Downloads
		fallback = filepath.Join(profile, "Downloads")
	case "documents":
		id = windows.FOLDERID_Documents
		fallback = filepath.Join(profile, "Documents")
	default:
		return ""
	}
	p, err := windows.KnownFolderPath(id, 0)
	if err != nil || p == "" {
		return fallback
	}
	return p
}

func steamPath() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Valve\Steam`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, err := k.GetStringValue("SteamPath")
	if err != nil || s == "" {
		return ""
	}
	return filepath.Clean(strings.ReplaceAll(s, "/", `\`))
}

// queryRecycleBin asks the shell for the size of one drive's recycle bin.
func queryRecycleBin(root string) (size, items int64, ok bool) {
	// SHQUERYRBINFO is packed to 1 byte on 32-bit Windows and naturally aligned on 64-bit.
	var buf [3]uint64
	b := (*[24]byte)(unsafe.Pointer(&buf[0]))
	off := 8
	if unsafe.Sizeof(uintptr(0)) == 4 {
		off = 4
	}
	*(*uint32)(unsafe.Pointer(&b[0])) = uint32(off + 16)
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0, false
	}
	hr, _, _ := procSHQueryRecycleBinW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&buf[0])))
	if int32(hr) != 0 {
		return 0, 0, false
	}
	size = *(*int64)(unsafe.Pointer(&b[off]))
	items = *(*int64)(unsafe.Pointer(&b[off+8]))
	return size, items, size >= 0 && items >= 0
}

func recycleBinParts() ([]JunkPart, bool) {
	var parts []JunkPart
	sid := ""
	if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		sid = tu.User.Sid.String()
	}
	for _, d := range listDrives() {
		size, items, ok := queryRecycleBin(d.Path)
		if !ok && sid != "" { // fallback: sum our own recycle bin folder
			dir := joinPath(d.Path, `$Recycle.Bin\`+sid)
			if dirExists(dir) {
				size, items, _ = sumDir(dir, nil, nil)
				ok = true
			}
		}
		if !ok || (size == 0 && items == 0) {
			continue
		}
		parts = append(parts, JunkPart{Label: d.Path[:2] + " 드라이브 휴지통", Path: "shell:RecycleBinFolder", Bytes: size, Files: items})
	}
	return parts, true
}

// fileID identifies the physical file, so hard links are not reported as duplicates.
func fileID(path string) (string, bool) {
	p, err := windows.UTF16PtrFromString(longPath(path))
	if err != nil {
		return "", false
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", false
	}
	return fmt.Sprintf("%x:%x:%x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), true
}

func explorerPath() string {
	win := os.Getenv("SystemRoot")
	if win == "" {
		win = `C:\Windows`
	}
	return filepath.Join(win, "explorer.exe")
}

func openInExplorer(path string, selectFile bool) error {
	exe := explorerPath()
	var cmdline string
	switch {
	case strings.HasPrefix(path, "shell:"):
		cmdline = `explorer.exe ` + path
	case selectFile:
		cmdline = `explorer.exe /select,"` + path + `"`
	case isDriveRoot(path): // C:\ has no spaces, and a quote right after \ would confuse parsing
		cmdline = `explorer.exe ` + path
	default:
		cmdline = `explorer.exe "` + path + `"`
	}
	cmd := exec.Command(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdline}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func shellOpen(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, t, nil, nil, windows.SW_SHOWNORMAL)
}

func launchTool(what string) error {
	win := os.Getenv("SystemRoot")
	if win == "" {
		win = `C:\Windows`
	}
	switch what {
	case "cleanmgr":
		return shellOpen(filepath.Join(win, "System32", "cleanmgr.exe"))
	case "storage":
		return shellOpen("ms-settings:storagesense")
	case "apps":
		return shellOpen("ms-settings:appsfeatures")
	case "recyclebin":
		return openInExplorer("shell:RecycleBinFolder", false)
	case "sysprotect":
		return shellOpen(filepath.Join(win, "System32", "SystemPropertiesProtection.exe"))
	case "temp":
		return openInExplorer(os.TempDir(), false)
	}
	return errors.New("알 수 없는 도구")
}

func canPickFolder() bool { return true }

type browseInfo struct {
	Owner       uintptr
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Callback    uintptr
	LParam      uintptr
	Image       int32
}

// pickFolder shows the classic folder picker. It runs on its own locked OS
// thread with COM initialised, because HTTP handlers run on arbitrary threads.
func pickFolder(owner uintptr, title string) string {
	ch := make(chan string, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer func() {
			if r := recover(); r != nil {
				logf("pickFolder panic: %v", r)
				ch <- ""
			}
		}()
		// S_FALSE (already initialised on this thread) still needs a matching CoUninitialize
		if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil || err == syscall.Errno(1) {
			defer windows.CoUninitialize()
		}
		display := make([]uint16, windows.MAX_PATH+1)
		t, _ := windows.UTF16PtrFromString(title)
		bi := browseInfo{Owner: owner, DisplayName: &display[0], Title: t,
			Flags: 0x0001 | 0x0040 | 0x0200} // RETURNONLYFSDIRS | NEWDIALOGSTYLE | NONEWFOLDERBUTTON
		pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
		if pidl == 0 {
			ch <- ""
			return
		}
		defer procCoTaskMemFree.Call(pidl)
		path := make([]uint16, 32768) // long paths are allowed
		r, _, _ := procSHGetPathFromIDListEx.Call(pidl, uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)), 0)
		if r == 0 {
			ch <- ""
			return
		}
		ch <- windows.UTF16ToString(path)
	}()
	return <-ch
}

func fatal(msg string) {
	logf("fatal: %s", msg)
	t, _ := windows.UTF16PtrFromString(msg)
	c, _ := windows.UTF16PtrFromString("용량 탐정")
	_, _ = windows.MessageBox(0, t, c, windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
