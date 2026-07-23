# TUI SSH Client — v7 구현 계획 (키맵 단일화 · 도움말 · 사용자 키 설정)

## Context (왜 만드는가)
v0의 키는 여섯 개(`enter`·`n`·`e`·`d`·`f`·`q`)였다. v7 직전인 지금 세어 보면 **40개가 넘는다** —
사이드바 15개(`/`·`i`·`s`·`Y`·`S`·`P`·…), SFTP 10개(`space`·`a`·`t`·`d`·`R`·`r`·`ctrl+c`·…),
탭 5개(`alt+1..9`·`alt+←/→`·`alt+w`·`r`), 거기에 폼·import·동기화·잠금 화면·키 패스프레이즈까지
각자의 키맵이 있다. 문제는 개수가 아니라 **어디에도 그 목록이 없다는 것**이다:

1. **사용자는 상태줄 한 줄로만 배운다.** `statusLine()`이 컨텍스트마다 손으로 쓴 문자열을
   돌려주는데, 폭이 좁으면 lipgloss가 그냥 잘라 버리고 `q quit`이 사라진다. 상태줄에 없는 키
   (`n`·`e`·`s`·`Y`·`S`·`P`·`a`·`alt+w`)는 **CLAUDE.md를 읽어야 알 수 있다.**
2. **키가 코드에 흩어져 있다.** `handleKey`/`handleSFTPKey`/`handleImportKey`/`handleSyncKey`/
   `handleUnlockKey`/`tabKey`의 `switch msg.String()` 리터럴이 전부다. 상태줄 문구는 그 switch와
   **아무 연결이 없어서** 한쪽만 고치면 조용히 거짓말이 된다.
3. **바꿀 수가 없다.** `d`가 삭제인 게 무섭다거나 `alt`를 터미널 멀티플렉서가 먹는 환경이면
   지금은 방법이 없다.

v7은 이 셋을 **하나의 원인**으로 본다 — 키 바인딩에 단일 출처가 없다. 레지스트리를 하나 만들고,
라우팅·상태줄·도움말·사용자 설정이 **전부 거기서 나오게** 한다. 도움말은 그 레지스트리를 그리는
뷰일 뿐이므로 정의상 최신이다.

로드맵의 "포트포워딩 / 점프호스트(ProxyJump)"는 **v8로 옮긴다.** 둘 다 `ssh.Dial` 경로를
건드리는 연결 계층 작업이고, 이 버전은 UI 한 축(키·도움말)만 다룬다 — v3에서 한 버전에 두 축을
넣지 않기로 한 것과 같은 이유다.

## 범위
| 포함 | 제외 (→ 이후 버전) |
|---|---|
| 키맵 레지스트리 — 라우팅·상태줄·도움말·설정의 단일 출처 | 키 시퀀스(`g g`, tmux식 프리픽스 체인) |
| `?` 도움말 오버레이 (컨텍스트별 섹션 · 검색 · 스크롤) | 마우스로 클릭하는 메뉴바 / 커맨드 팔레트 (v8) |
| 폭에 맞춰 재조립되는 상태줄 힌트 | 테마·색 설정 (v8) |
| `keys.json` 사용자 재바인딩 + 검증·경고 | GUI 키 캡처("키를 눌러 지정") |
| `--keys` 플래그로 현재 키맵을 stdout에 덤프 | 포트포워딩 / 점프호스트 → **v8** |

## 확정된 결정 (임의로 뒤집지 말 것)

- **키 문자열 리터럴은 레지스트리 안에만 있는다.** 핸들러의 `switch`는 `msg.String()`이 아니라
  **액션 ID**로 갈라진다(`switch a.keys.Action(ctxSidebar, msg.String())`). 기존 switch의 구조와
  분기 내용은 그대로 두고 case 라벨만 바꾸는 기계적 변경이다 — 이 버전에서 **동작이 바뀌는 키는
  하나도 없다.** 기본 바인딩은 지금 코드에 있는 것과 문자 하나까지 같다.
- **도움말은 레지스트리를 그리는 뷰다.** 도움말용 문자열 테이블을 따로 만들지 말 것 — 그게 지금
  상태줄이 하는 일이고, 정확히 그래서 낡는다. 설명문(`Desc`)은 바인딩에 붙어 있고,
  `TestHelpMatchesRealBindings`가 **도움말에 뜬 키를 실제로 눌러 액션이 도는지** 확인한다.
- **세션 포커스에는 도움말 키가 없다.** v0부터의 규약("세션에서는 탈출키만 가로채고 나머지는
  전부 SSH stdin")을 깨지 않는다. `?`는 vim·bash가 쓰는 평범한 문자다. 세션에서 도움말을 보려면
  `ctrl+b`로 나온 뒤 `?`이고, 상태줄이 그렇게 안내한다. `f1`도 예외로 만들지 않는다 —
  풀스크린 앱이 쓰는 키다.
- **도움말은 모달 규약을 따른다.** `App.help != nil`이면 `handleKey` 맨 앞에서 모든 키를 먹고,
  마우스도 막히며, 스크롤·검색 외의 키는 **카드를 닫기만 한다**(뒤로 새지 않는다).
  `confirm`/`pending`/`rename`/`sftpErr`/`unlock`이 떠 있으면 도움말은 **열리지 않는다** —
  모달 위에 모달을 쌓지 않는다.
- **도움말은 `overlay`로 띄운다.** SFTP 모드에서만 쓰던 `overlay`의 **첫 범용 사용**이다.
  ANSI 폭 계산 규칙(CLAUDE.md의 `overlay` 절)을 그대로 지킨다 — 새 합성 함수를 만들지 말 것.
  프레임에 안 들어가는 행은 그리지 않는다는 규칙도 같다.
- **상태줄은 이제 조립된다.** 손으로 쓴 문장 대신 컨텍스트의 바인딩을 `Priority` 순으로 이어
  붙이고, **폭이 모자라면 뒤에서부터 버린다.** lipgloss가 잘라서 문장이 반토막 나는 일이
  없어진다(`TestStatusHintNeverOverflows`).
- **도움말 셀은 상태줄의 다른 어떤 것에도 밀려나지 않는다.** 지금 상태줄은 `switch` 하나라
  전송·드래그·경고·에러·`status`가 뜨면 힌트가 통째로 사라진다 — 노란 경고가 뜬 화면에서
  단축키 안내가 없어지는 게 이것 때문이다. v7부터 상태줄은 `[메시지 … 도움말 셀]` 두 구역이고,
  자리가 모자라면 **메시지를 자른다**(도움말 셀을 버리지 않는다). 사용자가 뭘 눌러야 할지
  모르는 순간은 대개 뭔가 잘못됐다고 노란 글이 떠 있는 순간이다
  (`TestHelpCellSurvivesWarnings`).
- **셀은 열 수 없는 도움말을 광고하지 않는다.** 세션 포커스에서는 `ctrl+b ? help`로,
  모달이 떠 있을 때는 **아무것도** 뜨지 않는다(그 카드가 자기 답키를 이미 보여준다).
  화면에 보이는 키는 지금 누르면 반드시 동작해야 한다.
- **`keys.json`은 사용자가 손으로 쓰는 파일이다.** `ui.json`(접힘·정렬 = 지워도 되는 UI 찌꺼기,
  깨지면 조용히 zero value)과 **섞지 않는다.** 여기서는 반대로, 깨졌거나 모르는 액션 ID가 있으면
  **조용히 넘어가지 않고** 상태줄에 경고하고 그 항목만 기본값으로 되돌린다. 사용자가 의도해서
  쓴 파일이 조용히 무시되면 "왜 안 먹지"가 된다.
- **충돌은 거부한다.** 같은 컨텍스트에서 한 키가 두 액션에 붙으면 둘 다 기본값으로 되돌리고
  경고한다. 먼저 쓴 쪽이 이기는 규칙을 만들면 파일 순서에 의미가 생긴다.
- **탈출키는 사라질 수 없다.** `session.escape`(기본 `ctrl+b`)를 빈 배열로 두면 기본값을
  복구한다 — 그게 없으면 세션에서 나올 방법이 없고, 그 상태를 만들 수 있게 두면 앱이 잠긴다.
  세션 컨텍스트에 바인딩할 수 있는 액션은 **이것 하나뿐**이다.
- **키 시퀀스는 없다.** 바인딩은 `tea.KeyMsg.String()` 하나에 대응하는 단일 키다. 프리픽스
  체인을 넣으면 "다음 키를 기다리는 상태"가 생기고, 그건 모든 모달 규약과 세션 stdin 경로에
  타이머를 끼워 넣는 일이다.
- **`?`가 기본 도움말 키지만 그것도 재바인딩 대상이다.** 다만 어느 컨텍스트에서든 도움말
  액션이 하나도 없으면 기본값을 복구한다(탈출키와 같은 이유).

---

## 배경 — 기존 코드에서 반드시 재사용할 것
- `internal/ui/app.go:overlay` + `padLine` — 도움말 카드 합성. **유일한 ANSI 절단 지점**이라는
  성질은 그대로다.
- `sftpModal`이 카드를 화면 정중앙에 놓는 방식 — 도움말도 같은 배치 계산을 쓴다.
- `sidebar.go:containsFilter` — 도움말 안 검색(`/`)도 **부분 문자열 하나**로만 매칭한다.
  퍼지 랭킹을 다시 들이지 말 것(v5 결정).
- `config.Store`의 원자적 쓰기(`tmp` → `os.Rename`, 0600/0700). `keys.json`은 우리가 쓸 일이
  거의 없지만(`--keys`는 stdout으로 덤프한다) 쓰게 되면 이 경로다.
- `internal/config/uistate.go`의 "없으면 zero value" 로더 — `keys.json`은 **로드 실패를
  보고한다는 점만** 다르다. 파싱 골격은 같다.
- 모달 규약(`confirm`/`pending`/`rename`/`unlock`). 도움말은 그중 가장 가벼운 형태다.

## 의존성
**새 의존성 없음.** `bubbles/key`도 도입하지 않는다 — `key.Binding`은 도움말 문자열을 들고
있지만 컨텍스트 개념이 없고, 우리는 이미 `switch`로 라우팅한다. 레지스트리 100줄이 어댑터보다
작다.

---

## 구현

### 1. 레지스트리 — `internal/ui/keymap.go` (신규)

```go
// Context is where a key means something. Contexts are mutually exclusive at
// dispatch time: exactly one is active, decided by focus/rightMode/modal state.
type Context string

const (
    ctxSidebar Context = "sidebar" // + Connect·서버 목록·그룹 헤더
    ctxSession Context = "session" // 탈출키 하나뿐 — 나머지는 SSH stdin
    ctxTabs    Context = "tabs"    // alt+… : 세션이 있는 동안 어디서나
    ctxSFTP    Context = "sftp"
    ctxForm    Context = "form"
    ctxImport  Context = "import"
    ctxSync    Context = "sync"
    ctxConfirm Context = "confirm" // confirm/pending/rename/sftpErr/errorcard
    ctxUnlock  Context = "unlock"
    ctxHelp    Context = "help"
)

// Action is what a key does. The ID is the stable name users write in keys.json
// and the label tests assert on; it never changes once shipped.
type Action string

const (
    actConnect     Action = "sidebar.connect"
    actNewSession  Action = "sidebar.new_session"
    actDelete      Action = "sidebar.delete"
    actHelp        Action = "global.help"
    actEscape      Action = "session.escape"
    // …
)

// Binding is one row of the help card and one case of a switch.
type Binding struct {
    Action   Action
    Ctx      Context
    Keys     []string // tea.KeyMsg.String() 그대로. 첫 항목이 화면에 뜨는 대표 키.
    Desc     string   // 도움말 한 줄. 명령형 현재형("delete the server").
    Short    string   // 상태줄용 짧은 말("delete"). 비면 Desc의 첫 낱말.
    Priority int      // 상태줄에서 살아남는 순서. 0이면 상태줄에 안 뜬다.
    Hidden   bool     // 도움말에도 안 뜬다(alt+1..9처럼 묶어서 설명하는 것들)
}

type Keymap struct { /* ctx → key → Action, ctx → []Binding (선언 순서 유지) */ }

func DefaultKeymap() *Keymap

// Action resolves a pressed key. Empty Action means "not bound here" — the
// caller falls through exactly as its switch's default does today.
func (k *Keymap) Action(ctx Context, key string) Action

// Bindings returns the context's rows in declaration order (help card order).
func (k *Keymap) Bindings(ctx Context) []Binding
```

기본 바인딩 표는 **지금 코드에서 그대로 옮긴다.** 옮기면서 키를 바꾸거나 추가하지 않는다 —
이 버전의 회귀 위험은 전부 여기에 있고, `TestDefaultsMatchV6`가 컨텍스트별 (키 → 액션) 표를
통째로 고정한다.

### 2. 라우팅 교체 — 기존 핸들러들

```go
// before
switch msg.String() {
case "n":
    return a.openTab(srv, true)
case "e":
    a.editServer(srv)

// after
switch a.keys.Action(ctxSidebar, msg.String()) {
case actNewSession:
    return a.openTab(srv, true)
case actEditServer:
    a.editServer(srv)
```
- 바뀌는 파일: `app.go`(`handleKey`·`handleImportKey`), `sftp.go`(`handleSFTPKey`),
  `sync.go`, `unlock.go`, `tabs.go`(`tabKey`).
- **모달 우선순위는 건드리지 않는다.** `unlock` → `confirm`/`pending`/`rename`/`sftpErr` →
  필터 입력 → `importing` 순으로 키를 먹는 기존 순서가 그대로다. 레지스트리는 "이 컨텍스트에서
  이 키가 무엇인가"만 답하고, **어느 컨텍스트인지는 여전히 핸들러의 구조가 정한다.**
- 텍스트 입력(폼·필터·이름변경·패스프레이즈)은 레지스트리를 거의 안 쓴다 — `esc`/`enter`/`tab`
  정도만 액션이고 나머지 문자는 전부 입력으로 간다. 이 경계를 흐리지 말 것.

### 3. 도움말 카드 — `internal/ui/help.go` (신규)

```go
type helpState struct {
    ctx    Context         // 열린 시점의 컨텍스트 — 이 섹션이 맨 위에 온다
    filter textinput.Model // '/'로 켠다
    off    int             // 스크롤 오프셋
}
```
- 여는 키: `?`(레지스트리의 `global.help`). **세션 포커스와 모달 상태에서는 열리지 않는다**(§결정).
- 닫기: `esc`·`q`·`?`·아무 키. 마우스 클릭도 닫는다.
- 레이아웃: 화면 폭의 최대 80칸(작으면 `width-4`), 높이는 `panelHeight()-2`를 넘지 않는다.
  **2열**로 배치하되 폭이 60칸 미만이면 1열로 떨어진다.
  행 형식은 사이드바와 같은 규칙 — `키` 좌측 고정폭 + 흐린 설명, **폭이 모자라면 설명부터
  버린다**(스타일 입히기 전에 폭 계산을 끝낸다).
- 섹션 순서: **열린 컨텍스트가 맨 위**(제목에 `— current`), 그 아래 나머지가 고정 순서로
  이어진다. 사용자가 지금 있는 화면의 키를 스크롤 없이 본다는 것이 이 카드의 목적이다.
- `/`로 검색하면 **전 컨텍스트를 평평하게** 훑는다(v5 필터와 같은 성질: 부분 문자열, 원래 순서).
  검색 대상은 `키 + Desc + Action ID`. 결과 행에는 컨텍스트 배지가 붙는다.
- 스크롤: `↑/↓`·`pgup/pgdn`·휠. 내용이 카드보다 짧으면 스크롤 키도 카드를 닫는다(그게 "아무 키").
- 맨 아래 한 줄: `keys.json: ~/.config/ssh-client/keys.json · ssh-client --keys 로 현재 키맵 덤프`.

### 4. 상태줄 조립 — `app.go:statusLine`

지금 `statusLine()`은 **한 줄에 하나만** 돌려주는 `switch`다. 그래서 전송 중·드래그 중이거나
경고(`⚠ kept the local host key …`)·에러·`status` 문자열이 떠 있으면 힌트가 **통째로 사라진다**.
노란 글이 뜬 화면에서 단축키 안내가 없어지는 게 정확히 이 구조 때문이고, 도움말을 만들어도
같은 이유로 `? help`가 안 보이게 된다 — **가장 도움이 필요한 순간에 도움말 키가 사라진다.**

그래서 상태줄을 **두 구역으로 나눈다**:

```go
// statusLine composes the bar as [message … helpCell]. The message keeps the
// existing priority switch; the help cell is pinned to the right edge and is
// never dropped, because the moment a warning or a transfer owns the line is
// exactly the moment the user wants to know what else they can press.
func (a *App) statusLine() string        // = statusMessage() + gap + helpCell()

func (a *App) statusMessage() string     // 기존 switch 그대로 (전송/드래그/경고/에러/…)
func (a *App) helpCell() string          // "? help" / "ctrl+b ? help" / "" 
```
- **왼쪽 = 메시지.** 기존 우선순위 분기(transfer / drag / scanning / warning / err / 끊긴 탭 /
  status / 컨텍스트 힌트)를 **그대로** 옮긴다. 컨텍스트 힌트만 `hintFor`가 만든다:

  ```go
  // hintFor renders the context's bindings, dropping the low-priority tail
  // until it fits the space the help cell left over.
  func (a *App) hintFor(ctx Context, width int) string
  ```
- **오른쪽 = 도움말 셀.** 폭이 모자라면 **메시지를 잘라서**(꼬리에 `…`) 자리를 만든다.
  도움말 셀은 마지막까지 남는다. 둘 다 못 넣을 만큼 좁으면(`width < len(helpCell)+8`) 그때만
  도움말 셀을 버린다 — 메시지가 한 글자도 안 남는 화면은 더 나쁘다.
- 셀의 내용은 **도움말을 지금 열 수 있는지**를 그대로 반영한다. 없는 키를 광고하지 않는다:
  | 상태 | 셀 |
  |---|---|
  | 도움말을 열 수 있음 (사이드바·SFTP·폼·import·sync·empty) | `? help` |
  | 세션 포커스 (`?`는 셸로 간다) | `ctrl+b ? help` |
  | 모달이 떠 있음 (`confirm`/`pending`/`rename`/`sftpErr`/`unlock`) | (없음 — 그 카드가 자기 답키를 이미 보여준다) |
  | 도움말 카드가 열려 있음 | `esc close · / search` |
- 색은 **메시지와 독립**이다. 경고가 노랑이어도 도움말 셀은 `styleHint`(흐린 회색)로 그린다 —
  같은 색이면 경고의 일부로 읽힌다.
- 합성은 `padLine`으로 정확히 `a.width`에 맞춘다(`View`가 이미 `padLine(a.statusLine(), a.width)`을
  부르지만, 잘라내기를 하려면 이 함수가 폭을 알아야 한다). ANSI가 섞이므로 폭 계산은
  `ansi` 계열로 하고, **스타일을 입히기 전에** 끝낸다 — `overlay`·사이드바 행과 같은 규칙이다.
- 구분자·표기(`·`, `alt+←/→`)와 기존 문구는 그대로 둔다. 폭이 넉넉할 때 왼쪽 구역의 출력은
  v6과 문자열이 같아야 한다(`TestWideStatusLineUnchanged`) — 바뀌는 것은 오른쪽에 셀이 하나
  붙는 것뿐이다.

### 5. 사용자 키맵 — `internal/config/keymap.go` (신규)

```go
// LoadKeys reads keys.json. Unlike ui.json, a broken file is reported: the user
// wrote it on purpose, and silently ignoring it is indistinguishable from a bug.
func (s *Store) LoadKeys() (map[string][]string, error)
func (s *Store) KeysPath() string
```
파일 형식은 **액션 ID → 키 배열**의 평평한 맵 하나다(컨텍스트는 ID 접두사에 이미 있다):
```json
{
  "sidebar.delete":      ["ctrl+d"],
  "sidebar.new_session": ["n", "N"],
  "tabs.close":          []
}
```
- 빈 배열은 **해제**다(그 액션에 키가 없어진다). 도움말에는 `—`로 뜬다.
- `ui`가 `Apply(user map[string][]string) []KeymapProblem`으로 덮어쓴다. 문제는 셋뿐:
  `unknown action` / `duplicate key in context` / `escape unbound`. 문제가 있는 항목만
  기본값으로 되돌리고 나머지는 적용한다. 시작 시 상태줄:
  `keys.json: 2 problems — see ? · using defaults for those`, 상세는 도움말 카드 맨 아래.
- **`ui` → `config` 방향은 그대로다.** `config`는 키맵의 *내용*을 모른다(문자열 맵만 읽는다),
  검증은 레지스트리를 가진 `ui`가 한다. 액션 ID 상수를 `config`로 내리지 말 것.

### 6. `--keys` 덤프 — `main.go`
```bash
ssh-client --keys            # 컨텍스트별 표를 stdout에 (TUI를 띄우지 않는다)
ssh-client --keys=json       # keys.json 형식 그대로 — 편집 시작점
```
TUI 안에서 파일을 편집시키지 않는다. 사용자가 자기 에디터로 쓰는 파일이다.

### 7. 상태 전이 (v6에 추가되는 부분)
```
any(단, 세션 포커스·모달 제외) ──(?)──▶ help ──(아무 키 / 클릭)──▶ 원래 상태 그대로
help ──(/)──▶ help[filtering] ──(esc)──▶ help
session ──(ctrl+b)──▶ sidebar ──(?)──▶ help          (세션에는 도움말 키가 없다)
start ──(keys.json 문제)──▶ 기존 시작 경로 + 경고 상태줄
```
도움말은 **어떤 상태도 바꾸지 않는다** — 닫으면 포커스·모드·스크롤 위치가 열기 전과 같다.

---

## 변경 / 추가 파일

| 파일 | 내용 |
|---|---|
| `internal/ui/keymap.go` | **신규** `Context`/`Action`/`Binding`/`Keymap`, `DefaultKeymap`, `Apply` |
| `internal/ui/help.go` | **신규** `helpState`, 카드 렌더(2열·검색·스크롤), `overlay` 사용 |
| `internal/ui/app.go` | `App.keys`/`App.help`, `handleKey`가 액션으로 분기, `statusLine`→`hintFor`, 도움말 마우스 차단 |
| `internal/ui/sftp.go` | `handleSFTPKey`가 액션으로 분기 (동작 변화 없음) |
| `internal/ui/tabs.go` | `tabKey`가 액션으로 분기, `alt+1..9`는 `Hidden` 한 줄로 설명 |
| `internal/ui/sync.go` / `unlock.go` / `importer.go` | 같은 기계적 교체 |
| `internal/config/keymap.go` | **신규** `KeysPath`/`LoadKeys` (깨지면 **에러를 돌려준다**) |
| `main.go` | `--keys` 플래그 |
| `docs/V0_plan.md` | 로드맵 갱신 — v7 = 키맵·도움말, 포트포워딩/점프호스트는 **v8** |
| `CLAUDE.md` | 구현 시 갱신 — 키맵 단일 출처 규약, 도움말 모달 규약, `overlay` 범용 사용, `keys.json` vs `ui.json` |

## 검증 (end-to-end)

**자동 테스트** (`go test ./internal/...`)
1. `internal/ui/keymap_test.go`
   - `TestDefaultsMatchV6`: 컨텍스트별 (키 → 액션) 표 전체를 리터럴로 고정한다. **이 버전에서
     키가 하나라도 바뀌면 여기서 깨진다.**
   - `TestEveryActionIsBound` / `TestNoDuplicateKeyInContext`: 선언된 액션에 기본 키가 있고,
     한 컨텍스트에 같은 키가 두 번 없다.
   - `TestUserRebindApplies`, `TestUnknownActionIsReported`, `TestDuplicateRebindKeepsDefaults`,
     `TestEscapeCannotBeUnbound`, `TestHelpCannotBeUnbound`.
2. `internal/ui/help_test.go`
   - `TestHelpMatchesRealBindings`: 도움말에 렌더된 **모든 키를 실제로 눌러** 해당 액션이
     도는지 확인한다(도움말이 거짓말하지 않는다는 유일한 보증).
   - `TestHelpOpensOnCurrentContextFirst`: SFTP 모드에서 열면 첫 섹션이 sftp다.
   - `TestHelpSwallowsKeys`: 열린 동안 `q`·`alt+2`·`d`·마우스가 **아무것도 하지 않고** 닫히기만
     한다. 뒤의 세션 stdin으로도 새지 않는다.
   - `TestNoHelpInSession`: 세션 포커스에서 `?`는 **stdin으로 간다**(카드가 안 뜬다).
   - `TestHelpDoesNotStackOnModals`: `confirm`/`pending`/`unlock`이 떠 있으면 안 열린다.
   - `TestHelpRestoresState`: 닫은 뒤 focus·rightMode·`scrollOff`·필터가 그대로다.
   - `TestHelpFilterIsSubstring`: `del`이 `sidebar.delete`와 `sftp.delete`를 둘 다 잡고
     순서는 선언 순서다.
3. `internal/ui/smoke_test.go` 확장
   - `TestLayoutAlignmentWithHelp`: 도움말이 뜬 상태에서 **모든 모드**(empty·terminal·sftp·
     form·import·sync)의 모든 행이 정확히 width이고 세로 예산이 그대로다.
   - `TestHelpFloatsOverThePanes`: SFTP 모드에서 `╭`가 여전히 3개 + 카드(overlay 규약 유지).
   - `TestHelpWithColour`: `SetColorProfile(TrueColor)`로 카드 오른쪽 패널이 색을 잃지 않는다.
   - `TestStatusHintNeverOverflows`: 폭 20~200을 훑어 상태줄이 절대 width를 넘지 않고
     **`? help`가 항상 남는다**.
   - `TestHelpCellSurvivesWarnings`: `a.warning`(노란 글)·`a.errMsg`·`a.status`·전송 중·
     드래그 중 **다섯 상태 모두**에서 상태줄에 `? help`가 들어 있고, 메시지 쪽이 잘렸더라도
     전체 폭은 정확히 width다.
   - `TestHelpCellInSessionSaysCtrlB` / `TestNoHelpCellOnModals`: 세션 포커스에서는
     `ctrl+b ? help`, `confirm`/`pending`/`unlock`에서는 셀이 **없다**.
   - `TestWideStatusLineUnchanged`: 폭이 넉넉하면 왼쪽 구역이 v6과 문자열이 같다.
4. `internal/config/keymap_test.go`
   - `TestLoadKeysMissingIsNotAnError`(파일 없음 → 빈 맵), `TestBrokenKeysJSONIsAnError`
     (`ui.json`과 반대 — 조용히 넘어가지 않는다).
5. `go vet ./...`, `go build ./...`, `go test -race ./internal/...` 통과.
   기존 테스트는 **한 줄도 수정하지 않고** 통과해야 한다(동작이 안 바뀌었다는 증거).

**수동 확인 (v7 수용 기준 — 자동화하지 않음)**
1. 사이드바에서 `?` → 카드가 뜨고 sidebar 섹션이 맨 위. 아무 키로 닫으면 선택 위치가 그대로다.
2. SFTP 모드에서 `?` → 세 패널이 서 있는 채로 카드가 뜨고 sftp 섹션이 맨 위. 닫으면 커서·선택
   상태가 유지된다.
3. 세션 안에서 `?`를 치면 **셸에 `?`가 입력된다.** `ctrl+b` 후 `?`는 카드를 연다.
4. 터미널 폭을 40칸까지 줄이며 상태줄을 본다 — 문장이 반토막 나지 않고 `? help`가 마지막까지
   남는다.
5. **노란 글이 뜬 상태에서 도움말 키가 보이는지** — 넷을 각각 확인한다:
   호스트키 충돌 경고(pull 후 `⚠ kept the local host key …`), 드래그 중(`↦ … drop on the
   other pane`), 100MB 전송 중 진행률, 연결 실패 후 빨간 `✗ …`. 넷 다 오른쪽 끝에 흐린
   `? help`가 있고, 그 자리에서 `?`를 누르면 실제로 카드가 뜬다.
6. `keys.json`에 `{"sidebar.delete":["ctrl+d"]}`를 넣고 재시작 → `d`는 아무 일도 안 하고
   `ctrl+d`가 삭제 확인을 띄운다. 도움말에도 `ctrl+d`로 뜬다.
7. `keys.json`에 없는 액션 ID와 중복 키를 일부러 넣고 재시작 → 경고 상태줄이 뜨고, 그 항목만
   기본값으로 돌아가며 나머지 재바인딩은 살아 있다.
8. `session.escape`를 `[]`로 두고 재시작 → `ctrl+b`가 그대로 동작한다(기본값 복구).
9. `ssh-client --keys=json > keys.json` → 그 파일을 그대로 두고 재시작해도 아무 경고가 없고
   키가 하나도 바뀌지 않는다.
10. 색이 있는 터미널(TrueColor)에서 vim을 띄운 탭 위에 카드를 열었다 닫는다 — 뒤 화면의 색과
   커서 위치가 복구된다.

---

## 구현 후 메모 (계획과 달라진 것)

전체 설계는 위 그대로 구현됐다. 코드를 쓰면서 달라진 것만 적는다.

- **컨텍스트가 둘 늘었다.** 계획의 `ctxConfirm`이 확인 패널과 에러 카드를 같이 담고 있었는데,
  답키가 전혀 다르다(`enter`/`y` vs `r`/`e`/`esc`). `ctxError`를 갈랐다. 한 줄 입력
  (rename·키 패스프레이즈)은 `ctxPrompt`로 **적어만** 둔다.
- **`Doc: true` 필드가 생겼다.** "여기 적혀 있지만 여기서 라우팅되지 않는" 바인딩이다 —
  연결 폼은 `msg.Type`을 **포커스된 필드와 함께** 보고(키 본문 textarea에서는 `enter`·`↑`가
  입력이다), 필터와 커서는 bubbles list의 것이며, 드래그는 키가 아니다. 이것들을 레지스트리
  라우팅으로 끌어오면 폼이 필드마다 다르게 동작하는 이유를 잃는다. 대신 도움말에는 뜨고,
  **재바인딩 대상에서는 빠진다**(`TestDocBindingsAreNotRebindable`).
- **전역 폴백을 만들지 않았다.** `a.keys.Action(ctx, key)`는 그 컨텍스트만 본다. 폴백이 있으면
  `q`가 파일 패널에서도 종료가 되는데, v6에서는 아무것도 아니었다 — 이 버전에서 동작이 바뀌는
  키는 없어야 하므로 `ctxGlobal`은 **필요한 자리에서 명시적으로** 조회한다.
- **`TestEveryActionIsDispatched`는 소스를 읽는다.** "선언됐지만 아무도 처리하지 않는 액션"을
  잡는 방법으로, 패키지의 `.go` 파일에서 그 액션 상수 이름을 찾는다. 조금 특이하지만 싸고
  정확하다 — 액션 상수는 dispatch 말고는 쓰이지 않기 때문이다.
- **상태줄의 다중 탭 문구는 v6과 같지 않다.** `alt+←/→ cycle`처럼 두 바인딩을 한 항목으로
  합친 표기를 버리고 실제 바인딩을 그대로 나열한다(`alt+→ next`). 대신 사이드바 키도 같이
  뜬다 — 조립된 줄이 v6의 손으로 쓴 줄보다 정보가 많다. 기본 화면과 SFTP 줄은 v6과 **문자까지
  같다**(`TestWideStatusLineUnchanged`).
- **`statusRoom()`이 생겼다.** 진행률 바가 `a.width`로 자기 폭을 정하고 있어서, 도움말 셀이
  붙자 바가 넘쳐 문장이 잘렸다. 상태줄에 맞춰 크기를 정하는 것은 전부 이 함수를 쓴다.
- **도움말은 넓을 때만 2열이다.** 계획대로 2열을 넣었지만 기준을 100칸으로 올렸다 —
  80칸에서 2열로 쪼개면 설명이 전부 `…`로 끝난다. **잘린 설명보다 스크롤이 낫다.**
  키 칸도 고정폭이 아니라 섹션별로 가장 긴 키에 맞춘다(대부분 6칸, 탭 섹션만 16칸).
- **스크롤 키는 카드를 닫지 않는다.** 계획에는 "내용이 짧으면 스크롤 키도 닫는다"고 썼는데,
  같은 키가 화면에 따라 닫기도 하고 안 닫기도 하는 것이 더 놀랍다. `↑/↓`·`pgup/pgdn`은 항상
  스크롤(내용이 짧으면 아무 일도 안 함)이고, 그 외 아무 키가 닫는다.
- **너무 작은 화면에서는 도움말이 아예 없다**(`helpFits`). 카드가 프레임보다 크면 `overlay`가
  그 행을 건너뛰어 반쪽짜리 상자가 남는데, 그러면 키보드는 먹히고 화면에는 아무것도 안 뜬다.
  그래서 그 크기에서는 `?`가 열리지 않고 **상태줄도 그 키를 광고하지 않는다** — 같은 규칙의
  적용이다(보이는 키는 반드시 동작한다).
- **`--keys`는 `IsBoolFlag`다.** `--keys`(표)와 `--keys=json`(파일 형식)을 둘 다 받기 위한
  것으로, 공백 형식(`--keys json`)은 지원하지 않는다.
- **`keys.json`은 `.gitignore`에 들어갔다.** 비밀이 아니라 **이 기기의 설정**이라서다 —
  `ui.json`과 같은 이유(잘못 커밋되는 것을 막는다), `vault.age`와는 다른 이유.

## v7에서 하지 않는 것 (의도적 제외)
- 포트포워딩(`-L`/`-R`), 점프호스트/ProxyJump, agent forwarding → **v8**
- 커맨드 팔레트(`ctrl+p`로 액션을 이름으로 실행) — 레지스트리가 생기면 자연스러운 다음
  단계지만, 도움말이 먼저 자리를 잡아야 액션 이름이 안정된다 → v8
- 키 시퀀스·프리픽스 체인, TUI 안에서의 키 편집 UI
- 테마·색 설정 (`styles.go`는 손대지 않는다) → v8
- 마우스 메뉴바·우클릭 메뉴
