# TUI SSH Client — v3 구현 계획 (SFTP 심화)

## Context (왜 만드는가)
v2로 분할 뷰와 드래그 전송이 붙었다. 하지만 v2는 의도적으로 **파일 한 개씩, 한 번에 한 건,
진행률 없이** 옮기도록 잘라냈고, 그 경계가 코드에 그대로 주석으로 남아 있다:

- `internal/sftp/transfer.go`의 `ErrIsDir` — "Recursive copies are v3"
- `internal/ui/app.go`의 `runTransfer` — "progress and cancellation are v3"

실제로 써 보면 걸리는 것도 정확히 이 셋이다:

- **디렉터리를 못 옮긴다.** 프로젝트 폴더 하나 올리려면 앱 밖에서 `scp -r`를 쳐야 한다.
- **큰 파일을 보내면 앱이 멈춘 것처럼 보인다.** 상태줄에 `transferring…` 한 줄뿐이고,
  얼마나 남았는지도, 중간에 그만둘 방법도 없다. 실패하면 목적지에 잘린 파일이 남는다.
- **여러 파일을 하나씩** 확인 패널을 거쳐 보내야 한다.
- 원격 파일을 지우거나 이름을 바꾸려면 결국 터미널 세션으로 넘어가야 한다.

v3은 이 넷을 닫는다. **새 화면 모드를 만들지 않고** v2의 분할 뷰 위에 얹는 것이 원칙이다 —
전송은 여전히 `buildTransfer` 하나로 수렴하고, 확인은 여전히 공용 `confirm` 패널을 쓴다.

## 범위
| 포함 | 제외 (→ 이후 버전) |
|---|---|
| 디렉터리 재귀 전송 (업/다운 양방향) | 다중 세션 탭 · 세션 유지/재연결 (v4) |
| 틱 기반 진행률 + `ctrl+c` 취소 + 부분 파일 정리 | 사이드바 검색/필터 · 그룹/폴더 (v5) |
| 다중 선택 전송 | 전송 큐 / 병렬 전송 / 재개(resume) (v4 이후) |
| 원격·로컬 파일 삭제 · 이름변경 | 권한 변경(chmod) UI, 새 디렉터리 생성 (v5) |

## 확정된 결정 (사용자 합의 — 임의로 뒤집지 말 것)
- **진행률은 틱 기반**이다. 전송 goroutine이 atomic 카운터만 갱신하고, UI는
  `tea.Tick(100ms)`으로 그 값을 읽는다. goroutine에서 model을 만지지 않는다는 v0 규약을
  지키면서 초당 수천 번의 메시지를 만들지 않는 유일한 방법이다.
- **취소는 `context`**로 청크 루프를 끊고 **부분 파일을 지운다.** 잘린 파일을 남기지 않는다.
- **재귀 전송은 롤백하지 않는다.** 중간에 실패하면 그때까지 옮긴 것은 그대로 두고 멈춘다 —
  절반 지워진 원격 디렉터리가 절반 복사된 디렉터리보다 위험하다.
- 전송은 **한 번에 하나**다. 큐/병렬은 v3에도 넣지 않는다.

---

## 배경 — v2 코드에서 반드시 재사용할 것
- `internal/ui/sftp.go`의 `filePane`(1항목=1행), `rowToIndex`, `paneBodyTop`. 진행률 바가
  들어가도 **패널 높이 예산(`rightHeaderRows`, `panelHeight()`)은 건드리지 않는다** —
  `TestLayoutAlignment`가 잡는다.
- `buildTransfer(from, entry)`가 드래그 경로와 키보드 경로가 만나는 **단일 funnel**이다.
  v3에서 항목이 여러 개가 되어도 이 성질을 유지한다: 두 경로가 같은 `transferReq`를 만든다.
- 확인 UI는 `internal/ui/confirm.go`의 `confirm` + `transferConfirm()`. 영역 교체 방식이
  레이아웃 불변식을 지키는 유일한 방법이라는 v1 결정은 그대로다 — 오버레이를 만들지 말 것.
- `internal/sftp/browser.go`의 `Browser` 인터페이스가 Local/Remote를 대칭으로 다루는 지점이다.
  새 연산(`Remove`/`Rename`)도 여기에 추가해 UI가 양쪽을 구분하지 않게 한다.
- `sftpGen` 세대 카운터. 늦게 도착한 메시지를 버리는 규약은 진행률 틱·전송 완료에도 그대로
  적용한다.
- `internal/sftp/remote_test.go`의 in-process SFTP 서버 하네스. 이 시스템에는 sshd가 없다.

## 의존성
새 외부 의존성 없음. `context`, `sync/atomic`, `time`만 추가로 쓴다.
진행률 바도 `bubbles/progress`를 쓰지 않는다 — 폭 계산이 레이아웃 불변식에 직접 걸려 있어서
외부 위젯의 폭 동작을 신뢰할 수 없다. `padLine` 기반으로 직접 그린다.

---

## 구현

### 1. 전송 엔진 재작성 — `internal/sftp/transfer.go`

```go
// Progress is written by the transfer goroutine and read by the UI on a tick.
// Nothing else crosses that boundary — no channel, no callback into the model.
type Progress struct {
    done  atomic.Int64
    total atomic.Int64
    name  atomic.Value // string: the file currently moving
}

func (p *Progress) Done() int64   { ... }
func (p *Progress) Total() int64  { ... }
func (p *Progress) Name() string  { ... }

func Upload(ctx context.Context, r *Remote, localPath, remotePath string, p *Progress) (int64, error)
func Download(ctx context.Context, r *Remote, remotePath, localPath string, p *Progress) (int64, error)
```

- `io.Copy`를 **32KiB 청크 루프**로 바꾼다. 양방향이 같은 헬퍼를 쓴다:
  ```go
  func copyCtx(ctx context.Context, dst io.Writer, src io.Reader, p *Progress) (int64, error)
  ```
  매 청크마다 `ctx.Err()`를 확인하고 `p.done.Add(n)`. `p`가 nil이어도 동작한다(테스트 편의).
- **취소·실패 시 목적지 파일을 지운다** (`r.sc.Remove` / `os.Remove`). v2가 "resuming and
  cleaning up partial files is v3's problem"이라고 미뤄둔 부분이다. 이미 있던 파일을
  덮어쓰다 실패한 경우도 지운다 — 잘린 내용으로 남기는 것보다 없는 게 낫다(확인 패널이
  덮어쓰기를 이미 경고했다).
- 새 센티널을 `transfer.go`에 추가한다:
  ```go
  var ErrCancelled = errors.New("transfer cancelled")
  ```
  `ctx.Err()`가 `context.Canceled`면 `ErrCancelled`로 감싼다. UI는 `errors.Is`로만 갈라진다 —
  **문자열 매칭 금지**(v1 규약).
- `ErrIsDir`는 남기되 의미가 바뀐다: 이제 "재귀가 아닌 단일 파일 API에 디렉터리를 넘겼다"는
  **내부 오류**다. 사용자에게 보이는 거부 메시지는 아니다(재귀가 생겼으므로).

### 2. 재귀 전송 — `internal/sftp/tree.go` (신규)

```go
// TransferItem is one leaf of a recursive copy, relative to the root the user
// picked. Directories are listed too: they have to exist before their files.
type TransferItem struct {
    RelPath string // slash-separated, relative to the source root
    Size    int64
    IsDir   bool
}

// Plan walks src (a file or a directory) and returns everything to copy plus
// the byte total. The total is what makes the progress bar a percentage, so it
// has to be known before the first byte moves.
func Plan(br Browser, root string) (items []TransferItem, total int64, skipped int, err error)
```

- `Plan`은 `Browser.List`만 쓴다 — Local/Remote 양쪽에서 같은 코드가 돈다.
- **심링크는 따라가지 않고 건너뛴다.** 순환(`a/b -> a`)이 무한 재귀가 되기 때문이다.
  건너뛴 개수는 `skipped`로 돌려 완료 메시지에 `skipped 2`로 표시한다.
- 디렉터리 항목은 `IsDir: true`로 목록 **앞쪽**에 온다(부모가 자식보다 먼저).
- 실행:
  ```go
  func RunSet(ctx context.Context, r *Remote, req Set, p *Progress) (Result, error)

  type Set struct {
      Upload  bool
      SrcRoot string        // 소스 쪽 디렉터리
      DstRoot string        // 목적지 쪽 디렉터리
      Items   []TransferItem
  }
  type Result struct{ Files int; Bytes int64; Skipped int }
  ```
  디렉터리 항목은 `MkdirAll` 상당(`sc.MkdirAll` / `os.MkdirAll`), 파일 항목은 §1의
  `Upload`/`Download`를 그대로 호출한다. 경로 조립은 소스 쪽은 `Browser.Join`,
  목적지 쪽은 반대편 `Browser.Join` — 로컬 `\`와 원격 `/`가 섞이지 않게 한다.
- 실패하면 **거기서 멈추고 에러를 돌려준다.** 이미 옮긴 것은 지우지 않는다(확정 결정).

### 3. 다중 선택 — `internal/ui/sftp.go`

`filePane`에 선택 상태를 추가한다:
```go
marked map[string]bool // entry name → selected; 디렉터리를 옮기면 초기화
```
- `setEntries`가 `marked`를 비운다(다른 디렉터리의 선택이 살아 있으면 안 된다).
- 렌더: 선택 행 앞에 `●` 마커 + `styles.go`의 새 `styleRowMarked`. 커서 행 스타일이
  우선한다(둘 다면 커서 스타일 + 마커).
- **전송 대상 결정은 한 함수로 모은다**:
  ```go
  // targets returns what a transfer from this pane would move: the marked
  // entries if there are any, otherwise just the row under the cursor.
  func (p *filePane) targets() []model.FileEntry
  ```
  드래그도 이걸 쓴다 — 선택된 행을 잡고 끌면 **선택 전체가 따라간다**. 선택되지 않은 행을
  잡으면 그 행 하나만 간다(선택은 유지).

`buildTransfer`는 시그니처만 넓히고 역할은 그대로다:
```go
type transferReq struct {
    upload    bool
    entries   []model.FileEntry // 1개일 수도, N개일 수도
    srcDir    string
    dstDir    string
    overwrite []string          // 목적지에 이미 있는 이름들
    plan      *sftppkg.Set      // Plan 결과 (디렉터리 포함 시 채워진다)
    total     int64
    skipped   int
}
```
디렉터리가 섞여 있으면 총량을 알기 위해 `Plan`이 필요하고 그건 **네트워크·파일 IO**다.
따라서 흐름이 한 단계 늘어난다:

```
targets() ──▶ planTransfer cmd (tea.Cmd) ──▶ plannedMsg ──▶ pending 설정 ──▶ 확인 패널
```
`Update`에서 절대 블로킹하지 않는다는 v0 규약을 지키기 위한 것이다. 훑는 동안 상태줄에
`scanning…`을 띄운다. 파일만 선택된 경우에도 같은 경로를 타되 `Plan`은 stat 한 번으로 끝난다.

확인 패널(`transferConfirm()`)은 항목 수에 따라 문구만 갈라진다:
```
  Upload 4 items

  3 files, 1 directory  ·  12.4 MB
  from  /home/laoh/projects/ssh-client
  to    deploy@10.0.0.1:/srv/app
  ⚠ deploy.sh, main.go already exist — they will be overwritten
  ⚠ 2 symlinks will be skipped

  [enter] transfer   [esc] cancel
```
파일 하나면 v2와 똑같은 문구를 유지한다(회귀 없음).

### 4. 진행률 UI — `internal/ui/app.go`

`busy bool`을 상태 구조체로 대체한다:
```go
type transferState struct {
    prog    *sftppkg.Progress
    cancel  context.CancelFunc
    label   string      // "app.tar.gz" 또는 "4 items"
    upload  bool
    started time.Time
}
```
`App.transfer *transferState` — non-nil이 곧 "전송 중"이다.

- 새 메시지 `progressTickMsg{gen}`. `runTransfer`를 시작할 때 `tea.Tick(progressInterval)`을
  함께 배치하고, 틱을 받을 때마다 **전송 중일 때만** 다시 스케줄한다(아이들에 틱을 돌리지
  않는다). `progressInterval = 100 * time.Millisecond`.
- 틱 핸들러는 아무 상태도 바꾸지 않는다. `View`가 매 프레임 `prog`를 읽으므로 틱은
  **다시 그리게 만드는 것**이 전부다.
- 상태줄 렌더 (`statusLine`):
  ```
  ↑ app.tar.gz  ▓▓▓▓▓▓▓▓░░░░░░░░  42%  4.1/9.8 MB  2.3 MB/s  · ctrl+c cancel
  ```
  바 폭은 남는 폭에서 계산하고 `padLine`으로 정확히 맞춘다. 속도는
  `done / time.Since(started)`. 총량을 모를 때(스트리밍 stat 실패)는 퍼센트 없이
  바이트만 표시한다.
- **전송 중 키**: `ctrl+c`는 앱 종료가 아니라 **전송 취소**다(`transfer != nil`일 때만).
  취소하면 `cancel()`을 부르고 상태줄이 `cancelling…`으로 바뀐다 — 실제 종료는
  `transferDoneMsg{err: ErrCancelled}`가 올 때다. `esc`는 무시한다(실수로 누르기 쉽다).
- 종료 경로(`q`)와 `teardownSFTP()`는 `transfer.cancel()`을 먼저 부른다. 앱이 먼저 죽어도
  goroutine이 남지 않는다.
- `transferDoneMsg`에 `Result`를 실어 완료 메시지를 만든다:
  `sent 4 items (12.4 MB) · skipped 2`. `errors.Is(err, ErrCancelled)`면 에러가 아니라
  `transfer cancelled` 상태 메시지로 처리한다.

### 5. 파일 관리 — 삭제 · 이름변경

`Browser` 인터페이스를 두 줄 넓힌다 (Local/Remote 양쪽 구현):
```go
Remove(path string, recursive bool) error
Rename(oldPath, newPath string) error
```
- Local은 `os.Remove` / `os.RemoveAll` / `os.Rename`. Remote는 `sc.Remove` /
  디렉터리는 **직접 후위 순회 삭제**(pkg/sftp에는 `RemoveAll`이 없다) / `sc.Rename`.
- 삭제는 공용 `confirm`을 쓴다. 디렉터리가 섞이면 `Plan`으로 개수를 세어 문구에 넣는다:
  `Delete 3 items (1 directory, 128 files)?` — 재귀 삭제를 한 키로 넘기지 않는다.
- 이름변경은 **새 모드를 만들지 않는다.** 패널 본문을 한 줄 `textinput`으로 대체한다
  (confirm과 같은 영역 교체). `App.rename *renameState{side, from, input}`이 non-nil이면
  `handleKey` 맨 앞에서 키를 가로챈다 — `confirm`/`pending`과 같은 자리, 같은 규칙이다.
- 둘 다 완료 후 `refreshPanes()`로 목록을 갱신한다(이미 있는 함수).

### 6. 최종 키맵 (v2에서 겹치던 것 정리)

| 키 | 동작 |
|---|---|
| `up`/`down`/`k`/`j`/`pgup`/`pgdown`/`home`/`end` | 커서 이동 |
| `tab` / `left` / `right` / `h` / `l` | 패널 전환 |
| `enter` | 디렉터리면 진입, 파일이면 전송 |
| `space` | **선택 토글** (v2의 "즉시 전송"에서 바뀜) |
| `t` | 전송 — 선택이 있으면 선택 전체, 없으면 커서 항목 (디렉터리 포함) |
| `a` | 선택 전체 해제 |
| `d` | 삭제 (선택 우선, 확인 패널) |
| `R` | 이름변경 |
| `r` | 현재 디렉터리 새로고침 |
| `backspace` | 상위 디렉터리 |
| `ctrl+c` | 전송 중이면 취소 |
| `ctrl+b` / `esc` | 사이드바로 포커스 복귀 (SFTP 연결 유지) |

> `space`의 의미 변경은 의도적이다. 다중 선택이 생긴 이상 "고르는 키"가 하나 필요하고,
> 전송은 `enter`/`t`/드래그 셋으로 충분하다.

### 7. 상태 전이 (v2에 추가되는 부분)
```
sftp ──(t / enter / 드래그 드롭)──▶ scanning ──(Plan 완료)──▶ confirm
confirm ──(enter)──▶ transferring ──(완료/실패/취소)──▶ sftp + 상태줄 결과
transferring ──(ctrl+c)──▶ cancelling ──▶ sftp + "transfer cancelled"
sftp ──(d)──▶ confirm(delete) ──(enter)──▶ sftp + 목록 갱신
sftp ──(R)──▶ rename(한 줄 입력) ──(enter)──▶ sftp + 목록 갱신
```
`transfer`·`pending`·`rename`은 `confirm`과 같은 축에 있다: non-nil이면 우측 본문을 대체하고
`handleKey` 맨 앞에서 키를 먹는다. 뒤의 패널로 새면 안 된다.

---

## 변경 / 추가 파일

| 파일 | 내용 |
|---|---|
| `internal/sftp/transfer.go` | `context` + `Progress` 시그니처, 청크 루프, 부분 파일 삭제, `ErrCancelled` |
| `internal/sftp/tree.go` | **신규** `TransferItem` / `Plan` / `Set` / `RunSet` |
| `internal/sftp/browser.go` | `Browser`에 `Remove` / `Rename` 추가, `Local` 구현 |
| `internal/sftp/remote.go` | `Remove`(재귀 포함) / `Rename` 구현 |
| `internal/ui/sftp.go` | `marked`/`targets()`, `transferReq` 확장, 확인 문구 요약, 진행률 바 렌더 |
| `internal/ui/app.go` | `transferState`, `progressTickMsg`, `plannedMsg`, `renameState`, `ctrl+c` 취소, `statusLine` |
| `internal/ui/styles.go` | `styleRowMarked`, 진행률 바 채움/빈칸 스타일 |
| `docs/V0_plan.md` | 로드맵 재배치 (v3 축소, v4~v7로 확장) |
| `CLAUDE.md` | v3 참조 추가 — 구현 시 SFTP 키맵·전송 규약 반영 |

## 검증 (end-to-end)

**자동 테스트** (`go test ./internal/...`)
1. `internal/sftp/transfer_test.go` — 진행률 카운터가 단조 증가하고 최종값이 파일 크기와
   같다. 중간에 `cancel()`하면 `errors.Is(err, ErrCancelled)`이고 **목적지 파일이 남지
   않는다**(로컬·원격 양쪽).
2. `internal/sftp/tree_test.go` — 중첩 디렉터리를 `t.TempDir()`에 만들고 `Plan`의 파일 수·총
   바이트를 검증. 자기 부모를 가리키는 심링크를 넣어 **무한 재귀하지 않고 `skipped`로
   세는지** 확인. 디렉터리 항목이 자식보다 앞에 오는지.
3. `internal/sftp/remote_test.go` 확장 — in-process SFTP 서버로 디렉터리 재귀 업로드/다운로드
   라운드트립(내용·경로 일치), `Remove(dir, recursive)`, `Rename`.
4. `internal/ui/smoke_test.go` 확장 —
   - `TestMarkedTransferIncludesAllSelected`: `space`로 두 개를 고르고 `t`를 누르면
     `pending.entries`가 둘 다 담긴다. 선택된 행을 드래그해도 결과가 **동일**하다.
   - `TestTransferProgressKeepsLayout`: `transfer`가 진행 중인 상태에서 `View()`의 모든 행이
     정확히 width (`TestLayoutAlignment`와 같은 검사).
   - `TestCtrlCCancelsTransferNotApp`: 전송 중 `ctrl+c`가 `tea.Quit`을 돌려주지 않고
     `cancel`을 호출한다.
   - `TestRenameSwallowsKeys`: 이름변경 입력 중 다른 키가 패널로 새지 않는다.
5. `go vet ./...`, `go build ./...`, `go test -race ./internal/...` 통과.

**수동 확인 (v3 수용 기준 — 자동화하지 않음)**
1. 100MB 이상 파일 업로드 → 진행률 바·퍼센트·속도가 움직이고 UI가 계속 반응한다
   (사이드바 커서 이동이 먹는다).
2. 전송 중 `ctrl+c` → 앱이 죽지 않고 전송만 멈춘다. 원격에 부분 파일이 남지 않는다.
3. 디렉터리를 드래그 → 재귀 전송되고, 원격에서 `find`로 구조가 같은지 확인.
4. `space`로 여러 파일을 고르고 `t` → 한 번의 확인으로 전부 전송, 완료 메시지에 개수·총량.
5. 원격 파일 `R`로 이름변경, `d`로 삭제(디렉터리는 개수가 확인 문구에 뜬다) → 목록 갱신.
6. 전송 중 터미널 리사이즈 → 3-패널과 진행률 바가 어긋나지 않는다.

## v3에서 하지 않는 것 (의도적 제외)
- 전송 큐 / 병렬 전송 / 재개(resume) — 한 번에 하나라는 결정을 유지한다.
- 다중 세션 탭, 세션 유지·자동 재연결 → **v4**
- 사이드바 검색/필터, 그룹/폴더, `~/.ssh/config` import → **v5**
- 비밀번호 키체인, ssh-agent, 점프호스트 → **v6**
- 권한 변경(chmod) UI, 새 디렉터리 생성, 포트포워딩 → **v5 / v7**
