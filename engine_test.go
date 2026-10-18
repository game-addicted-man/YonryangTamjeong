//go:build !windows

package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeFile struct {
	rel  string
	size int
	age  time.Duration
	fill byte
}

func makeTree(t *testing.T, root string, files []fakeFile) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		fill := f.fill
		if fill == 0 {
			fill = byte(len(f.rel))
		}
		if err := os.WriteFile(p, bytes.Repeat([]byte{fill}, f.size), 0o644); err != nil {
			t.Fatal(err)
		}
		if f.age > 0 {
			ts := time.Now().Add(-f.age)
			_ = os.Chtimes(p, ts, ts)
		}
	}
}

var day = 24 * time.Hour

func fakeWindowsTree(t *testing.T) string {
	root := t.TempDir()
	makeTree(t, root, []fakeFile{
		{"Windows/System32/a.dll", 100_000, 0, 0},
		{"Windows/Temp/w1.tmp", 7_000, 0, 0},
		{"Windows/SoftwareDistribution/Download/kb1.cab", 33_000, 0, 0},
		{"Program Files/App/app.exe", 200_000, 0, 0},
		{"Program Files (x86)/Steam/steamapps/common/Game/data.pak", 300_000, 0, 0},
		{"Program Files (x86)/Steam/steamapps/shadercache/123/s.bin", 11_000, 0, 0},
		{"Users/me/Videos/movie.mp4", 500_000, 0, 0},
		{"Users/me/Downloads/setup.exe", 150_000, 0, 0},
		{"Users/me/Downloads/tool.zip", 60_000, 0, 0},
		{"Users/me/Downloads/old.pdf", 50_000, 200 * day, 0},
		{"Users/me/Downloads/recent.txt", 1_000, 0, 0},
		{"Users/me/AppData/Local/Temp/tmp1.tmp", 12_000, 0, 0},
		{"Users/me/AppData/Local/Google/Chrome/User Data/Default/Cache/Cache_Data/f_000001", 80_000, 0, 0},
		{"Users/me/AppData/Local/Google/Chrome/User Data/Profile 1/Code Cache/js/x", 20_000, 0, 0},
		{"Users/me/AppData/Local/Google/Chrome/User Data/ShaderCache/GPUCache/data_0", 10_000, 0, 0},
		{"Users/me/AppData/Local/Google/Chrome/User Data/Default/Service Worker/CacheStorage/abc/1", 40_000, 0, 0},
		{"Users/me/AppData/Local/Microsoft/Windows/Explorer/thumbcache_256.db", 9_000, 0, 0},
		{"Users/me/AppData/Local/Microsoft/Windows/Explorer/other.db", 5_000, 0, 0},
		{"Users/me/AppData/Local/Kakao/KakaoTalk/users/abc/chat.db", 70_000, 0, 0},
		{"Users/me/Documents/카카오톡 받은 파일/photo.jpg", 25_000, 0, 0},
		{"$Recycle.Bin/S-1-5-21/$R123.mp4", 45_000, 0, 0},
		{"hiberfil.sys", 400_000, 0, 0},
		{"pagefile.sys", 300_000, 0, 0},
	})
	// a junction-like symlink that must not be followed
	if err := os.Symlink(filepath.Join(root, "Users"), filepath.Join(root, "Documents and Settings")); err != nil {
		t.Fatal(err)
	}
	return root
}

func duTotal(t *testing.T, root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.Type().IsRegular() {
			info, _ := d.Info()
			total += info.Size()
		}
		return nil
	})
	return total
}

func TestDriveScan(t *testing.T) {
	root := fakeWindowsTree(t)
	t.Setenv("DD_DRIVES", root)
	j := newScanJob(root)
	if !j.IsDriveRoot {
		t.Fatal("expected drive root")
	}
	j.Start()
	for !j.done.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	if e := j.Err(); e != "" {
		t.Fatal(e)
	}
	want := duTotal(t, root)
	if j.root.Size != want {
		t.Fatalf("total %d, want %d", j.root.Size, want)
	}
	if j.root.Files != 23 {
		t.Fatalf("files %d, want 23", j.root.Files)
	}
	// children sorted by size desc
	for i := 1; i < len(j.root.Children); i++ {
		if j.root.Children[i-1].Size < j.root.Children[i].Size {
			t.Fatal("children not sorted")
		}
	}
	find := func(path string) *Node {
		n := j.root
		for _, part := range strings.Split(path, "/") {
			var next *Node
			for _, c := range n.Children {
				if c.Name == part {
					next = c
				}
			}
			if next == nil {
				t.Fatalf("missing %s", path)
			}
			n = next
		}
		return n
	}
	if k := find("Windows").Kind; k != KindSystem {
		t.Errorf("Windows kind %d", k)
	}
	if k := find("Program Files (x86)/Steam/steamapps").Kind; k != KindGame {
		t.Errorf("steamapps kind %d", k)
	}
	if k := find("$Recycle.Bin").Kind; k != KindRecycle {
		t.Errorf("recycle kind %d", k)
	}
	if k := find("Users").Kind; k != KindNormal {
		t.Errorf("users kind %d", k)
	}
	for _, c := range j.root.Children {
		if c.Name == "Documents and Settings" {
			t.Error("symlink was followed")
		}
	}
	if got := j.typeBytes[TGame]; got != 311_000 {
		t.Errorf("game bytes %d", got)
	}
	if got := j.typeBytes[TVideo]; got != 500_000 {
		t.Errorf("video bytes %d", got)
	}
	if got := j.typeBytes[TRecycle]; got != 45_000 {
		t.Errorf("recycle bytes %d", got)
	}
	wantSys := int64(100_000 + 7_000 + 33_000 + 200_000 + 400_000 + 300_000)
	if got := j.typeBytes[TSystem]; got != wantSys {
		t.Errorf("system bytes %d want %d", got, wantSys)
	}
	if j.topAll[0].Name != "movie.mp4" {
		t.Errorf("top file %s", j.topAll[0].Name)
	}
	for _, f := range j.topUser {
		if f.Type == TSystem {
			t.Errorf("system file %s in user list", f.Name)
		}
	}
	if len(j.topByType[TInstaller]) != 1 || j.topByType[TInstaller][0].Name != "setup.exe" {
		t.Errorf("installer list %+v", j.topByType[TInstaller])
	}
	if p := j.topAll[0].Dir.Path(); p != filepath.Join(root, "Users", "me", "Videos") {
		t.Errorf("path %s", p)
	}
	if j.Node(0) != j.root {
		t.Error("root id")
	}
}

func setFakeEnv(t *testing.T, root string) {
	me := filepath.Join(root, "Users", "me")
	t.Setenv("LOCALAPPDATA", filepath.Join(me, "AppData", "Local"))
	t.Setenv("APPDATA", filepath.Join(me, "AppData", "Roaming"))
	t.Setenv("USERPROFILE", me)
	t.Setenv("ProgramData", filepath.Join(root, "ProgramData"))
	t.Setenv("SystemRoot", filepath.Join(root, "Windows"))
	t.Setenv("SystemDrive", root)
	t.Setenv("TMPDIR", filepath.Join(me, "AppData", "Local", "Temp"))
	t.Setenv("DD_DOWNLOADS", filepath.Join(me, "Downloads"))
	t.Setenv("DD_DOCUMENTS", filepath.Join(me, "Documents"))
	t.Setenv("DD_STEAM", filepath.Join(root, "Program Files (x86)", "Steam"))
	t.Setenv("DD_TRASH", filepath.Join(root, "$Recycle.Bin"))
}

func TestJunkScan(t *testing.T) {
	root := fakeWindowsTree(t)
	setFakeEnv(t, root)
	j := newJunkJob()
	j.Start()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j.mu.Lock()
		running := j.running
		j.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := map[string]*JunkCat{}
	for _, c := range j.cats {
		got[c.ID] = c
	}
	check := func(id string, bytes int64) {
		t.Helper()
		c := got[id]
		if c == nil {
			t.Fatalf("no category %s", id)
		}
		if !c.Found || c.Bytes != bytes {
			t.Errorf("%s: found=%v bytes=%d want %d parts=%+v", id, c.Found, c.Bytes, bytes, c.Parts)
		}
	}
	check("usertemp", 12_000)
	check("wintemp", 7_000)
	check("wucache", 33_000)
	check("chrome", 80_000+20_000+10_000) // ShaderCache\GPUCache counted once
	check("sitedata", 40_000)
	check("thumbs", 9_000)
	check("kakaofiles", 25_000)
	check("kakaodata", 70_000)
	check("dlinstall", 150_000+60_000)
	check("dlold", 50_000)
	check("hiber", 400_000)
	check("recycle", 45_000)
	check("steam", 11_000)
	if got["edge"].Found {
		t.Error("edge should not be found")
	}
	if c := got["chrome"]; len(c.Parts) != 3 {
		t.Errorf("chrome parts %+v", c.Parts)
	} else if c.Parts[0].Label != "기본 프로필" {
		t.Errorf("chrome first part label %q", c.Parts[0].Label)
	}
	if c := got["hiber"]; !c.Parts[0].IsFile || filepath.Base(c.Parts[0].Path) != "hiberfil.sys" {
		t.Errorf("hiber part %+v", c.Parts[0])
	}
}

func TestDupes(t *testing.T) {
	root := t.TempDir()
	big := bytes.Repeat([]byte("abcdefgh"), 40_000) // 320 KB, needs the full hash
	other := append([]byte(nil), big...)
	other[150_000] = 'Z' // same size, same head and tail, different middle
	write := func(rel string, data []byte) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("a/movie.bin", big)
	write("b/movie copy.bin", big)
	write("c/different.bin", other)
	small := bytes.Repeat([]byte{7}, 2_000_000)
	write("photos/1.jpg", small)
	p2 := write("backup/1.jpg", small)
	if err := os.Link(p2, filepath.Join(root, "backup", "hardlink.jpg")); err != nil {
		t.Fatal(err)
	}
	write("AppData/x.jpg", small)
	write("tiny/a.txt", []byte("hello"))
	write("tiny/b.txt", []byte("hello"))

	j := newDupeJob(root, 100_000, true)
	j.Start()
	for !j.done.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	st := j.status(true)
	if st.Error != "" {
		t.Fatal(st.Error)
	}
	if st.GroupCount != 2 {
		for _, g := range st.Groups {
			t.Logf("group %d x%d %v", g.Size, g.Count, g.Files)
		}
		t.Fatalf("groups %d", st.GroupCount)
	}
	if st.Groups[0].Size != 2_000_000 || st.Groups[0].Count != 2 {
		t.Errorf("group0 %+v", st.Groups[0])
	}
	if st.Groups[1].Size != int64(len(big)) || st.Groups[1].Count != 2 {
		t.Errorf("group1 %+v", st.Groups[1])
	}
	if st.Wasted != 2_000_000+int64(len(big)) {
		t.Errorf("wasted %d", st.Wasted)
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		p, s string
		ok   bool
	}{
		{"thumbcache_*.db", "thumbcache_256.db", true},
		{"thumbcache_*.db", "THUMBCACHE_idx.DB", true},
		{"thumbcache_*.db", "iconcache_1.db", false},
		{"*", "anything", true},
		{"Profile ?", "Profile 1", true},
		{"Profile ?", "Profile 12", false},
		{"SpotifyAB.SpotifyMusic_*", "SpotifyAB.SpotifyMusic_zpdnekdrzrea0", true},
	}
	for _, c := range cases {
		if globMatch(c.p, c.s) != c.ok {
			t.Errorf("globMatch(%q,%q) != %v", c.p, c.s, c.ok)
		}
	}
}
