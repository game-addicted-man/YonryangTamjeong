# 용량 탐정 (Disk Detective) 소스 코드

쓸모없는 파일이 얼마나, 어디에 있는지 보여주는 Windows 프로그램입니다.
Go로 작성했고, 화면은 HTML/JS로 만들어 WebView2 창(없으면 Edge 앱 창)에 띄웁니다.
디스크를 읽기만 하고 파일을 지우지 않습니다.

## 파일 구성

| 파일 | 하는 일 |
|---|---|
| `main.go` | 시작점. 내부 화면 서버를 띄우고 창을 엽니다 |
| `server.go` | 127.0.0.1 전용 HTTP 서버와 API (토큰·Host 검사 포함) |
| `scan.go` | 드라이브/폴더 전체 검사 (병렬), 폴더 트리, 큰 파일 목록, 종류별 통계 |
| `junk.go` | 정리 대상 목록(임시 파일, 브라우저·카톡 캐시, 휴지통 등)과 검사 |
| `dupes.go` | 중복 파일 찾기 (크기 → 앞뒤 64KB 해시 → 전체 SHA-1, 하드 링크 제외) |
| `common.go` | 공용 타입, 파일 종류 분류, 경로·와일드카드 도우미, 로그 |
| `fs_windows.go` | Windows 전용: FindFirstFileExW 목록, 휴지통 크기, 드라이브 정보, 탐색기 열기, 관리자 재실행 |
| `ui_windows.go` | Windows 전용: WebView2 창, 실패 시 Edge/Chrome 앱 창으로 자동 전환 |
| `fs_other.go`, `ui_other.go` | 리눅스에서 개발·테스트할 때 쓰는 대체 구현 |
| `engine_test.go` | 검사 로직 테스트 (가짜 Windows 폴더 구조로 검증) |
| `web/index.html` | 화면 전체 (CSS·JS 포함, 외부 연결 없음) |
| `web/fonts/` | IBM Plex Sans KR 글꼴 (SIL OFL 1.1) |
| `winres/` | 아이콘 생성 스크립트, 앱 매니페스트 |
| `rsrc_windows_amd64.syso` | exe에 들어가는 아이콘·매니페스트·버전 정보 (빌드 시 자동 포함) |
| `third_party/` | 의존성 사본: `golang.org/x/sys`, `github.com/wailsapp/go-webview2` |

`third_party/go-webview2/pkg/edge/chromium.go`의 `errorCallback`은 한 군데 고쳤습니다.
원본은 오류가 나면 무조건 프로그램을 끄는데, 창이 뜬 뒤의 사소한 오류(크기 조절 실패 등)로는 꺼지지 않게 했습니다.

## 빌드 방법

Go 1.22 이상이 필요합니다. 의존성이 `third_party`에 들어 있어 인터넷 없이 빌드됩니다.

Windows (PowerShell, 이 폴더에서):

```powershell
go build -trimpath -ldflags "-s -w -H=windowsgui" -o 용량탐정.exe .
```

리눅스·맥에서 Windows용으로 만들 때:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H=windowsgui" -o 용량탐정.exe .
```

테스트는 리눅스·맥에서 `go test .`로 돌립니다.

## 실행 옵션

- `--browser` : 앱 창 대신 웹 브라우저 창으로 엽니다.
- 기록 파일: `%LOCALAPPDATA%\DiskDetective\log.txt`
