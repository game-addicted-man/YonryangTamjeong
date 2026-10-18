package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/fonts/*.woff2
var fontFS embed.FS

type App struct {
	mu       sync.Mutex
	junk     *JunkJob
	scan     *ScanJob
	dupes    *DupeJob
	lastPing atomic.Int64   // unix ms of the last heartbeat from the page
	byeAt    atomic.Int64   // unix ms when the page said goodbye
	hwnd     atomic.Uintptr // main window (Windows app mode), owner for dialogs
	mode     atomic.Value   // "app" or "browser"
}

func newApp() *App {
	a := &App{}
	a.mode.Store("browser")
	return a
}

type Server struct {
	app   *App
	token string
	host  string
	url   string
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func startServer(app *App, port int, token string) (*Server, error) {
	if token == "" {
		token = randomToken()
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	p := ln.Addr().(*net.TCPAddr).Port
	s := &Server{app: app, token: token, host: fmt.Sprintf("127.0.0.1:%d", p)}
	s.url = fmt.Sprintf("http://%s/?t=%s", s.host, token)
	hs := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
			logf("http server: %v", err)
		}
	}()
	return s, nil
}

func (s *Server) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/", s.handleIndex)
	m.HandleFunc("/fonts/", s.handleFont)
	m.HandleFunc("/api/info", s.handleInfo)
	m.HandleFunc("/api/junk/start", s.handleJunkStart)
	m.HandleFunc("/api/junk", s.handleJunk)
	m.HandleFunc("/api/scan/start", s.handleScanStart)
	m.HandleFunc("/api/scan/cancel", s.handleScanCancel)
	m.HandleFunc("/api/scan", s.handleScan)
	m.HandleFunc("/api/scan/node", s.handleNode)
	m.HandleFunc("/api/scan/files", s.handleFiles)
	m.HandleFunc("/api/dupes/start", s.handleDupesStart)
	m.HandleFunc("/api/dupes/cancel", s.handleDupesCancel)
	m.HandleFunc("/api/dupes", s.handleDupes)
	m.HandleFunc("/api/open", s.handleOpen)
	m.HandleFunc("/api/launch", s.handleLaunch)
	m.HandleFunc("/api/admin", s.handleAdmin)
	m.HandleFunc("/api/pickfolder", s.handlePickFolder)
	m.HandleFunc("/api/ping", s.handlePing)
	m.HandleFunc("/api/bye", s.handleBye)
	return s.guard(m)
}

// guard blocks requests that do not come from our own page: wrong Host header
// (DNS rebinding) or missing token on API calls.
func (s *Server) guard(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.host)
	allowedHosts := map[string]bool{s.host: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			tok := r.Header.Get("X-Token")
			if tok == "" {
				tok = r.URL.Query().Get("t")
			}
			if tok != s.token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logf("json encode: %v", err)
	}
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func badRequest(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleFont(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/fonts/")
	if strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".woff2") {
		http.NotFound(w, r)
		return
	}
	data, err := fontFS.ReadFile("web/fonts/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = w.Write(data)
}

// ---- info ----

type driveInfo struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	FS    string `json:"fs"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
	Type  string `json:"type"`
}

func systemDrive() string {
	sd := os.Getenv("SystemDrive")
	if sd == "" {
		return ""
	}
	return strings.TrimRight(sd, `\/`) + string(sepChar)
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	profile := os.Getenv("USERPROFILE")
	if profile == "" {
		profile, _ = os.UserHomeDir()
	}
	writeJSON(w, map[string]interface{}{
		"platform":    platformName(),
		"admin":       isAdmin(),
		"drives":      listDrives(),
		"profile":     profile,
		"downloads":   knownFolder("downloads"),
		"version":     appVersion,
		"mode":        s.app.mode.Load(),
		"canPick":     canPickFolder() && s.app.mode.Load() == "app", // in a browser window the dialog could hide behind it
		"user":        os.Getenv("USERNAME"),
		"systemDrive": systemDrive(),
	})
}

// ---- junk ----

func (s *Server) handleJunkStart(w http.ResponseWriter, r *http.Request) {
	s.app.mu.Lock()
	if s.app.junk != nil {
		s.app.junk.cancel.Store(true)
	}
	j := newJunkJob()
	s.app.junk = j
	s.app.mu.Unlock()
	j.Start()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleJunk(w http.ResponseWriter, r *http.Request) {
	s.app.mu.Lock()
	j := s.app.junk
	s.app.mu.Unlock()
	if j == nil {
		writeJSON(w, map[string]interface{}{"running": false, "started": false, "cats": []int{}})
		return
	}
	data, err := j.snapshot(json.Marshal)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(data)
}

// ---- drive / folder scan ----

func cleanRoot(p string) (string, bool) {
	p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"`))
	if p == "" {
		return "", false
	}
	if len(p) == 2 && p[1] == ':' { // "C:" -> "C:\"
		p += `\`
	}
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return "", false
	}
	if !dirExists(p) {
		return "", false
	}
	return p, true
}

func (s *Server) handleScanStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root string `json:"root"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, "잘못된 요청이에요.")
		return
	}
	root, ok := cleanRoot(req.Root)
	if !ok {
		badRequest(w, "폴더를 찾을 수 없어요: "+req.Root)
		return
	}
	s.app.mu.Lock()
	if s.app.scan != nil && s.app.scan.running.Load() {
		s.app.scan.cancel.Store(true)
	}
	j := newScanJob(root)
	s.app.scan = j
	s.app.mu.Unlock()
	j.Start()
	writeJSON(w, map[string]interface{}{"ok": true, "gen": j.Gen})
}

func (s *Server) currentScan() *ScanJob {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	return s.app.scan
}

func (s *Server) handleScanCancel(w http.ResponseWriter, r *http.Request) {
	if j := s.currentScan(); j != nil {
		j.cancel.Store(true)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type scanSummary struct {
	Total       int64 `json:"total"`
	Files       int64 `json:"files"`
	Dirs        int64 `json:"dirs"`
	Denied      int64 `json:"denied"`
	DriveTotal  int64 `json:"driveTotal"`
	DriveUsed   int64 `json:"driveUsed"`
	Unaccounted int64 `json:"unaccounted"`
	CloudBytes  int64 `json:"cloudBytes"`
	IsDriveRoot bool  `json:"isDriveRoot"`
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	j := s.currentScan()
	if j == nil {
		writeJSON(w, map[string]interface{}{"started": false})
		return
	}
	cur, _ := j.current.Load().(string)
	resp := map[string]interface{}{
		"started":   true,
		"gen":       j.Gen,
		"running":   j.running.Load(),
		"done":      j.done.Load(),
		"cancelled": j.cancel.Load(),
		"root":      j.Root,
		"files":     j.files.Load(),
		"bytes":     j.bytes.Load(),
		"dirs":      j.dirs.Load(),
		"denied":    j.denied.Load(),
		"current":   cur,
		"percent":   j.Percent(),
		"elapsedMs": j.Elapsed().Milliseconds(),
		"error":     j.Err(),
	}
	if j.done.Load() && j.root != nil {
		sum := scanSummary{Total: j.root.Size, Files: j.root.Files, Dirs: int64(len(j.nodes)),
			Denied: j.denied.Load(), DriveTotal: j.DriveTotal, DriveUsed: j.DriveUsed,
			CloudBytes: j.cloudBytes, IsDriveRoot: j.IsDriveRoot}
		if j.IsDriveRoot && j.DriveUsed > j.root.Size && !j.cancel.Load() {
			sum.Unaccounted = j.DriveUsed - j.root.Size
		}
		resp["summary"] = sum
	}
	writeJSON(w, resp)
}

type nodeOut struct {
	ID       int32      `json:"id"`
	Name     string     `json:"name"`
	Size     int64      `json:"size"`
	Files    int64      `json:"files"`
	Own      int64      `json:"own"`
	OwnFiles int64      `json:"ownFiles"`
	Denied   bool       `json:"denied,omitempty"`
	Kind     uint8      `json:"kind,omitempty"`
	HasKids  bool       `json:"hasKids,omitempty"`
	More     int        `json:"more,omitempty"`
	MoreSize int64      `json:"moreSize,omitempty"`
	Kids     []*nodeOut `json:"kids,omitempty"`
}

func toNodeOut(n *Node, depth, limit int) *nodeOut {
	o := &nodeOut{ID: n.ID, Name: n.Name, Size: n.Size, Files: n.Files, Own: n.OwnBytes,
		OwnFiles: n.OwnFiles, Denied: n.Denied, Kind: n.Kind}
	for _, c := range n.Children {
		if c.Size > 0 || c.Denied {
			o.HasKids = true
			break
		}
	}
	if depth <= 0 {
		return o
	}
	visible := 0
	for _, c := range n.Children {
		if c.Size > 0 || c.Denied {
			visible++
		}
	}
	count := 0
	for _, c := range n.Children {
		if c.Size <= 0 && !c.Denied {
			continue
		}
		if count >= limit {
			o.More++
			o.MoreSize += c.Size
			continue
		}
		childDepth := depth - 1
		if visible == 1 { // a folder holding a single folder: look one level further so the map is not empty
			childDepth = depth
		}
		o.Kids = append(o.Kids, toNodeOut(c, childDepth, limit))
		count++
	}
	return o
}

type crumb struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	j := s.currentScan()
	if j == nil || !j.done.Load() || j.root == nil {
		badRequest(w, "아직 분석 결과가 없어요.")
		return
	}
	q := r.URL.Query()
	id, _ := strconv.Atoi(q.Get("id"))
	depth, _ := strconv.Atoi(q.Get("depth"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if depth < 1 {
		depth = 2
	}
	if depth > 3 {
		depth = 3
	}
	if limit < 5 || limit > 300 {
		limit = 60
	}
	n := j.Node(int32(id))
	if n == nil {
		badRequest(w, "폴더를 찾을 수 없어요.")
		return
	}
	var crumbs []crumb
	for x := n; x != nil; x = x.Parent {
		crumbs = append([]crumb{{ID: x.ID, Name: x.Name}}, crumbs...)
	}
	parent := int32(-1)
	if n.Parent != nil {
		parent = n.Parent.ID
	}
	writeJSON(w, map[string]interface{}{
		"gen":      j.Gen,
		"node":     toNodeOut(n, depth, limit),
		"path":     n.Path(),
		"crumbs":   crumbs,
		"parentId": parent,
		"rootSize": j.root.Size,
	})
}

type fileOut struct {
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Type  uint8  `json:"type"`
	DirID int32  `json:"dirId"`
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	j := s.currentScan()
	if j == nil || !j.done.Load() || j.root == nil {
		badRequest(w, "아직 분석 결과가 없어요.")
		return
	}
	q := r.URL.Query()
	t, err := strconv.Atoi(q.Get("type"))
	if err != nil {
		t = -1
	}
	hideSystem := q.Get("hideSystem") == "1"
	var list []*FileRec
	switch {
	case t >= 0 && t < NTypes:
		list = j.topByType[t]
	case hideSystem:
		list = j.topUser
	default:
		list = j.topAll
	}
	dirCache := map[*Node]string{}
	out := make([]fileOut, 0, len(list))
	for _, f := range list {
		d, ok := dirCache[f.Dir]
		if !ok {
			d = f.Dir.Path()
			dirCache[f.Dir] = d
		}
		out = append(out, fileOut{Name: f.Name, Dir: d, Size: f.Size, MTime: f.MTime, Type: f.Type, DirID: f.Dir.ID})
	}
	types := make([]map[string]int64, NTypes)
	for i := 0; i < NTypes; i++ {
		types[i] = map[string]int64{"bytes": j.typeBytes[i], "files": j.typeFiles[i]}
	}
	writeJSON(w, map[string]interface{}{"gen": j.Gen, "files": out, "types": types, "total": j.root.Size})
}

// ---- duplicates ----

func (s *Server) handleDupesStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root        string `json:"root"`
		MinSize     int64  `json:"minSize"`
		SkipAppData bool   `json:"skipAppData"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, "잘못된 요청이에요.")
		return
	}
	root, ok := cleanRoot(req.Root)
	if !ok {
		badRequest(w, "폴더를 찾을 수 없어요: "+req.Root)
		return
	}
	if req.MinSize < 1 {
		req.MinSize = 1 << 20
	}
	s.app.mu.Lock()
	if s.app.dupes != nil {
		s.app.dupes.cancel.Store(true)
	}
	j := newDupeJob(root, req.MinSize, req.SkipAppData)
	s.app.dupes = j
	s.app.mu.Unlock()
	j.Start()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleDupesCancel(w http.ResponseWriter, r *http.Request) {
	s.app.mu.Lock()
	j := s.app.dupes
	s.app.mu.Unlock()
	if j != nil {
		j.cancel.Store(true)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleDupes(w http.ResponseWriter, r *http.Request) {
	s.app.mu.Lock()
	j := s.app.dupes
	s.app.mu.Unlock()
	if j == nil {
		writeJSON(w, map[string]interface{}{"started": false})
		return
	}
	st := j.status(j.done.Load())
	writeJSON(w, map[string]interface{}{"started": true, "status": st})
}

// ---- actions ----

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Select bool   `json:"select"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, "잘못된 요청이에요.")
		return
	}
	if req.Path != "shell:RecycleBinFolder" {
		st, err := os.Stat(req.Path)
		if err != nil {
			badRequest(w, "이 위치를 찾을 수 없어요. 이미 지워졌거나 접근 권한이 없을 수 있어요.")
			return
		}
		if !st.IsDir() {
			req.Select = true // show a file in its folder; never open or run it
		}
	}
	if err := openInExplorer(req.Path, req.Select); err != nil {
		badRequest(w, "탐색기를 열지 못했어요: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		What string `json:"what"`
	}
	if err := readJSON(r, &req); err != nil {
		badRequest(w, "잘못된 요청이에요.")
		return
	}
	if err := launchTool(req.What); err != nil {
		badRequest(w, "실행하지 못했어요: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !relaunchAsAdmin() {
		badRequest(w, "관리자 권한으로 다시 실행하지 못했어요. (권한 요청 창에서 [아니요]를 누르셨다면 그대로 계속 쓰셔도 돼요.)")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
	go func() {
		time.Sleep(400 * time.Millisecond)
		beforeExit()
		os.Exit(0)
	}()
}

func (s *Server) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	path := pickFolder(s.app.hwnd.Load(), "분석할 폴더를 고르세요")
	writeJSON(w, map[string]string{"path": path})
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.app.lastPing.Store(time.Now().UnixMilli())
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleBye(w http.ResponseWriter, r *http.Request) {
	s.app.byeAt.Store(time.Now().UnixMilli())
	writeJSON(w, map[string]bool{"ok": true})
}
