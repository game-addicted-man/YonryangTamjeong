//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modUser32 = windows.NewLazySystemDLL("user32.dll")

	procRegisterClassExW      = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW       = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW        = modUser32.NewProc("DefWindowProcW")
	procShowWindow            = modUser32.NewProc("ShowWindow")
	procUpdateWindow          = modUser32.NewProc("UpdateWindow")
	procGetMessageW           = modUser32.NewProc("GetMessageW")
	procTranslateMessage      = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW      = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage       = modUser32.NewProc("PostQuitMessage")
	procDestroyWindow         = modUser32.NewProc("DestroyWindow")
	procLoadCursorW           = modUser32.NewProc("LoadCursorW")
	procGetDpiForSystem       = modUser32.NewProc("GetDpiForSystem")
	procSystemParametersInfoW = modUser32.NewProc("SystemParametersInfoW")
	procSetForegroundWindow   = modUser32.NewProc("SetForegroundWindow")
	procExtractIconExW        = modShell32.NewProc("ExtractIconExW")
)

const (
	wmDestroy        = 0x0002
	wmMove           = 0x0003
	wmSize           = 0x0005
	wmSetFocus       = 0x0007
	wmClose          = 0x0010
	wmGetMinMaxInfo  = 0x0024
	wmMoving         = 0x0216
	wsOverlappedWnd  = 0x00CF0000
	swShow           = 5
	idcArrow         = 32512
	spiGetWorkArea   = 0x0030
	colorWindowBrush = 6 // COLOR_WINDOW + 1
)

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type point struct{ X, Y int32 }

type msgT struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type minMaxInfo struct {
	Reserved, MaxSize, MaxPosition, MinTrackSize, MaxTrackSize point
}

type rect struct{ Left, Top, Right, Bottom int32 }

var (
	mainChromium    *edge.Chromium
	controllerReady atomic.Bool
	navigated       atomic.Bool
	fellBack        atomic.Bool
	uiScale         = 1.0
	minTrack        point
)

func runUI(app *App, srv *Server, forceBrowser bool) {
	if forceBrowser || preferBrowser() || !webview2Available() {
		app.mode.Store("browser")
		runBrowserUI(app, srv)
		return
	}
	app.mode.Store("app")
	runAppWindow(app, srv)
}

func webview2Available() (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			logf("webview2 check panic: %v", r)
			ok = false
		}
	}()
	v, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString("")
	if err != nil || v == "" {
		logf("webview2 not available: %v", err)
		return false
	}
	logf("webview2 runtime %s", v)
	return true
}

func markerPath() string { return filepath.Join(appDataDir(), "webview2-starting") }

// beforeExit runs when the program quits on its own (e.g. to restart as administrator).
func beforeExit() { _ = os.Remove(markerPath()) }

// preferBrowser returns true when the app window failed recently, so the
// program opens in a browser window instead of failing again.
func preferBrowser() bool {
	dir := appDataDir()
	if dir == "" {
		return false
	}
	if st, err := os.Stat(filepath.Join(dir, "use-browser")); err == nil && time.Since(st.ModTime()) < 7*24*time.Hour {
		return true
	}
	data, err := os.ReadFile(markerPath())
	if err != nil {
		return false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid > 0 && pid != os.Getpid() && processAlive(uint32(pid)) {
		return false // another copy is starting right now
	}
	_ = os.Remove(markerPath())
	logf("previous app window start did not finish; using browser mode this time")
	return true
}

func processAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

func fallbackToBrowser(remember bool) {
	if !fellBack.CompareAndSwap(false, true) {
		select {} // another path is already switching to the browser window
	}
	_ = os.Remove(markerPath())
	if remember {
		_ = os.WriteFile(filepath.Join(appDataDir(), "use-browser"), []byte("webview2 failed"), 0o644)
	}
	if exe, err := os.Executable(); err == nil {
		if err := exec.Command(exe, "--browser").Start(); err == nil {
			os.Exit(0)
		}
	}
	fatal("화면을 열지 못했어요. 프로그램을 다시 실행해 주세요.")
}

func systemScale() float64 {
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi >= 96 && dpi < 1000 {
			return float64(dpi) / 96
		}
	}
	return 1
}

func workArea() rect {
	var r rect
	_, _, _ = procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0)
	if r.Right-r.Left < 200 || r.Bottom-r.Top < 200 {
		r = rect{0, 0, 1366, 728}
	}
	return r
}

func appIcons() (large, small uintptr) {
	exe, err := os.Executable()
	if err != nil {
		return 0, 0
	}
	p, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, 0
	}
	_, _, _ = procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1)
	return large, small
}

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmSize:
		if mainChromium != nil && controllerReady.Load() {
			mainChromium.Resize()
		}
		return 0
	case wmMove, wmMoving:
		if mainChromium != nil && controllerReady.Load() {
			_ = mainChromium.NotifyParentWindowPositionChanged()
		}
	case wmSetFocus:
		// keyboard focus belongs inside the web page (Backspace, Esc, Tab)
		if mainChromium != nil && navigated.Load() {
			mainChromium.Focus()
		}
		return 0
	case wmGetMinMaxInfo:
		mmi := (*minMaxInfo)(unsafe.Pointer(lparam))
		mmi.MinTrackSize = minTrack
		return 0
	case wmClose:
		if !controllerReady.Load() {
			// closed while WebView2 was still starting: just quit cleanly
			beforeExit()
			os.Exit(0)
		}
		_, _, _ = procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

func runAppWindow(app *App, srv *Server) {
	dir := appDataDir()
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(markerPath(), []byte(strconv.Itoa(os.Getpid())), 0o644)

	uiScale = systemScale()
	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)
	className, _ := windows.UTF16PtrFromString("DiskDetectiveWindow")
	large, small := appIcons()
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	wc := wndClassExW{
		WndProc:    windows.NewCallback(wndProc),
		Instance:   hinst,
		Icon:       windows.Handle(large),
		IconSm:     windows.Handle(small),
		Cursor:     windows.Handle(cursor),
		Background: windows.Handle(colorWindowBrush),
		ClassName:  className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		logf("RegisterClassEx failed: %v", err)
		fallbackToBrowser(false)
	}

	area := workArea()
	aw, ah := int(area.Right-area.Left), int(area.Bottom-area.Top)
	minTrack = point{X: int32(900 * uiScale), Y: int32(600 * uiScale)}
	if int(minTrack.X) > aw {
		minTrack.X = int32(aw)
	}
	if int(minTrack.Y) > ah {
		minTrack.Y = int32(ah)
	}
	w, h := int(1280*uiScale), int(860*uiScale)
	if w > aw-24 {
		w = aw - 24
	}
	if h > ah-24 {
		h = ah - 24
	}
	x := int(area.Left) + (aw-w)/2
	y := int(area.Top) + (ah-h)/2
	title, _ := windows.UTF16PtrFromString("용량 탐정 — 쓸모없는 파일 찾기")
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWnd, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, uintptr(hinst), 0)
	if hwnd == 0 {
		logf("CreateWindowEx failed: %v", err)
		fallbackToBrowser(false)
	}
	app.hwnd.Store(hwnd)
	_, _, _ = procShowWindow.Call(hwnd, swShow)
	_, _, _ = procUpdateWindow.Call(hwnd)
	_, _, _ = procSetForegroundWindow.Call(hwnd)

	// If the WebView2 environment never comes up, switch to the browser window.
	go func() {
		time.Sleep(30 * time.Second)
		if !controllerReady.Load() {
			logf("webview2 start timed out")
			fallbackToBrowser(false)
		}
	}()

	c := edge.NewChromium()
	suffix := ""
	if isAdmin() { // elevated and normal instances must not share a WebView2 profile
		suffix = "-admin"
	}
	c.DataPath = filepath.Join(dir, "WebView2"+suffix)
	c.SetErrorCallback(func(err error) {
		logf("webview2 error: %v", err)
		switch {
		case !controllerReady.Load(): // WebView2 could not start at all
			fallbackToBrowser(true)
		case !navigated.Load(): // started, but the page never showed
			fallbackToBrowser(false)
		}
		// later errors (a failed resize and the like) are only logged
	})
	c.ProcessFailedCallback = func(sender *edge.ICoreWebView2, args *edge.ICoreWebView2ProcessFailedEventArgs) {
		kind, _ := args.GetProcessFailedKind()
		logf("webview2 process failed: kind=%d", kind)
		switch kind {
		case edge.COREWEBVIEW2_PROCESS_FAILED_KIND_BROWSER_PROCESS_EXITED:
			fallbackToBrowser(false) // the WebView is gone; carry on in a browser window
		case edge.COREWEBVIEW2_PROCESS_FAILED_KIND_RENDER_PROCESS_EXITED,
			edge.COREWEBVIEW2_PROCESS_FAILED_KIND_RENDER_PROCESS_UNRESPONSIVE:
			c.Navigate(srv.url) // reload the page
		}
	}
	c.NavigationCompletedCallback = func(sender *edge.ICoreWebView2, args *edge.ICoreWebView2NavigationCompletedEventArgs) {
		if navigated.CompareAndSwap(false, true) {
			_ = os.Remove(markerPath())
		}
	}
	mainChromium = c
	if !c.Embed(hwnd) {
		fallbackToBrowser(true)
	}
	controllerReady.Store(true)
	if s, err := c.GetSettings(); err == nil {
		_ = s.PutAreDevToolsEnabled(false)
		_ = s.PutAreDefaultContextMenusEnabled(false)
		_ = s.PutIsStatusBarEnabled(false)
		_ = s.PutIsZoomControlEnabled(true)
	}
	disableExternalDrop(c)
	c.SetBackgroundColour(246, 247, 249, 255)
	c.Resize()
	c.Navigate(srv.url)
	c.Focus()

	var m msgT
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	_ = os.Remove(markerPath())
	c.ShuttingDown()
}

// disableExternalDrop stops a dragged-in file from replacing the app page.
// Older WebView2 runtimes lack the setting; the page blocks drops as well.
func disableExternalDrop(c *edge.Chromium) {
	defer func() {
		if r := recover(); r != nil {
			logf("AllowExternalDrag: %v", r)
		}
	}()
	if err := c.AllowExternalDrag(false); err != nil {
		logf("AllowExternalDrag: %v", err)
	}
}

// ---- browser fallback ----

func findAppPath(name string) string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetStringValue("")
		k.Close()
		v = strings.Trim(v, `"`)
		if err == nil && v != "" {
			if st, err := os.Stat(v); err == nil && !st.IsDir() {
				return v
			}
		}
	}
	var rels []string
	switch name {
	case "msedge.exe":
		rels = []string{`Microsoft\Edge\Application\msedge.exe`}
	case "chrome.exe":
		rels = []string{`Google\Chrome\Application\chrome.exe`}
	}
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		for _, r := range rels {
			if base == "" {
				continue
			}
			p := filepath.Join(base, r)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

func openAppBrowser(url string) *exec.Cmd {
	profile := filepath.Join(appDataDir(), "BrowserWindow")
	if isAdmin() { // never share a browser profile between elevated and normal copies
		profile += "-admin"
	}
	args := []string{"--app=" + url, "--window-size=1280,860", "--no-first-run",
		"--no-default-browser-check", "--disable-sync", "--user-data-dir=" + profile}
	for _, name := range []string{"msedge.exe", "chrome.exe"} {
		exe := findAppPath(name)
		if exe == "" {
			continue
		}
		cmd := exec.Command(exe, args...)
		if err := cmd.Start(); err == nil {
			logf("opened app window with %s", exe)
			return cmd
		}
	}
	if err := shellOpen(url); err != nil {
		fatal("웹 브라우저를 열지 못했어요.\n\n브라우저 주소창에 아래 주소를 직접 입력해 주세요:\n" + url)
	}
	return nil
}

// waitForBrowserClose keeps the program alive while its page is open.
func waitForBrowserClose(app *App, cmd *exec.Cmd) {
	start := time.Now()
	procExited := make(chan struct{})
	if cmd != nil {
		go func() {
			_ = cmd.Wait()
			// A browser that exits within seconds usually handed the window to an
			// already running browser process, so its exit says nothing.
			if time.Since(start) > 15*time.Second {
				close(procExited)
			}
		}()
	}
	exited := false
	for {
		time.Sleep(1500 * time.Millisecond)
		if !exited {
			select {
			case <-procExited:
				exited = true
			default:
			}
		}
		now := time.Now().UnixMilli()
		last := app.lastPing.Load()
		bye := app.byeAt.Load()
		if last == 0 {
			if time.Since(start) > 3*time.Minute {
				return
			}
			continue
		}
		switch {
		case bye > last && now-bye > 5000: // the page was closed and not reopened
			return
		case exited && now-last > 10000:
			return
		case now-last > 150000: // background pages may ping only once a minute
			return
		}
	}
}

func runBrowserUI(app *App, srv *Server) {
	cmd := openAppBrowser(srv.url)
	waitForBrowserClose(app, cmd)
}
