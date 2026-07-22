# TUI SSH Client — v2 구현 계획 (SFTP 파일 브라우저)

> **구현 완료.** 계획과 달라진 곳만 먼저 적는다:
> - **2절(`Dial()` 추출)은 v1에서 이미 끝났다.** `ssh.Dial(srv, opts)`가 `Options`(known_hosts)를
>   받으므로 `sftp.Connect(srv, opts)`도 같은 시그니처를 따른다. 따라서 마지막 절의
>   "SFTP는 여전히 `InsecureIgnoreHostKey`"는 **틀렸다** — SFTP도 v1의 호스트키 검증을
>   그대로 통과하고, TOFU 프롬프트도 뜬다.
> - `sftpConnectedMsg`가 첫 목록까지 함께 실어 온다. `Home()`이 네트워크 왕복이라 `Update`에서
>   부를 수 없고, 연결 직후 빈 패널을 한 프레임 보여줄 이유도 없다.
> - **`Stat`으로 덮어쓰기를 확인하지 않는다.** 목적지 패널이 이미 들고 있는 목록을 본다 —
>   `Update`에서 원격 `Stat`을 부르면 UI가 블로킹된다. `Stat`/`StatLocal` 자체는 구현돼 있고
>   `remote_test.go`가 검증한다.
> - 연결 실패 카드는 `ssh.ErrSFTP` 센티널을 추가로 분류하고, `[r] retry`는
>   `lastWasSFTP`를 보고 터미널이 아니라 SFTP를 다시 연다.
> - 전송이 끝나면 양쪽 패널을 자동으로 다시 읽는다(수동 확인 2번의 "`r`로 새로고침"은 불필요).
> - **7절의 "확인 alert는 우측 영역 전체를 교체한다"는 사용자 요청으로 뒤집혔다.** 확인·에러
>   카드는 세 패널을 그대로 둔 채 화면 정중앙에 **떠서** 그려진다(`overlay`). 연결 실패도
>   `rightError`로 빠지지 않고 SFTP 모드에 남아 카드만 띄운다. 자세한 폭 계산 규칙은 CLAUDE.md의
>   "SFTP 모드의 다이얼로그만 예외적으로 떠 있는다" 절.

## Context (왜 만드는가)
v0에서 좌측 서버 리스트 + 우측 임베디드 PTY 세션까지 동작한다. 하지만 파일을 옮기려면
세션 안에서 `scp`를 직접 치거나 앱 밖으로 나가야 한다.

v2는 **SFTP 모드**를 추가한다. 사이드바에서 서버를 고른 뒤 단축키로 진입하면 우측 영역이
`Local | Remote` 두 파일 패널로 쪼개지고, 한쪽 파일을 **드래그해서 반대쪽에 떨어뜨리면
확인 alert**가 뜬다. 동의하면 SFTP로 실제 전송한다. 마우스가 없거나 마우스 리포팅이 안 되는
환경을 위해 동일한 확인 흐름을 타는 **키보드 전송 경로**도 함께 넣는다.

`docs/V0_plan.md`의 로드맵은 SFTP를 v4에 적어뒀지만 사용자 합의로 **v2로 앞당긴다**.

## 확정된 결정 (사용자 합의 — 임의로 뒤집지 말 것)
- **레이아웃**: 사이드바는 그대로 두고 **우측 영역만** Local | Remote로 분할 (3-패널)
- **진입**: 사이드바에서 서버에 커서를 두고 `f` → 그 서버로 SFTP 모드. `enter`는 지금처럼 터미널
- **범위**: **양방향 전송(업로드 + 다운로드), 파일만.** 디렉터리 드래그는 거부 메시지 —
  재귀 전송은 v3
- **키보드 경로**: 파일 위에서 `enter`(파일일 때) 또는 `space` → 드래그와 동일한 확인 다이얼로그
- **연결**: SFTP는 터미널 세션과 **독립적인 별도 TCP 연결**. 한쪽을 끊어도 다른 쪽은 산다

```
┌Servers─────┐┌Local──────────┐┌Remote─────────┐
│+ Connect   ││ ../           ││ ../           │
│prod-web    ││ docs/         ││ app/          │
│db-1        ││ main.go       ││ deploy.sh     │
└────────────┘└───────────────┘└───────────────┘
 tab 패널이동 · 드래그 전송 · ctrl+b 나가기
```

## 배경 — v0 코드에서 반드시 재사용할 것
- 레이아웃 상수는 전부 `internal/ui/app.go`에 있다: `topMargin` / `statusRows` /
  `borderSize` / `padX` / `sidePadX` / `rightHeaderRows`, 그리고 `panelHeight()` ·
  `rightInner()` · `clampBlock()`. 마우스 좌표 변환(`rowToIndex`, form 클릭)이 **같은
  상수를 재사용**한다. SFTP 패널도 이 상수들만 써야 하고, 새 상수를 만들면
  `TestLayoutAlignment`가 잡는다.
- `clampBlock(s, w, h)`는 테두리 적용 **전에** 블록을 정확한 사각형으로 자른다. 3-패널도
  각각 `clampBlock` → `panelStyle(focused).Render(...)` → `lipgloss.JoinHorizontal` 순서를
  지킨다.
- 상태 축은 `focus`(sidebar|form|session) + `rightMode`(empty|form|terminal) 두 개. SFTP는
  여기에 값을 **추가**하는 형태로 붙는다.
- 세션 생성/폐기는 `gen` 카운터로 뒤늦게 도착한 msg를 버린다(`app.go`의 `connectedMsg` ~
  `sessionEndedMsg` 처리). SFTP도 같은 패턴을 그대로 복제한다.
- `internal/ssh/session.go`의 `authMethods()`가 password / keyboard-interactive / key를 이미
  처리한다. SFTP가 이 로직을 **재구현하면 안 된다**.
- bubbletea v1.3.10은 `MouseActionMotion` + `MouseButtonLeft`(드래그 중 이동)와
  `MouseActionRelease`를 준다. `main.go`가 이미 `tea.WithMouseCellMotion()`으로 띄우므로
  **추가 옵션이 필요 없다**.
- `internal/ssh/session_test.go`에 in-process SSH 서버 하네스가 있다. 이 시스템엔 sshd가
  없으므로 SFTP 테스트도 이걸 본떠 `subsystem sftp` 요청을 받아 `sftp.NewServer(ch)`를 돌린다.

## 의존성
`github.com/pkg/sftp` (v1.13.11). 이미 `golang.org/x/crypto`에 의존하고 있어 추가 전이
의존은 사실상 없다.

---

## 구현

### 1. `internal/model/server.go` — 공유 자료구조 추가
```go
// FileEntry is one row in a file pane. Both the local filesystem and the
// remote SFTP listing are reduced to this.
type FileEntry struct {
    Name    string
    Size    int64
    Mode    fs.FileMode
    ModTime time.Time
    IsDir   bool
}
```
`model`은 여전히 프로젝트 내부를 아무것도 import 하지 않는다(표준 라이브러리만).
정렬 헬퍼 `SortEntries([]FileEntry)`(디렉터리 먼저, 그 다음 이름순)도 여기 둔다 — 로컬과
원격이 같은 순서로 보여야 하므로 정렬은 한 곳에서만 한다.

### 2. `internal/ssh/session.go` — 다이얼 경로 노출 (리팩터링)
`Connect()` 안의 `ClientConfig` 조립 + `xssh.Dial` 부분을 잘라내 exported 함수로 뽑는다:
```go
// Dial authenticates and returns a client. Session and SFTP both build on it.
func Dial(srv model.Server) (*xssh.Client, error)
```
`Connect()`는 `Dial()`을 호출하도록 고친다. **동작 변경 없음** — `authMethods`,
`InsecureIgnoreHostKey`, `DialTimeout` 그대로. 기존 `session_test.go`가 회귀를 잡는다.

### 3. `internal/sftp/` — 새 패키지 (파일 IO 전담)
의존 방향에 `sftp → ssh → model` 한 줄이 추가된다(여전히 단방향). `ui`는 여기만 부르고,
파일 IO나 net 연결을 직접 하지 않는다는 v0 규약은 그대로다.

**`internal/sftp/browser.go`**
```go
// Browser is one side of the split view. Local and Remote both implement it so
// the UI can treat the two panes symmetrically.
type Browser interface {
    List(dir string) ([]model.FileEntry, error)
    Home() (string, error)
    Join(dir, name string) string   // local: filepath.Join, remote: path.Join
    Parent(dir string) string
    Label() string                  // "Local" / "user@host"
}

type Local struct{}                 // os.ReadDir 기반
```
- `Local.List`는 `os.ReadDir` → `DirEntry.Info()`로 채운다. 정보를 못 읽는 항목은 **건너뛰지
  말고** 크기 0으로 넣되 에러는 삼킨다 — 퍼미션 없는 항목 하나가 목록 전체를 죽이면 안 된다.
- 숨김 파일은 기본 표시. `.` 항목은 넣지 않고 `..`는 UI가 그린다.

**`internal/sftp/remote.go`**
```go
type Remote struct { client *xssh.Client; sc *sftp.Client; label string }

func Connect(srv model.Server) (*Remote, error)   // ssh.Dial → sftp.NewClient
func (r *Remote) Close() error                    // sc.Close 후 client.Close
```
`List`는 `sc.ReadDir`, `Home`은 `sc.Getwd()`(실패 시 `"/"`).

**`internal/sftp/transfer.go`**
```go
// Upload copies a local file to the remote, preserving the source's mode.
func Upload(r *Remote, localPath, remotePath string) (int64, error)
// Download is the mirror image.
func Download(r *Remote, remotePath, localPath string) (int64, error)
// Stat reports whether the destination already exists (drives the overwrite warning).
func StatLocal(path string) (model.FileEntry, bool, error)
func (r *Remote) Stat(path string) (model.FileEntry, bool, error)
```
- 디렉터리를 넘기면 `errors.New("directories are not supported yet")`로 **즉시 거절**.
- 임시 파일 없이 바로 `Create` + `io.Copy`. 부분 파일 정리/재개는 v3에서 함께 다룬다.
- 전송 후 `Chmod`로 소스 모드를 반영한다.

### 4. `internal/ui/sftp.go` — 파일 패널 위젯 (신규)
`bubbles/list`를 **쓰지 않는다**. 기본 델리게이트가 항목당 3행을 먹어 파일 목록에 부적합하고,
드롭 좌표 매핑이 어려워진다. 대신 **항목당 정확히 1행**인 자체 패널을 만든다:
```go
type filePane struct {
    br      sftp.Browser
    dir     string
    entries []model.FileEntry   // [0]은 항상 ".." (루트 제외)
    cursor  int
    offset  int                 // 스크롤 오프셋
    rows    int                 // 본문 높이 (= panelHeight() - rightHeaderRows)
    err     string
}

func (p *filePane) View(cols int) string         // 타이틀바 + 1행/항목
func (p *filePane) rowToIndex(y int) (int, bool)  // 스크린 row → entries 인덱스
func (p *filePane) moveCursor(delta int)          // offset 클램프 포함
func (p *filePane) selected() (model.FileEntry, bool)
```
- 행 포맷: `📁 name/` 또는 `   name` 좌측 정렬 + 우측에 사람이 읽는 크기(`humanSize`).
  `terminal.go`의 **기존 `padLine`을 재사용**해 정확히 cols에 맞춘다.
- 커서 행은 `styles.go`의 기존 스타일 계열로 반전. 드래그 원본 행은 별도 색.
- `rowToIndex` 공식: `idx := y - (topMargin + 1 + rightHeaderRows) + p.offset` —
  **기존 레이아웃 상수만** 쓴다. 새 테스트가 이 공식을 렌더 결과와 대조해 고정한다.

### 5. `internal/ui/app.go` — 상태 확장
```go
const ( focusSidebar; focusForm; focusSession; focusLocal; focusRemote )   // 추가
const ( rightEmpty; rightForm; rightTerminal; rightSFTP )                  // 추가
```
`App`에 추가되는 필드:
```go
remote     *sftp.Remote
local      filePane
remotePane filePane
sftpGen    int          // 세션의 gen과 같은 역할 — 늦게 온 msg 폐기
sftpName   string       // 패널 타이틀
drag       *dragState   // 드래그 중일 때만 non-nil
pending    *transferReq // 확인 대기 중인 전송 (non-nil이면 확인 화면)
busy       bool         // 전송 진행 중 (중복 실행 차단)
```
```go
type dragState struct { from focusArea; entry model.FileEntry; over focusArea }
type transferReq struct {
    upload    bool
    entry     model.FileEntry
    srcPath   string
    dstPath   string
    overwrite bool     // 목적지가 이미 있음 → 경고 문구
}
```
새 메시지(기존 규약대로 `gen` 동봉):
`sftpConnectedMsg` / `sftpFailedMsg` / `listedMsg{gen, side, dir, entries, err}` /
`transferDoneMsg{gen, name, bytes, err}`.

새 커맨드(전부 `tea.Cmd` — `Update`에서 절대 블로킹하지 않는다):
`connectSFTP(srv, gen)`, `listDir(br, side, dir, gen)`, `runTransfer(remote, req, gen)`.

### 6. 키 / 마우스 라우팅
`handleKey`의 `focusSidebar` 분기에 `case "f":` 추가 → 선택된 서버로 `openSFTP()`
(`+ Connect` 항목이면 무시). `openSFTP`는 기존 `activateSelection`의 터미널 경로를 본떠
`sftpGen++` → `rightMode = rightSFTP` → 로컬 패널은 즉시 `Home()`으로 채우고 원격은
`connectSFTP` 커맨드를 반환한다.

`focusLocal` / `focusRemote` 분기 (새 함수 `handleSFTPKey`):

| 키 | 동작 |
|---|---|
| `up`/`down`/`pgup`/`pgdown`/`home`/`end` | 커서 이동 (`moveCursor`) |
| `tab` | Local ↔ Remote 패널 전환 |
| `enter` | 디렉터리면 진입(`listDir`), 파일이면 전송 확인 다이얼로그 |
| `space` | 항상 전송 확인 다이얼로그 (디렉터리면 거부 메시지) |
| `backspace` | 상위 디렉터리 |
| `r` | 현재 디렉터리 새로고침 |
| `ctrl+b` / `esc` | 사이드바로 포커스 복귀 (SFTP 연결은 유지) |

**확인 다이얼로그가 떠 있을 때(`pending != nil`)는 다른 키를 전부 가로챈다**:
`enter`/`y` → `runTransfer`, `esc`/`n` → `pending = nil`.

`handleMouse` 확장 — 지금은 press만 처리하고 나머지를 버리는데, 여기에 드래그 3단계를 넣는다:

1. **press** (`MouseActionPress` + `MouseButtonLeft`): 어느 패널인지 판별 → 포커스 이동 →
   `rowToIndex`로 항목 선택. 파일이면 `drag = &dragState{from: side, entry: e}`.
   `..`나 디렉터리는 드래그를 시작하지 않는다(커서 이동만).
2. **motion** (`MouseActionMotion` + `MouseButtonLeft`, `drag != nil`): 포인터가 있는 패널을
   `drag.over`에 기록 → 뷰가 그 패널 테두리를 강조하고 상태줄에 `↦ <파일명>`을 띄운다.
3. **release** (`MouseActionRelease`): `drag`가 있고 `drag.over`가 **반대편 패널**이면
   `buildTransfer`로 `pending`을 만든다. 같은 패널이거나 사이드바면 그냥 취소. `drag = nil`.
   > 릴리스 이벤트는 터미널에 따라 버튼을 `MouseButtonNone`으로 보고한다. **버튼 값으로
   > 거르지 말고** "드래그 중이었는가"로만 판단할 것.

패널 판별은 x 좌표 하나로 끝난다: `x < sidebarWidth` → 사이드바,
`x < sidebarWidth + localOuter` → Local, 그 외 → Remote.

키보드 경로와 드래그 경로는 **둘 다 `buildTransfer(from, entry)` 하나로 수렴**한다. 여기서
목적지 경로를 만들고(`Join(반대편 dir, entry.Name)`) 목적지 stat으로 `overwrite`를 채운다.
확인 화면과 전송 실행은 그 뒤로 완전히 공유된다.

### 7. `View()` — 3-패널 렌더
`rightMode == rightSFTP`일 때만 갈라지고, 나머지 모드는 지금 코드 그대로다.
```
right       := width - sidebarWidth   // 우측 영역 총 폭
localOuter  := right / 2              // 테두리 포함
remoteOuter := right - localOuter     // 나머지 — 반올림 오차를 여기서 흡수
localCols   := localOuter - borderSize
remoteCols  := remoteOuter - borderSize
```
각 패널은 기존 우측 패널과 동일하게 **자체 타이틀바(`rightHeaderRows`)** 를 갖는다:
`Local` + 현재 경로 / `user@host` + 현재 경로. 본문 높이는
`panelHeight() - rightHeaderRows`로 터미널 모드와 같다. 세 패널 모두
`clampBlock(..., panelHeight())` 후 `JoinHorizontal(lipgloss.Top, ...)`.

**확인 alert**: `pending != nil`이면 우측 **영역 전체**(Local + Remote 자리)를 테두리 하나짜리
확인 패널로 대체한다. lipgloss v1에는 안전한 오버레이 합성이 없고, ANSI가 섞인 행을
스플라이싱하면 폭 계산이 깨진다 — 영역 교체가 레이아웃 불변식을 지키는 유일한 방법이다.
사이드바는 그대로 있으므로 화면은 여전히 2-박스, `TestLayoutAlignment`와 같은 형태다.
```
  Transfer file

  main.go  (1.2 KB)
  from  /home/laoh/projects/ssh-client
  to    deploy@10.0.0.1:/srv/app
  ⚠ destination already exists — it will be overwritten

  [enter] transfer   [esc] cancel
```
상태줄(`statusLine`)에 SFTP 상태를 추가한다: 드래그 중이면 `↦ name`, 전송 중이면
`uploading name…`, 완료 시 `sent name (1.2 KB)`, 실패는 기존 `styleError` 경로.
전송 중 진행률 표시는 v3 — v2는 한 번에 한 건만 처리하고 `busy`로 중복 실행을 막는다.

### 8. `resize()` 갱신
`rightMode == rightSFTP`이면 두 패널의 `rows`(= `panelHeight() - rightHeaderRows`)를 갱신하고
`offset`을 클램프한다. **SFTP 모드는 원격 PTY가 없으므로 `WindowChange`를 보내지 않는다** —
터미널 세션이 따로 살아 있으면 기존 경로가 계속 처리한다(두 연결은 독립).

### 9. 정리 (teardown)
`teardownSFTP()`: `remote.Close()` → `remote = nil` → `sftpGen++`. `q` / `ctrl+c` 종료 경로에서
`teardownSession()`과 나란히 호출한다.

### 상태 전이 (v0 상태머신에 추가되는 부분)
```
empty|terminal ──(사이드바에서 f)──▶ sftp(connecting) ──(성공)──▶ sftp
                                        └──(실패)──▶ empty + 에러 메시지
sftp ──(드래그 드롭 / space / 파일에서 enter)──▶ confirm
confirm ──(enter)──▶ transferring ──▶ sftp + 상태줄 결과
confirm ──(esc)──▶ sftp
sftp ──(ctrl+b|esc)──▶ focus만 sidebar로 (SFTP 연결은 유지)
```

---

## 변경 / 추가 파일

| 파일 | 내용 |
|---|---|
| `internal/model/server.go` | `FileEntry`, `SortEntries` 추가 |
| `internal/ssh/session.go` | `Dial()` 추출 (동작 불변) |
| `internal/sftp/browser.go` | **신규** `Browser` 인터페이스 + `Local` |
| `internal/sftp/remote.go` | **신규** `Connect` / `Remote` |
| `internal/sftp/transfer.go` | **신규** `Upload` / `Download` / `Stat` |
| `internal/ui/sftp.go` | **신규** `filePane`, 드래그 상태, SFTP 키 처리, 확인 패널 렌더 |
| `internal/ui/app.go` | focus·rightMode 값 추가, 새 msg·cmd, `handleMouse` 드래그 3단계, `View`/`resize`/`statusLine` 분기 |
| `internal/ui/styles.go` | 커서행 / 드래그 원본 / 드롭 대상 강조 스타일 |
| `go.mod` | `github.com/pkg/sftp` |
| `docs/V0_plan.md` | 로드맵에서 SFTP를 v4 → v2로 이동 |
| `CLAUDE.md` | SFTP 모드 상태 전이·레이아웃·의존 방향(`sftp → ssh`) 반영 |

## 검증 (end-to-end)

**자동 테스트** (`go test ./internal/...`)
1. `internal/sftp/browser_test.go` — `Local.List`를 `t.TempDir()`에 만든 파일/디렉터리로
   검증(정렬 순서, `IsDir`, 크기).
2. `internal/sftp/remote_test.go` — `internal/ssh/session_test.go`의 in-process SSH 서버를
   본떠 `subsystem`=`sftp` 요청에 `sftp.NewServer(ch)`를 붙인다. 검증: `Connect` → `List` →
   `Upload` 라운드트립(원격 파일 내용 일치) → `Download` 라운드트립 → 디렉터리를 넘기면 에러.
3. `internal/ui/smoke_test.go` 확장:
   - `TestSFTPLayoutAlignment` — `rightMode = rightSFTP`에서 모든 행이 정확히 width이고
     `╭`이 **3개**, 패널 상/하단 행이 일치.
   - `TestFilePaneRowGeometry` — `TestSidebarRowGeometry`와 같은 방식으로 렌더된 화면에서
     파일명을 찾아 그 행을 `rowToIndex`에 넣어 인덱스가 맞는지 확인(스크롤 오프셋 포함).
   - `TestDragDropRequestsConfirmation` — press/motion/release 3개 `tea.MouseMsg`를 흘려
     `pending != nil`이 되고, `esc`로 취소되며, 같은 패널 안에서의 드롭은 아무 일도 없음.
   - `TestKeyboardTransferMatchesDrag` — 같은 파일에 대해 `space`로 만든 `pending`이
     드래그로 만든 것과 동일(경로·방향·overwrite 플래그).
4. `go vet ./...`, `go build ./...` 통과.

**수동 확인 (v2 수용 기준 — 자동화하지 않음)**
이 시스템에는 sshd가 없으므로 원격이 있는 환경에서 확인한다:
1. `go run .` → 서버 등록 → 사이드바에서 `f` → 우측이 Local | Remote로 갈라지고 양쪽 목록이 뜬다.
2. Local 파일을 Remote 패널로 드래그 → 확인 alert → `enter` → 상태줄에 완료, `r`로
   새로고침하면 원격 목록에 파일이 보인다.
3. 반대 방향(Remote → Local) 다운로드도 동일하게 동작.
4. 디렉터리를 드래그하면 거부 메시지가 뜨고 전송되지 않는다.
5. 이미 있는 이름으로 떨어뜨리면 덮어쓰기 경고가 확인 화면에 뜬다.
6. 터미널 리사이즈 시 3-패널이 어긋나지 않는다. `ctrl+b`로 사이드바 복귀 후 `enter`로 같은
   서버 터미널을 열어도 SFTP 연결이 살아 있다.

## v2에서 하지 않는 것 (의도적 제외)
- 디렉터리 재귀 전송, 다중 선택 전송 → v3 (`docs/V3_plan.md`)
- 전송 진행률 바 / 취소 → v3. 재개(resume)는 그 이후
- 원격 파일 삭제·이름변경 → v3. 권한 변경(chmod) UI는 v5
- ~~호스트키 검증은 여전히 `InsecureIgnoreHostKey`~~ — v1이 먼저 들어갔으므로 SFTP도
  같은 다이얼 경로를 타고 처음부터 검증된다.
