package main

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type DupFile struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	MTime int64  `json:"mtime"`
}

type DupGroup struct {
	Size   int64     `json:"size"`
	Count  int       `json:"count"`
	Wasted int64     `json:"wasted"`
	Files  []DupFile `json:"files"`
}

type DupeJob struct {
	Root        string
	MinSize     int64
	SkipAppData bool

	cancel     atomic.Bool
	running    atomic.Bool
	done       atomic.Bool
	phase      atomic.Int32 // 1 listing, 2 quick compare, 3 full compare
	listed     atomic.Int64
	candidates atomic.Int64
	hashed     atomic.Int64
	toHash     atomic.Int64
	current    atomic.Value

	start      time.Time    // set before the job is published
	endNano    atomic.Int64 // unix nanoseconds when finished
	mu         sync.Mutex
	err        string
	groups     []DupGroup
	wasted     int64
	totalGroup int
}

type dupCand struct {
	path  string
	size  int64
	mtime int64
}

var dupSkipAlways = lowerSet("$Recycle.Bin", "System Volume Information", "node_modules", ".git",
	"WinSxS", "$WINDOWS.~BT", "$Windows.~WS", "Windows.old")

func newDupeJob(root string, minSize int64, skipAppData bool) *DupeJob {
	j := &DupeJob{Root: root, MinSize: minSize, SkipAppData: skipAppData, start: time.Now()}
	j.current.Store("")
	return j
}

func (j *DupeJob) Start() {
	j.running.Store(true)
	go j.run()
}

func (j *DupeJob) setErr(s string) {
	j.mu.Lock()
	j.err = s
	j.mu.Unlock()
}

func (j *DupeJob) run() {
	defer func() {
		if r := recover(); r != nil {
			j.setErr(fmt.Sprint("검사 중 오류: ", r))
			logf("dupes panic: %v", r)
		}
		j.endNano.Store(time.Now().UnixNano())
		j.running.Store(false)
		j.done.Store(true)
	}()

	// 1. list candidate files
	j.phase.Store(1)
	bySize := map[int64][]dupCand{}
	rootIsDrive := isDriveRoot(j.Root)
	stack := []string{j.Root}
	var buf []Entry
	for len(stack) > 0 {
		if j.cancel.Load() {
			return
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		var err error
		buf, err = listDir(dir, buf[:0])
		if err != nil {
			continue
		}
		if j.listed.Load()%200 == 0 {
			j.current.Store(dir)
		}
		atRoot := rootIsDrive && normPath(dir) == normPath(j.Root)
		for i := range buf {
			e := &buf[i]
			if e.IsDir {
				ln := strings.ToLower(e.Name)
				if e.SkipRecurse || dupSkipAlways[ln] {
					continue
				}
				if j.SkipAppData && ln == "appdata" {
					continue
				}
				if atRoot && systemRootDirs[ln] {
					continue
				}
				stack = append(stack, joinPath(dir, e.Name))
				continue
			}
			if e.CloudOnly || e.Logical < j.MinSize || e.Logical <= 0 {
				continue
			}
			j.listed.Add(1)
			bySize[e.Logical] = append(bySize[e.Logical], dupCand{path: joinPath(dir, e.Name), size: e.Logical, mtime: e.MTime})
		}
	}

	// 2. quick compare: first + last 64 KB
	j.phase.Store(2)
	var sizes []int64
	var candTotal int64
	for sz, list := range bySize {
		if len(list) > 1 {
			sizes = append(sizes, sz)
			j.candidates.Add(int64(len(list)))
			candTotal += int64(len(list))
		}
	}
	sort.Slice(sizes, func(a, b int) bool { return sizes[a] > sizes[b] })
	var stage2 [][]dupCand
	var quickDone int64
	for _, sz := range sizes {
		if j.cancel.Load() {
			return
		}
		groups := map[string][]dupCand{}
		for _, c := range bySize[sz] {
			quickDone++
			j.hashed.Store(quickDone)
			j.toHash.Store(candTotal)
			h, err := quickHash(c.path, c.size)
			if err != nil {
				continue
			}
			groups[h] = append(groups[h], c)
		}
		for _, g := range groups {
			if len(g) > 1 {
				stage2 = append(stage2, g)
			}
		}
	}

	// 3. full compare (only needed when the quick hash did not cover the whole file)
	j.phase.Store(3)
	var total int64
	for _, g := range stage2 {
		if g[0].size > 2*quickChunk {
			total += g[0].size * int64(len(g))
		}
	}
	j.toHash.Store(total)
	j.hashed.Store(0)
	var final [][]dupCand
	for _, g := range stage2 {
		if j.cancel.Load() {
			return
		}
		if g[0].size <= 2*quickChunk {
			final = append(final, g)
			continue
		}
		groups := map[string][]dupCand{}
		for _, c := range g {
			j.current.Store(c.path)
			h, err := fullHash(c.path, &j.cancel, &j.hashed)
			if err != nil {
				continue
			}
			groups[h] = append(groups[h], c)
		}
		for _, sub := range groups {
			if len(sub) > 1 {
				final = append(final, sub)
			}
		}
	}

	// 4. drop hard links (same physical file under two names) and build the result
	var out []DupGroup
	var wasted int64
	for _, g := range final {
		seen := map[string]bool{}
		var files []DupFile
		for _, c := range g {
			if id, ok := fileID(c.path); ok {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
			files = append(files, DupFile{Path: c.path, Name: filepath.Base(c.path), Dir: filepath.Dir(c.path), MTime: c.mtime})
		}
		if len(files) < 2 {
			continue
		}
		sort.Slice(files, func(a, b int) bool { return files[a].MTime < files[b].MTime })
		dg := DupGroup{Size: g[0].size, Count: len(files), Files: files}
		dg.Wasted = dg.Size * int64(len(files)-1)
		wasted += dg.Wasted
		out = append(out, dg)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Wasted > out[b].Wasted })
	j.mu.Lock()
	j.totalGroup = len(out)
	if len(out) > 1000 {
		out = out[:1000]
	}
	j.groups = out
	j.wasted = wasted
	j.mu.Unlock()
}

const quickChunk = 64 * 1024

func quickHash(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	buf := make([]byte, quickChunk)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	h.Write(buf[:n])
	if size > 2*quickChunk {
		if _, err := f.Seek(size-quickChunk, io.SeekStart); err != nil {
			return "", err
		}
		n, err = io.ReadFull(f, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return "", err
		}
		h.Write(buf[:n])
	} else if size > quickChunk {
		rest, err := io.ReadAll(f)
		if err != nil {
			return "", err
		}
		h.Write(rest)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fullHash(path string, cancel *atomic.Bool, progress *atomic.Int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	buf := make([]byte, 1<<20)
	for {
		if cancel.Load() {
			return "", errors.New("cancelled")
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			progress.Add(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type dupeStatus struct {
	Running    bool       `json:"running"`
	Done       bool       `json:"done"`
	Cancelled  bool       `json:"cancelled"`
	Root       string     `json:"root"`
	Phase      int32      `json:"phase"`
	Listed     int64      `json:"listed"`
	Candidates int64      `json:"candidates"`
	Hashed     int64      `json:"hashed"`
	ToHash     int64      `json:"toHash"`
	Current    string     `json:"current"`
	ElapsedMs  int64      `json:"elapsedMs"`
	Error      string     `json:"error,omitempty"`
	Wasted     int64      `json:"wasted"`
	GroupCount int        `json:"groupCount"`
	Groups     []DupGroup `json:"groups,omitempty"`
}

func (j *DupeJob) status(withGroups bool) dupeStatus {
	st := dupeStatus{
		Running: j.running.Load(), Done: j.done.Load(), Cancelled: j.cancel.Load(), Root: j.Root,
		Phase: j.phase.Load(), Listed: j.listed.Load(), Candidates: j.candidates.Load(),
		Hashed: j.hashed.Load(), ToHash: j.toHash.Load(),
	}
	st.Current, _ = j.current.Load().(string)
	if e := j.endNano.Load(); e != 0 {
		st.ElapsedMs = (e - j.start.UnixNano()) / int64(time.Millisecond)
	} else {
		st.ElapsedMs = time.Since(j.start).Milliseconds()
	}
	j.mu.Lock()
	st.Error = j.err
	st.Wasted = j.wasted
	st.GroupCount = j.totalGroup
	if withGroups {
		st.Groups = j.groups
	}
	j.mu.Unlock()
	return st
}
