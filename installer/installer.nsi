; 용량 탐정 설치 프로그램 (NSIS 3, 유니코드)
;
; build/용량탐정.exe를 먼저 빌드한 뒤 저장소 폴더에서:
;   makensis -INPUTCHARSET UTF8 installer/installer.nsi     (리눅스·맥, build.sh가 실행)
;   makensis /INPUTCHARSET UTF8 installer\installer.nsi     (Windows)
; 결과: build/용량탐정_설치_v<버전>.exe
;
; 관리자 권한 없이 지금 사용자 계정에 설치합니다 (%LOCALAPPDATA%\Programs\용량 탐정).
; 버전은 main.go의 appVersion에서 읽습니다.

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma

!define APP_NAME   "용량 탐정"
!define APP_EXE    "용량탐정.exe"
!define APP_ID     "DiskDetective"
!searchparse /file "../main.go" `const appVersion = "` APP_VERSION `"`
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_ID}"
!define DATA_DIR   "$LOCALAPPDATA\DiskDetective" ; 앱이 기록 파일과 WebView2 캐시를 두는 곳

Name "${APP_NAME}"
OutFile "../build/용량탐정_설치_v${APP_VERSION}.exe"
InstallDir "$LOCALAPPDATA\Programs\${APP_NAME}"
InstallDirRegKey HKCU "${UNINST_KEY}" "InstallLocation"
RequestExecutionLevel user
BrandingText "${APP_NAME} ${APP_VERSION}"

!include "MUI2.nsh"
!include "FileFunc.nsh"

!define MUI_ICON   "../winres/icon.ico"
!define MUI_UNICON "../winres/icon.ico"
!define MUI_WELCOMEFINISHPAGE_BITMAP "wizard.bmp"
!define MUI_ABORTWARNING
!define MUI_ABORTWARNING_TEXT "설치를 그만둘까요?"

; ---- 설치 화면 ----

!define MUI_WELCOMEPAGE_TITLE "${APP_NAME} 설치"
!define MUI_WELCOMEPAGE_TEXT "쓸모없는 파일이 얼마나, 어디에 있는지 보여주는 프로그램이에요.$\r$\n$\r$\n디스크를 읽기만 하고 파일을 지우지 않아요.$\r$\n$\r$\n[다음]을 누르면 설치를 시작해요."
!insertmacro MUI_PAGE_WELCOME

!define MUI_PAGE_HEADER_SUBTEXT "설치할 폴더를 확인해 주세요."
!define MUI_DIRECTORYPAGE_TEXT_TOP "${APP_NAME}을 설치할 폴더예요. 관리자 권한 없이 지금 사용하는 계정에만 설치돼요.$\r$\n$\r$\n다른 폴더에 설치하려면 [찾아보기]를 누르세요."
!insertmacro MUI_PAGE_DIRECTORY

!define MUI_PAGE_HEADER_TEXT "설치하는 중"
!define MUI_PAGE_HEADER_SUBTEXT "잠시만 기다려 주세요."
!define MUI_INSTFILESPAGE_ABORTHEADER_TEXT "설치를 끝내지 못했어요"
!define MUI_INSTFILESPAGE_ABORTHEADER_SUBTEXT "설치가 중간에 멈췄어요. 설치 파일을 다시 실행해 주세요."
!insertmacro MUI_PAGE_INSTFILES
; NSIS 3.04의 MUI2는 위 두 값을 페이지가 끝나도 지우지 않아서 직접 지운다 (새 버전에서는 이미 지워져 있음)
!ifdef MUI_INSTFILESPAGE_ABORTHEADER_TEXT
  !undef MUI_INSTFILESPAGE_ABORTHEADER_TEXT
!endif
!ifdef MUI_INSTFILESPAGE_ABORTHEADER_SUBTEXT
  !undef MUI_INSTFILESPAGE_ABORTHEADER_SUBTEXT
!endif

!define MUI_FINISHPAGE_TITLE "설치가 끝났어요"
!define MUI_FINISHPAGE_TEXT "${APP_NAME}을 설치했어요. 시작 메뉴에서 언제든 실행할 수 있어요.$\r$\n$\r$\n지우고 싶을 때는 Windows 설정 > 앱에서 제거하면 돼요."
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "지금 ${APP_NAME} 실행하기"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchApp
!define MUI_FINISHPAGE_SHOWREADME
!define MUI_FINISHPAGE_SHOWREADME_TEXT "바탕 화면에 바로 가기 만들기"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION CreateDesktopShortcut
!insertmacro MUI_PAGE_FINISH

; ---- 제거 화면 ----

!define MUI_PAGE_HEADER_SUBTEXT "지우기 전에 확인해 주세요."
!define MUI_UNCONFIRMPAGE_TEXT_TOP "${APP_NAME}을 이 컴퓨터에서 지워요. 프로그램이 만든 기록과 캐시 파일도 함께 지워져요."
!insertmacro MUI_UNPAGE_CONFIRM

!define MUI_PAGE_HEADER_TEXT "지우는 중"
!define MUI_PAGE_HEADER_SUBTEXT "잠시만 기다려 주세요."
!define MUI_INSTFILESPAGE_FINISHHEADER_TEXT "제거가 끝났어요"
!define MUI_INSTFILESPAGE_FINISHHEADER_SUBTEXT "${APP_NAME}을 이 컴퓨터에서 지웠어요."
!define MUI_INSTFILESPAGE_ABORTHEADER_TEXT "제거를 끝내지 못했어요"
!define MUI_INSTFILESPAGE_ABORTHEADER_SUBTEXT "제거가 중간에 멈췄어요. 다시 시도해 주세요."
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "Korean"
SetFont /LANG=${LANG_KOREAN} "Malgun Gothic" 9 ; 기본값 굴림 대신 Windows 기본 한글 글꼴(맑은 고딕)

!pragma warning disable 9100 ; 버전 정보에 저작권(LegalCopyright) 문구는 넣지 않음
VIProductVersion "${APP_VERSION}.0"
VIAddVersionKey /LANG=${LANG_KOREAN} "ProductName" "${APP_NAME}"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileDescription" "${APP_NAME} 설치 프로그램"
VIAddVersionKey /LANG=${LANG_KOREAN} "FileVersion" "${APP_VERSION}"
VIAddVersionKey /LANG=${LANG_KOREAN} "ProductVersion" "${APP_VERSION}"

; 실행 중인 exe는 덮어쓰거나 지울 수 없으니, 사용자가 닫을 때까지 기다린다.
; (실행 중인 exe는 쓰기용으로 열리지 않는다는 점으로 확인)
!macro DEFINE_ENSURE_APP_CLOSED un
Function ${un}EnsureAppClosed
  retry:
  IfFileExists "$INSTDIR\${APP_EXE}" 0 done
  ClearErrors
  FileOpen $0 "$INSTDIR\${APP_EXE}" a
  IfErrors 0 closed
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "${APP_NAME}이 실행 중이에요.$\r$\n프로그램을 닫은 뒤 [다시 시도]를 눌러 주세요." /SD IDCANCEL IDRETRY retry
    Abort
  closed:
  FileClose $0
  done:
FunctionEnd
!macroend
!insertmacro DEFINE_ENSURE_APP_CLOSED ""
!insertmacro DEFINE_ENSURE_APP_CLOSED "un."

Section "Install"
  SetOutPath "$INSTDIR"
  Call EnsureAppClosed
  File "../build/${APP_EXE}"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  CreateShortcut "$SMPROGRAMS\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"

  ; Windows 설정 > 앱 목록에 등록
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${APP_EXE},0"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" $0
SectionEnd

Function LaunchApp
  Exec '"$INSTDIR\${APP_EXE}"'
FunctionEnd

Function CreateDesktopShortcut
  CreateShortcut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}"
FunctionEnd

Section "Uninstall"
  Call un.EnsureAppClosed
  Delete "$SMPROGRAMS\${APP_NAME}.lnk"
  Delete "$DESKTOP\${APP_NAME}.lnk"
  Delete "$INSTDIR\${APP_EXE}"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR" ; 비어 있을 때만 지운다 (사용자가 넣어 둔 파일은 건드리지 않음)
  RMDir /r "${DATA_DIR}"
  DeleteRegKey HKCU "${UNINST_KEY}"
SectionEnd
