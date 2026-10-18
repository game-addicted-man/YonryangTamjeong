package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Safety string

const (
	SafeLevel    Safety = "safe"
	ReviewLevel  Safety = "review"
	CautionLevel Safety = "caution"
)

// jTarget describes where a category's files live. rel uses '\' separators
// and may contain wildcards in directory components (e.g. `*\Cache`).
type jTarget struct {
	base    string
	rel     string
	pattern string              // ";"-separated file patterns, non-recursive, inside the resolved dir
	filter  func(e *Entry) bool // optional filter for recursive sums
	label   string
}

type JunkPart struct {
	Label  string `json:"label"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Files  int64  `json:"files"`
	Denied bool   `json:"denied,omitempty"`
	IsFile bool   `json:"isFile,omitempty"`
}

type JunkCat struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Desc      string     `json:"desc"`
	Tip       string     `json:"tip"`
	Safety    Safety     `json:"safety"`
	Bytes     int64      `json:"bytes"`
	Files     int64      `json:"files"`
	Denied    bool       `json:"denied"`
	Found     bool       `json:"found"`
	Done      bool       `json:"done"`
	Parts     []JunkPart `json:"parts"`
	Open      string     `json:"open"`
	Tool      string     `json:"tool,omitempty"`
	ToolLabel string     `json:"toolLabel,omitempty"`

	targets []jTarget
	custom  func() ([]JunkPart, bool)
}

type jEnv struct {
	local, roaming, profile, programData, windir, sysRoot, temp, downloads, documents, steam string
}

func trimSep(p string) string {
	if len(p) > 3 {
		return strings.TrimRight(p, `\/`)
	}
	return p
}

func currentJunkEnv() jEnv {
	e := jEnv{
		local:       trimSep(os.Getenv("LOCALAPPDATA")),
		roaming:     trimSep(os.Getenv("APPDATA")),
		profile:     trimSep(os.Getenv("USERPROFILE")),
		programData: trimSep(os.Getenv("ProgramData")),
		windir:      trimSep(os.Getenv("SystemRoot")),
		temp:        trimSep(os.TempDir()),
		downloads:   knownFolder("downloads"),
		documents:   knownFolder("documents"),
		steam:       steamPath(),
	}
	if e.programData == "" {
		e.programData = trimSep(os.Getenv("ALLUSERSPROFILE"))
	}
	if e.windir == "" {
		e.windir = trimSep(os.Getenv("windir"))
	}
	if sd := os.Getenv("SystemDrive"); sd != "" {
		e.sysRoot = strings.TrimRight(sd, `\/`) + string(sepChar)
	}
	return e
}

func tg(base, rel string) jTarget        { return jTarget{base: base, rel: rel} }
func tl(label, base, rel string) jTarget { return jTarget{base: base, rel: rel, label: label} }
func tp(label, base, rel, pat string) jTarget {
	return jTarget{base: base, rel: rel, pattern: pat, label: label}
}
func tf(label, base, rel string, f func(*Entry) bool) jTarget {
	return jTarget{base: base, rel: rel, filter: f, label: label}
}

var chromiumCacheRels = []string{`*\Cache`, `*\Code Cache`, `*\GPUCache`, `*\DawnCache`,
	`*\DawnGraphiteCache`, `*\DawnWebGPUCache`, `ShaderCache`, `GrShaderCache`, `GraphiteDawnCache`}

const browserTip = "브라우저에서 Ctrl+Shift+Delete를 누르고 '캐시된 이미지 및 파일'만 체크한 뒤 삭제하세요. 로그인 정보나 방문 기록은 그대로 남아요."
const browserDesc = "웹페이지를 빨리 열려고 브라우저가 저장해 둔 이미지·스크립트예요. 지워도 다시 생겨요."

func buildCatalog(e jEnv) []*JunkCat {
	setOf := func(exts string) map[string]bool {
		m := map[string]bool{}
		for _, x := range strings.Fields(exts) {
			m[x] = true
		}
		return m
	}
	installers := setOf("exe msi msix msixbundle appx appxbundle")
	archives := setOf("zip rar 7z alz egg tar gz bz2 xz tgz cab zst")
	diskImages := setOf("iso img")
	cutoff := time.Now().AddDate(0, 0, -90).Unix()
	isInst := func(en *Entry) bool { return installers[extOf(en.Name)] }
	isArch := func(en *Entry) bool { return archives[extOf(en.Name)] }
	isImg := func(en *Entry) bool { return diskImages[extOf(en.Name)] }
	isOld := func(en *Entry) bool {
		x := extOf(en.Name)
		if installers[x] || archives[x] || diskImages[x] {
			return false
		}
		return en.MTime > 0 && en.MTime < cutoff
	}

	var cats []*JunkCat
	add := func(c *JunkCat) { cats = append(cats, c) }

	add(&JunkCat{ID: "recycle", Name: "휴지통", Safety: SafeLevel,
		Desc: "지운 파일은 휴지통을 비우기 전까지 실제로는 계속 공간을 차지해요.",
		Tip:  "바탕화면의 휴지통을 마우스 오른쪽 버튼으로 클릭하고 [휴지통 비우기]를 누르세요. 비우면 되돌릴 수 없으니 필요한 파일이 없는지 먼저 열어서 확인하세요.",
		Open: "shell:RecycleBinFolder", Tool: "recyclebin", ToolLabel: "휴지통 열기",
		custom: recycleBinParts})

	add(&JunkCat{ID: "usertemp", Name: "사용자 임시 파일", Safety: SafeLevel,
		Desc: "프로그램을 설치하거나 실행하면서 잠깐 쓰고 남겨 둔 파일이에요.",
		Tip:  "[설정 > 시스템 > 저장소 > 임시 파일]에서 '임시 파일'을 선택해 지우는 게 가장 쉬워요. 직접 지우려면 Win+R → %temp% 입력 → 전체 선택 후 삭제하고, '사용 중인 파일'이라고 나오면 [건너뛰기]를 누르세요.",
		Tool: "storage", ToolLabel: "저장소 설정 열기",
		targets: []jTarget{tl("내 임시 폴더", e.temp, "")}})

	add(&JunkCat{ID: "wintemp", Name: "Windows 임시 파일", Safety: SafeLevel,
		Desc: "Windows와 설치 프로그램이 남긴 임시 파일이에요.",
		Tip:  "[설정 > 시스템 > 저장소 > 임시 파일]에서 정리하는 게 가장 안전해요. 폴더를 직접 열려면 관리자 권한이 필요할 수 있어요.",
		Tool: "storage", ToolLabel: "저장소 설정 열기",
		targets: []jTarget{tl("Windows\\Temp", e.windir, "Temp")}})

	add(&JunkCat{ID: "wucache", Name: "Windows 업데이트 캐시", Safety: SafeLevel,
		Desc: "설치가 끝난 업데이트의 설치 파일이 그대로 남아 있는 경우가 많아요.",
		Tip:  "[디스크 정리]를 실행하고 [시스템 파일 정리] → 'Windows 업데이트 정리'를 체크하세요. 폴더를 직접 지우는 건 권하지 않아요.",
		Tool: "cleanmgr", ToolLabel: "디스크 정리 실행",
		targets: []jTarget{tl("업데이트 다운로드", e.windir, `SoftwareDistribution\Download`)}})

	add(&JunkCat{ID: "dooptim", Name: "배달 최적화 파일", Safety: SafeLevel,
		Desc: "업데이트를 다른 PC와 나눠 받으려고 보관해 둔 파일이에요.",
		Tip:  "[설정 > 시스템 > 저장소 > 임시 파일]에서 '배달 최적화 파일'을 선택해 지우세요.",
		Tool: "storage", ToolLabel: "저장소 설정 열기",
		targets: []jTarget{
			tl("배달 최적화 캐시", e.windir, `ServiceProfiles\NetworkService\AppData\Local\Microsoft\Windows\DeliveryOptimization\Cache`),
			tl("배달 최적화 (이전 위치)", e.windir, `SoftwareDistribution\DeliveryOptimization`),
		}})

	add(&JunkCat{ID: "crash", Name: "오류 보고서 · 메모리 덤프", Safety: SafeLevel,
		Desc: "프로그램이나 Windows가 오류로 멈췄을 때 남긴 진단 파일이에요.",
		Tip:  "[디스크 정리]에서 '시스템 오류 메모리 덤프 파일'과 'Windows 오류 보고서'를 체크해 정리하세요.",
		Tool: "cleanmgr", ToolLabel: "디스크 정리 실행",
		targets: []jTarget{
			tl("Windows 오류 보고 (공용)", e.programData, `Microsoft\Windows\WER`),
			tl("Windows 오류 보고 (내 계정)", e.local, `Microsoft\Windows\WER`),
			tl("프로그램 충돌 덤프", e.local, "CrashDumps"),
			tl("미니 덤프", e.windir, "Minidump"),
			tl("커널 오류 보고", e.windir, "LiveKernelReports"),
			tp("메모리 덤프 (MEMORY.DMP)", e.windir, "", "MEMORY.DMP"),
		}})

	add(&JunkCat{ID: "thumbs", Name: "썸네일 · 아이콘 캐시", Safety: SafeLevel,
		Desc: "사진 미리보기를 빨리 보여주려고 저장해 둔 파일이에요. 지워도 자동으로 다시 만들어져요.",
		Tip:  "[디스크 정리]에서 '미리 보기 사진'을 체크해 정리하세요.",
		Tool: "cleanmgr", ToolLabel: "디스크 정리 실행",
		targets: []jTarget{tp("thumbcache / iconcache", e.local, `Microsoft\Windows\Explorer`, "thumbcache_*.db;iconcache_*.db")}})

	browsers := []struct{ id, name, base, rel string }{
		{"chrome", "Chrome 캐시", e.local, `Google\Chrome\User Data`},
		{"edge", "Edge 캐시", e.local, `Microsoft\Edge\User Data`},
		{"whale", "네이버 웨일 캐시", e.local, `Naver\Naver Whale\User Data`},
		{"brave", "Brave 캐시", e.local, `BraveSoftware\Brave-Browser\User Data`},
		{"vivaldi", "Vivaldi 캐시", e.local, `Vivaldi\User Data`},
	}
	var swTargets []jTarget
	for _, b := range browsers {
		var ts []jTarget
		for _, r := range chromiumCacheRels {
			if strings.HasPrefix(r, `*\`) {
				ts = append(ts, tg(e.base(b.base, b.rel), r)) // per profile: labelled by profile name
			} else {
				ts = append(ts, tl("공용 그래픽 캐시", e.base(b.base, b.rel), r))
			}
		}
		add(&JunkCat{ID: b.id, Name: b.name, Safety: SafeLevel, Desc: browserDesc, Tip: browserTip, targets: ts})
		short := strings.TrimSuffix(b.name, " 캐시")
		swTargets = append(swTargets,
			tl(short, e.base(b.base, b.rel), `*\Service Worker\CacheStorage`),
			tl(short, e.base(b.base, b.rel), `*\Service Worker\ScriptCache`))
	}
	var operaTs []jTarget
	for _, o := range []struct{ label, rel string }{{"Opera", `Opera Software\Opera Stable`}, {"Opera GX", `Opera Software\Opera GX Stable`}} {
		for _, r := range []string{"Cache", "Code Cache", "GPUCache", `Default\Cache`, `Default\Code Cache`, `Default\GPUCache`} {
			operaTs = append(operaTs, tl(o.label, e.base(e.local, o.rel), r))
		}
	}
	add(&JunkCat{ID: "opera", Name: "Opera 캐시", Safety: SafeLevel, Desc: browserDesc, Tip: browserTip, targets: operaTs})
	add(&JunkCat{ID: "firefox", Name: "Firefox 캐시", Safety: SafeLevel, Desc: browserDesc,
		Tip: "Firefox에서 Ctrl+Shift+Delete를 누르고 '캐시'만 체크한 뒤 삭제하세요. 로그인 정보는 그대로 남아요.",
		targets: []jTarget{
			tg(e.base(e.local, `Mozilla\Firefox\Profiles`), `*\cache2`),
			tg(e.base(e.local, `Mozilla\Firefox\Profiles`), `*\startupCache`),
			tg(e.base(e.local, `Mozilla\Firefox\Profiles`), `*\thumbnails`),
		}})

	add(&JunkCat{ID: "sitedata", Name: "브라우저 사이트 저장소", Safety: CautionLevel,
		Desc:    "웹사이트가 오프라인용으로 저장해 둔 데이터(서비스 워커)예요. 몇 GB까지 커지기도 해요.",
		Tip:     "브라우저 설정 > 개인정보 > '쿠키 및 사이트 데이터'에서 안 쓰는 사이트만 골라 지우세요. 한꺼번에 지우면 여러 사이트에서 로그아웃될 수 있어요.",
		targets: swTargets})

	add(&JunkCat{ID: "shader", Name: "그래픽 셰이더 캐시", Safety: SafeLevel,
		Desc: "게임이나 그래픽 프로그램이 화면 효과를 빨리 그리려고 저장한 파일이에요.",
		Tip:  "[디스크 정리]에서 'DirectX 셰이더 캐시'를 체크해 정리하세요. 지운 뒤 처음 실행하는 게임은 잠깐 끊길 수 있어요.",
		Tool: "cleanmgr", ToolLabel: "디스크 정리 실행",
		targets: []jTarget{
			tl("DirectX 셰이더 캐시", e.local, "D3DSCache"),
			tl("NVIDIA DX 캐시", e.local, `NVIDIA\DXCache`),
			tl("NVIDIA GL 캐시", e.local, `NVIDIA\GLCache`),
			tl("NVIDIA Optix 캐시", e.local, `NVIDIA\OptixCache`),
			tl("NVIDIA 캐시 (공용)", e.programData, `NVIDIA Corporation\NV_Cache`),
			tl("AMD DX 캐시", e.local, `AMD\DxCache`),
			tl("AMD DXC 캐시", e.local, `AMD\DxcCache`),
			tl("AMD GL 캐시", e.local, `AMD\GLCache`),
			tl("AMD Vulkan 캐시", e.local, `AMD\VkCache`),
			tl("Intel 셰이더 캐시", e.local, `Intel\ShaderCache`),
		}})

	add(&JunkCat{ID: "appcache", Name: "앱 캐시 (디스코드·VS Code 등)", Safety: SafeLevel,
		Desc: "메신저·음악·편집 프로그램이 쌓아 둔 캐시예요. 지워도 앱이 필요할 때 다시 받아요.",
		Tip:  "해당 앱을 완전히 종료한 뒤 폴더 안의 파일을 지우거나, 앱 설정의 '캐시 지우기' 기능을 이용하세요.",
		targets: []jTarget{
			tl("디스코드", e.roaming, `discord\Cache`),
			tl("디스코드", e.roaming, `discord\Code Cache`),
			tl("디스코드", e.roaming, `discord\GPUCache`),
			tl("슬랙", e.roaming, `Slack\Cache`),
			tl("슬랙", e.roaming, `Slack\Code Cache`),
			tl("슬랙", e.roaming, `Slack\Service Worker\CacheStorage`),
			tl("VS Code", e.roaming, `Code\Cache`),
			tl("VS Code", e.roaming, `Code\CachedData`),
			tl("VS Code", e.roaming, `Code\Code Cache`),
			tl("VS Code", e.roaming, `Code\CachedExtensionVSIXs`),
			tl("Teams (클래식)", e.roaming, `Microsoft\Teams\Cache`),
			tl("Teams (클래식)", e.roaming, `Microsoft\Teams\Service Worker\CacheStorage`),
			tl("Spotify", e.local, `Spotify\Storage`),
			tl("Spotify (스토어 버전)", e.local, `Packages\SpotifyAB.SpotifyMusic_*\LocalCache\Spotify\Data`),
			tl("Adobe 미디어 캐시", e.roaming, `Adobe\Common\Media Cache Files`),
			tl("Adobe 미디어 캐시", e.roaming, `Adobe\Common\Media Cache`),
			tl("Windows 인터넷 캐시", e.local, `Microsoft\Windows\INetCache`),
		}})

	if e.steam != "" {
		add(&JunkCat{ID: "steam", Name: "Steam 캐시", Safety: SafeLevel,
			Desc: "스팀이 받아 둔 셰이더·웹 캐시와 덜 받은 다운로드 파일이에요.",
			Tip:  "스팀 [설정 > 다운로드 > 다운로드 캐시 지우기]를 이용하세요. 셰이더 캐시는 게임을 실행하면 다시 만들어져요.",
			targets: []jTarget{
				tl("셰이더 캐시", e.steam, `steamapps\shadercache`),
				tl("웹 캐시", e.steam, `appcache\httpcache`),
				tl("덜 받은 다운로드", e.steam, `steamapps\downloading`),
				tl("임시 파일", e.steam, `steamapps\temp`),
				tl("오류 덤프", e.steam, "dumps"),
				tl("로그", e.steam, "logs"),
			}})
	}

	add(&JunkCat{ID: "devcache", Name: "개발 도구 캐시", Safety: ReviewLevel,
		Desc: "개발 도구가 내려받아 둔 패키지 캐시예요. 지워도 필요할 때 다시 받아요.",
		Tip:  "폴더를 직접 지우기보다 npm cache clean --force, pip cache purge, dotnet nuget locals all --clear 처럼 각 도구의 정리 명령을 쓰는 게 안전해요. 지운 뒤 첫 빌드는 느려질 수 있어요.",
		targets: []jTarget{
			tl("npm", e.local, "npm-cache"),
			tl("Yarn", e.local, `Yarn\Cache`),
			tl("pnpm", e.local, `pnpm\store`),
			tl("pnpm", e.profile, ".pnpm-store"),
			tl("pip", e.local, `pip\Cache`),
			tl("uv", e.local, `uv\cache`),
			tl("Go 빌드 캐시", e.local, "go-build"),
			tl("NuGet 패키지", e.profile, `.nuget\packages`),
			tl("NuGet 캐시", e.local, `NuGet\v3-cache`),
			tl("Gradle", e.profile, `.gradle\caches`),
			tl("Maven", e.profile, `.m2\repository`),
			tl("Cargo", e.profile, `.cargo\registry`),
			tl("Anaconda 패키지", e.profile, `anaconda3\pkgs`),
			tl("Miniconda 패키지", e.profile, `miniconda3\pkgs`),
			tl("Playwright 브라우저", e.local, "ms-playwright"),
		}})

	add(&JunkCat{ID: "iosbackup", Name: "아이폰 · 아이패드 백업", Safety: ReviewLevel,
		Desc: "iTunes나 Apple 기기 앱으로 만든 백업이에요. 오래된 백업이 여러 개 쌓여 있을 수 있어요.",
		Tip:  "iTunes(또는 Apple 기기 앱)에서 설정 > 기기를 열고 오래된 백업만 골라 삭제하세요. 폴더를 직접 지우면 어떤 기기의 백업인지 알기 어려워요.",
		targets: []jTarget{
			tl("iTunes 백업", e.roaming, `Apple Computer\MobileSync\Backup`),
			tl("Apple 기기 앱 백업", e.profile, `Apple\MobileSync\Backup`),
		}})

	add(&JunkCat{ID: "kakaofiles", Name: "카카오톡 받은 파일", Safety: ReviewLevel,
		Desc: "카카오톡 PC에서 받은 사진·문서가 자동으로 저장되는 폴더예요.",
		Tip:  "필요한 파일만 다른 폴더로 옮겨 두고 나머지는 지우세요. 크기순으로 정렬하면 큰 동영상부터 찾을 수 있어요.",
		targets: []jTarget{
			tl("카카오톡 받은 파일", e.documents, "카카오톡 받은 파일"),
			tl("카카오톡 받은 파일", e.profile, `Documents\카카오톡 받은 파일`),
		}})

	add(&JunkCat{ID: "kakaodata", Name: "카카오톡 PC 데이터", Safety: CautionLevel,
		Desc:    "채팅방에서 본 사진·동영상 캐시와 대화 데이터가 함께 들어 있어요.",
		Tip:     "이 폴더를 직접 지우면 대화 내용이 사라질 수 있어요. 카카오톡 PC의 설정에 있는 캐시·저장 공간 정리 기능을 이용하세요.",
		targets: []jTarget{tl("카카오톡 데이터", e.local, `Kakao\KakaoTalk`)}})

	add(&JunkCat{ID: "dlinstall", Name: "다운로드 폴더 · 설치/압축 파일", Safety: ReviewLevel,
		Desc: "이미 설치한 프로그램의 설치 파일이나 풀어 본 압축 파일이 그대로 남아 있는 경우가 많아요.",
		Tip:  "다운로드 폴더를 '크기' 순으로 정렬해서 다시 쓸 일 없는 설치 파일(.exe, .msi)과 압축 파일(.zip 등)을 지우세요.",
		targets: []jTarget{
			tf("설치 파일 (.exe, .msi 등)", e.downloads, "", isInst),
			tf("압축 파일 (.zip, .7z, .alz 등)", e.downloads, "", isArch),
			tf("디스크 이미지 (.iso 등)", e.downloads, "", isImg),
		}})

	add(&JunkCat{ID: "dlold", Name: "다운로드 폴더 · 90일 넘은 파일", Safety: ReviewLevel,
		Desc:    "석 달 넘게 그대로 있는 다운로드 파일이에요. 잊고 있던 큰 파일이 숨어 있을 수 있어요.",
		Tip:     "다운로드 폴더에서 '수정한 날짜' 순으로 정렬해 오래된 파일부터 확인하세요.",
		targets: []jTarget{tf("90일 넘게 그대로인 파일", e.downloads, "", isOld)}})

	add(&JunkCat{ID: "driverleft", Name: "그래픽 드라이버 설치 잔여 파일", Safety: ReviewLevel,
		Desc: "그래픽 드라이버를 설치할 때 풀어 둔 설치 파일이 그대로 남아 있는 거예요.",
		Tip:  "드라이버 설치가 끝났다면 폴더째 지워도 돼요. 드라이버를 설치하는 중이라면 끝난 뒤에 지우세요.",
		targets: []jTarget{
			tl("C:\\AMD", e.sysRoot, "AMD"),
			tl("C:\\NVIDIA", e.sysRoot, "NVIDIA"),
		}})

	add(&JunkCat{ID: "winold", Name: "이전 Windows · 업그레이드 잔여 파일", Safety: CautionLevel,
		Desc: "Windows를 업그레이드하기 전의 시스템 파일이에요. 되돌리기(롤백)용으로 보관돼요.",
		Tip:  "[설정 > 시스템 > 저장소 > 임시 파일]에서 '이전 Windows 설치'를 선택해 지우세요. 지우면 이전 버전으로 되돌릴 수 없어요.",
		Tool: "storage", ToolLabel: "저장소 설정 열기",
		targets: []jTarget{
			tl("Windows.old", e.sysRoot, "Windows.old"),
			tl("$WINDOWS.~BT", e.sysRoot, "$WINDOWS.~BT"),
			tl("$Windows.~WS", e.sysRoot, "$Windows.~WS"),
			tl("ESD", e.sysRoot, "ESD"),
		}})

	add(&JunkCat{ID: "hiber", Name: "최대 절전 모드 파일", Safety: CautionLevel,
		Desc:    "최대 절전 모드와 빠른 시작을 위해 Windows가 미리 잡아 둔 공간이에요.",
		Tip:     "최대 절전 모드를 쓰지 않는다면 관리자 권한 명령 프롬프트에서 powercfg /h off 를 실행하면 사라져요. 이 경우 '빠른 시작' 기능도 함께 꺼져요.",
		targets: []jTarget{tp("hiberfil.sys", e.sysRoot, "", "hiberfil.sys")}})

	return cats
}

// base joins a base folder with a relative path, returning "" when the base is unknown.
func (e jEnv) base(base, rel string) string {
	if base == "" {
		return ""
	}
	for _, p := range strings.Split(rel, `\`) {
		if p != "" {
			base = joinPath(base, p)
		}
	}
	return base
}

// ---- target expansion ----

type expanded struct {
	path  string
	wild  []string
	last  string
	found bool
}

func expandTarget(t *jTarget) []expanded {
	if t.base == "" {
		return nil
	}
	var parts []string
	for _, p := range strings.Split(t.rel, `\`) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	var out []expanded
	var rec func(cur string, i int, wild []string)
	rec = func(cur string, i int, wild []string) {
		if i == len(parts) {
			if dirExists(cur) {
				last := ""
				if len(parts) > 0 {
					last = parts[len(parts)-1]
				}
				out = append(out, expanded{path: cur, wild: append([]string(nil), wild...), last: last})
			}
			return
		}
		p := parts[i]
		if !hasWildcard(p) {
			rec(joinPath(cur, p), i+1, wild)
			return
		}
		entries, err := listDir(cur, nil)
		if err != nil {
			return
		}
		for _, en := range entries {
			if en.IsDir && !en.SkipRecurse && globMatch(p, en.Name) {
				rec(joinPath(cur, en.Name), i+1, append(wild, en.Name))
			}
		}
	}
	rec(t.base, 0, nil)
	return out
}

func profileLabel(name string) string {
	switch {
	case name == "Default":
		return "기본 프로필"
	case strings.HasPrefix(name, "Profile "):
		return "프로필 " + strings.TrimPrefix(name, "Profile ")
	case name == "Guest Profile":
		return "게스트 프로필"
	case name == "System Profile":
		return "시스템 프로필"
	}
	if i := strings.IndexByte(name, '.'); i > 0 && i < len(name)-1 { // firefox "abcd1234.default-release"
		return name[i+1:]
	}
	return name
}

func partLabel(t *jTarget, x expanded) string {
	if t.label != "" {
		if len(x.wild) > 0 && !strings.Contains(t.rel, "SpotifyAB") {
			return t.label + " · " + profileLabel(x.wild[0])
		}
		return t.label
	}
	if len(x.wild) > 0 {
		return profileLabel(x.wild[0])
	}
	if x.last != "" {
		return x.last
	}
	return x.path
}

// ---- directory sums ----

func sumDir(root string, filter func(*Entry) bool, cancel *atomic.Bool) (bytes, files int64, denied bool) {
	stack := []string{root}
	var buf []Entry
	for len(stack) > 0 {
		if cancel != nil && cancel.Load() {
			return
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		var err error
		buf, err = listDir(dir, buf[:0])
		if err != nil {
			if errors.Is(err, errDenied) {
				denied = true
			}
			continue
		}
		for i := range buf {
			en := &buf[i]
			if en.IsDir {
				if !en.SkipRecurse {
					stack = append(stack, joinPath(dir, en.Name))
				}
				continue
			}
			if filter != nil && !filter(en) {
				continue
			}
			bytes += en.Size
			files++
		}
	}
	return
}

// ---- job ----

type JunkJob struct {
	mu        sync.Mutex
	cats      []*JunkCat
	start     time.Time
	end       time.Time
	running   bool
	completed int
	cancel    atomic.Bool
}

type jWork struct {
	cat      *JunkCat
	t        *jTarget
	x        expanded
	covering bool
}

func newJunkJob() *JunkJob {
	return &JunkJob{cats: buildCatalog(currentJunkEnv())}
}

func (j *JunkJob) Start() {
	j.mu.Lock()
	j.start = time.Now()
	j.running = true
	j.mu.Unlock()
	go j.run()
}

func (j *JunkJob) run() {
	defer func() {
		if r := recover(); r != nil {
			logf("junk panic: %v", r)
		}
		j.mu.Lock()
		j.running = false
		j.end = time.Now()
		for _, c := range j.cats {
			c.Done = true
		}
		j.mu.Unlock()
	}()

	// 1. expand all targets
	var works []*jWork
	for _, c := range j.cats {
		for i := range c.targets {
			t := &c.targets[i]
			for _, x := range expandTarget(t) {
				works = append(works, &jWork{cat: c, t: t, x: x, covering: t.pattern == "" && t.filter == nil})
			}
		}
	}
	// 2. drop targets already covered by another (avoid double counting)
	byCat := map[*JunkCat][]*jWork{}
	for i, w := range works {
		dup := false
		nw := normPath(w.x.path)
		for k, c := range works {
			if k == i || !c.covering {
				continue
			}
			nc := normPath(c.x.path)
			if nw == nc {
				if !w.covering || k < i {
					dup = true
					break
				}
			} else if strings.HasPrefix(nw, nc+string(sepChar)) {
				dup = true
				break
			}
		}
		if !dup {
			byCat[w.cat] = append(byCat[w.cat], w)
		}
	}

	// 3. scan categories in parallel
	queue := make(chan *JunkCat, len(j.cats))
	for _, c := range j.cats {
		queue <- c
	}
	close(queue)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range queue {
				if j.cancel.Load() {
					return
				}
				j.scanCategory(c, byCat[c])
			}
		}()
	}
	wg.Wait()
}

func (j *JunkJob) scanCategory(c *JunkCat, works []*jWork) {
	defer func() {
		if r := recover(); r != nil {
			logf("junk category %s panic: %v", c.ID, r)
		}
	}()
	var parts []JunkPart
	found := false
	if c.custom != nil {
		parts, found = c.custom()
	}
	for _, w := range works {
		part := JunkPart{Label: partLabel(w.t, w.x), Path: w.x.path}
		if w.t.pattern != "" {
			entries, err := listDir(w.x.path, nil)
			if errors.Is(err, errDenied) {
				part.Denied = true
			}
			var matched []string
			for _, en := range entries {
				if en.IsDir {
					continue
				}
				for _, p := range strings.Split(w.t.pattern, ";") {
					if globMatch(p, en.Name) {
						part.Bytes += en.Size
						part.Files++
						matched = append(matched, en.Name)
						break
					}
				}
			}
			if len(matched) == 0 && !part.Denied {
				continue
			}
			if len(matched) == 1 && !hasWildcard(w.t.pattern) {
				part.Path = joinPath(w.x.path, matched[0])
				part.IsFile = true
			}
		} else {
			part.Bytes, part.Files, part.Denied = sumDir(w.x.path, w.t.filter, &j.cancel)
			if w.t.filter != nil && part.Files == 0 && !part.Denied {
				found = true // the folder exists, it just has nothing matching
				continue
			}
		}
		found = true
		parts = append(parts, part)
	}
	// merge parts that share a label (e.g. several cache folders of one app)
	merged := map[string]int{}
	biggest := map[string]int64{}
	var out []JunkPart
	for _, p := range parts {
		if idx, ok := merged[p.Label]; ok && !p.IsFile && !out[idx].IsFile && p.Path != "shell:RecycleBinFolder" {
			out[idx].Bytes += p.Bytes
			out[idx].Files += p.Files
			out[idx].Denied = out[idx].Denied || p.Denied
			if p.Bytes > biggest[p.Label] { // "open" should show the most useful folder
				biggest[p.Label] = p.Bytes
				out[idx].Path = p.Path
			}
			continue
		}
		merged[p.Label] = len(out)
		biggest[p.Label] = p.Bytes
		out = append(out, p)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Bytes > out[b].Bytes })

	var bytes, files int64
	denied := false
	for _, p := range out {
		bytes += p.Bytes
		files += p.Files
		denied = denied || p.Denied
	}

	j.mu.Lock()
	c.Parts = out
	c.Bytes = bytes
	c.Files = files
	c.Denied = denied
	c.Found = found
	c.Done = true
	if c.Open == "" && len(out) > 0 {
		c.Open = out[0].Path
	}
	j.completed++
	j.mu.Unlock()
}

type junkStatus struct {
	Running   bool       `json:"running"`
	Started   bool       `json:"started"`
	Total     int        `json:"total"`
	Completed int        `json:"completed"`
	ElapsedMs int64      `json:"elapsedMs"`
	Cats      []*JunkCat `json:"cats"`
}

// Snapshot must be encoded while holding the lock; it returns the encoded JSON.
func (j *JunkJob) snapshot(encode func(v interface{}) ([]byte, error)) ([]byte, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := junkStatus{Running: j.running, Started: !j.start.IsZero(), Total: len(j.cats), Completed: j.completed, Cats: j.cats}
	if !j.start.IsZero() {
		end := j.end
		if j.running || end.IsZero() {
			end = time.Now()
		}
		st.ElapsedMs = end.Sub(j.start).Milliseconds()
	}
	return encode(st)
}

func (c *JunkCat) String() string { return fmt.Sprintf("%s(%d)", c.ID, c.Bytes) }
