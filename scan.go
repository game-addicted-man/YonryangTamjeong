package main

import (
	"container/heap"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Node is one directory in the scanned tree. Files are not stored individually,
// only aggregated, which keeps memory small even for millions of files.
type Node struct {
	Name     string
	Parent   *Node
	Children []*Node
	Size     int64 // total bytes including all descendants (after aggregation)
	Files    int64 // total file count including descendants
	OwnBytes int64 // bytes of files directly inside this directory
	OwnFiles int64
	ID       int32
	Kind     uint8
	Denied   bool
}

const (
	KindNormal uint8 = iota
	KindSystem
	KindGame
	KindRecycle
)

func (n *Node) Path() string {
	if n.Parent == nil {
		return n.Name
	}
	var parts []string
	x := n
	for ; x.Parent != nil; x = x.Parent {
		parts = append(parts, x.Name)
	}
	var b strings.Builder
	b.WriteString(x.Name)
	for i := len(parts) - 1; i >= 0; i-- {
		s := b.String()
		if s != "" && s[len(s)-1] != '\\' && s[len(s)-1] != '/' {
			b.WriteByte(byte(sepChar))
		}
		b.WriteString(parts[i])
	}
	return b.String()
}

// FileRec is a single large file remembered for the "big files" lists.
type FileRec struct {
	Dir   *Node
	Name  string
	Size  int64
	MTime int64
	Type  uint8
}

// ---- min-heap keeping the N largest files ----

type recHeap []*FileRec

func (h recHeap) Len() int            { return len(h) }
func (h recHeap) Less(i, j int) bool  { return h[i].Size < h[j].Size }
func (h recHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *recHeap) Push(x interface{}) { *h = append(*h, x.(*FileRec)) }
func (h *recHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

type topN struct {
	cap int
	h   recHeap
}

func (t *topN) threshold() int64 {
	if len(t.h) < t.cap {
		return 0
	}
	return t.h[0].Size
}

func (t *topN) add(r *FileRec) {
	if len(t.h) < t.cap {
		heap.Push(&t.h, r)
	} else if r.Size > t.h[0].Size {
		t.h[0] = r
		heap.Fix(&t.h, 0)
	}
}

// ---- shared work stack for the parallel directory walk ----

type workStack struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []*Node
	pending int
	closed  bool
}

func newWorkStack() *workStack {
	s := &workStack{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *workStack) push(ns ...*Node) {
	s.mu.Lock()
	s.items = append(s.items, ns...)
	s.pending += len(ns)
	s.mu.Unlock()
	if len(ns) == 1 {
		s.cond.Signal()
	} else {
		s.cond.Broadcast()
	}
}

func (s *workStack) pop() (*Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.items) == 0 {
		if s.pending == 0 || s.closed {
			return nil, false
		}
		s.cond.Wait()
	}
	if s.closed {
		return nil, false
	}
	n := s.items[len(s.items)-1]
	s.items[len(s.items)-1] = nil
	s.items = s.items[:len(s.items)-1]
	return n, true
}

func (s *workStack) done() {
	s.mu.Lock()
	s.pending--
	if s.pending <= 0 {
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

func (s *workStack) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cond.Broadcast()
}

// ---- classification ----

var systemRootDirs = lowerSet("Windows", "Program Files", "Program Files (x86)", "ProgramData",
	"Recovery", "$WinREAgent", "System Volume Information", "PerfLogs", "$SysReset", "Config.Msi",
	"MSOCache", "Boot", "$GetCurrent", "$Windows.~WS", "$WINDOWS.~BT")

var rootSystemFiles = lowerSet("pagefile.sys", "hiberfil.sys", "swapfile.sys", "DumpStack.log",
	"DumpStack.log.tmp", "bootmgr", "BOOTNXT")

var gameDirs = lowerSet("steamapps", "SteamLibrary", "Epic Games", "Riot Games", "Nexon",
	"XboxGames", "GOG Games", "GOG Galaxy", "Ubisoft Game Launcher", "EA Games", "Origin Games",
	"Electronic Arts", "Smilegate", "NCSOFT", "Kakao Games", "Pearl Abyss", "Netmarble",
	"Rockstar Games", "Battle.net Games", "StarCraft", "StarCraft II", "Overwatch", "Diablo III",
	"Diablo IV", "World of Warcraft", "Hearthstone", "League of Legends", "MapleStory", "Lost Ark",
	"Black Desert", "Genshin Impact", "HoYoPlay", "miHoYo", "Call of Duty", "Wuthering Waves",
	"Honkai Star Rail", "Grand Theft Auto V", "Minecraft Launcher")

func lowerSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[strings.ToLower(n)] = true
	}
	return m
}

func childKind(parent *Node, name string, parentIsDriveRoot bool) uint8 {
	if parent.Kind == KindRecycle {
		return KindRecycle
	}
	lname := strings.ToLower(name)
	if gameDirs[lname] {
		return KindGame
	}
	if parent.Kind != KindNormal {
		return parent.Kind
	}
	if parentIsDriveRoot {
		if lname == "$recycle.bin" || lname == "recycler" {
			return KindRecycle
		}
		if systemRootDirs[lname] {
			return KindSystem
		}
	}
	return KindNormal
}

func fileTypeIn(n *Node, name string, isDriveRoot bool) int {
	switch n.Kind {
	case KindGame:
		return TGame
	case KindSystem:
		return TSystem
	case KindRecycle:
		return TRecycle
	}
	if isDriveRoot && rootSystemFiles[strings.ToLower(name)] {
		return TSystem
	}
	return typeByName(name)
}

// ---- scan job ----

type ScanJob struct {
	Gen         int64
	Root        string
	IsDriveRoot bool
	DriveTotal  int64
	DriveUsed   int64

	start   time.Time    // set before the job is published, never changed
	endNano atomic.Int64 // unix nanoseconds when the job finished
	cancel  atomic.Bool
	running atomic.Bool
	done    atomic.Bool
	files   atomic.Int64
	bytes   atomic.Int64
	dirs    atomic.Int64
	denied  atomic.Int64
	current atomic.Value // string

	mu  sync.Mutex
	err string

	// results, valid once done
	root       *Node
	nodes      []*Node
	typeBytes  [NTypes]int64
	typeFiles  [NTypes]int64
	topAll     []*FileRec
	topUser    []*FileRec
	topByType  [NTypes][]*FileRec
	cloudBytes int64
}

type scanWorker struct {
	typeBytes [NTypes]int64
	typeFiles [NTypes]int64
	all       topN
	user      topN
	byType    [NTypes]topN
	cloud     int64
	buf       []Entry
}

func newScanWorker() *scanWorker {
	w := &scanWorker{all: topN{cap: 400}, user: topN{cap: 400}, buf: make([]Entry, 0, 512)}
	for i := range w.byType {
		w.byType[i].cap = 200
	}
	return w
}

func (w *scanWorker) consider(n *Node, e *Entry, t int) {
	sz := e.Size
	needAll := sz > w.all.threshold()
	needUser := t != TSystem && sz > w.user.threshold()
	needType := sz > w.byType[t].threshold()
	if !needAll && !needUser && !needType {
		return
	}
	r := &FileRec{Dir: n, Name: e.Name, Size: sz, MTime: e.MTime, Type: uint8(t)}
	if needAll {
		w.all.add(r)
	}
	if needUser {
		w.user.add(r)
	}
	if needType {
		w.byType[t].add(r)
	}
}

func newScanJob(root string) *ScanJob {
	j := &ScanJob{Root: root, Gen: time.Now().UnixNano(), start: time.Now()}
	j.IsDriveRoot = isDriveRoot(root)
	if j.IsDriveRoot {
		if total, free, ok := diskUsage(root); ok {
			j.DriveTotal = total
			j.DriveUsed = total - free
		}
	}
	j.current.Store("")
	return j
}

func (j *ScanJob) setErr(msg string) {
	j.mu.Lock()
	j.err = msg
	j.mu.Unlock()
}

func (j *ScanJob) Err() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

func (j *ScanJob) process(w *scanWorker, n *Node, st *workStack) {
	path := n.Path()
	if j.dirs.Add(1)%97 == 1 {
		j.current.Store(path)
	}
	entries, err := listDir(path, w.buf[:0])
	if cap(entries) <= 1<<16 {
		w.buf = entries[:0]
	} else {
		w.buf = make([]Entry, 0, 512)
	}
	if err != nil {
		if errors.Is(err, errDenied) {
			n.Denied = true
			j.denied.Add(1)
		}
		return
	}
	isRoot := n.Parent == nil && j.IsDriveRoot
	var kids []*Node
	var bytes, files int64
	for i := range entries {
		e := &entries[i]
		if e.IsDir {
			if e.SkipRecurse {
				continue
			}
			kids = append(kids, &Node{Name: e.Name, Parent: n, Kind: childKind(n, e.Name, isRoot)})
			continue
		}
		files++
		bytes += e.Size
		if e.CloudOnly {
			w.cloud += e.Logical
		}
		t := fileTypeIn(n, e.Name, isRoot)
		w.typeBytes[t] += e.Size
		w.typeFiles[t]++
		if e.Size > 0 {
			w.consider(n, e, t)
		}
	}
	n.Children = kids
	n.OwnBytes = bytes
	n.OwnFiles = files
	j.files.Add(files)
	j.bytes.Add(bytes)
	if len(kids) > 0 {
		st.push(kids...)
	}
}

// Start runs the scan in the background.
func (j *ScanJob) Start() {
	j.running.Store(true)
	go j.run()
}

// Elapsed is safe to call from any goroutine.
func (j *ScanJob) Elapsed() time.Duration {
	if e := j.endNano.Load(); e != 0 {
		return time.Duration(e - j.start.UnixNano())
	}
	return time.Since(j.start)
}

func (j *ScanJob) run() {
	defer func() {
		if r := recover(); r != nil {
			j.setErr(fmt.Sprint("검사 중 오류: ", r))
			logf("scan panic: %v", r)
		}
		j.endNano.Store(time.Now().UnixNano())
		j.running.Store(false)
		j.done.Store(true)
	}()

	root := &Node{Name: j.Root}
	st := newWorkStack()
	st.push(root)

	nw := runtime.NumCPU()
	if nw < 4 {
		nw = 4
	}
	if nw > 8 {
		nw = 8
	}
	workers := make([]*scanWorker, nw)
	var wg sync.WaitGroup
	for i := range workers {
		w := newScanWorker()
		workers[i] = w
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					j.setErr(fmt.Sprint("검사 중 오류: ", r))
					logf("scan worker panic: %v", r)
					st.close()
				}
			}()
			for {
				if j.cancel.Load() {
					st.close()
					return
				}
				n, ok := st.pop()
				if !ok {
					return
				}
				j.process(w, n, st)
				st.done()
			}
		}()
	}
	wg.Wait()

	// aggregate sizes bottom-up and assign ids
	order := make([]*Node, 0, j.dirs.Load()+1)
	stack := []*Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n.ID = int32(len(order))
		order = append(order, n)
		n.Size = n.OwnBytes
		n.Files = n.OwnFiles
		stack = append(stack, n.Children...)
	}
	for i := len(order) - 1; i > 0; i-- {
		n := order[i]
		n.Parent.Size += n.Size
		n.Parent.Files += n.Files
	}
	for _, n := range order {
		if len(n.Children) > 1 {
			kids := n.Children
			sort.Slice(kids, func(a, b int) bool { return kids[a].Size > kids[b].Size })
		}
	}

	// merge per-worker statistics
	var alls, users []*topN
	var byType [NTypes][]*topN
	for _, w := range workers {
		for t := 0; t < NTypes; t++ {
			j.typeBytes[t] += w.typeBytes[t]
			j.typeFiles[t] += w.typeFiles[t]
			byType[t] = append(byType[t], &w.byType[t])
		}
		j.cloudBytes += w.cloud
		alls = append(alls, &w.all)
		users = append(users, &w.user)
	}
	j.topAll = mergeTop(alls, 400)
	j.topUser = mergeTop(users, 400)
	for t := 0; t < NTypes; t++ {
		j.topByType[t] = mergeTop(byType[t], 200)
	}
	j.root = root
	j.nodes = order
}

func mergeTop(hs []*topN, limit int) []*FileRec {
	var all []*FileRec
	for _, h := range hs {
		all = append(all, h.h...)
	}
	sort.Slice(all, func(a, b int) bool { return all[a].Size > all[b].Size })
	if len(all) > limit {
		all = all[:limit]
	}
	return all
}

func (j *ScanJob) Node(id int32) *Node {
	if id < 0 || int(id) >= len(j.nodes) {
		return nil
	}
	return j.nodes[id]
}

func (j *ScanJob) Percent() float64 {
	if j.done.Load() {
		return 100
	}
	if j.DriveUsed <= 0 {
		return -1
	}
	p := float64(j.bytes.Load()) * 100 / float64(j.DriveUsed)
	if p > 99 {
		p = 99
	}
	return p
}
