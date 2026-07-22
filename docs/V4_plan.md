# TUI SSH Client — v4 구현 계획 (다중 세션 · 세션 유지 · 자동 재연결)

## Context (왜 만드는가)
v3까지로 "한 서버에 붙어서 일하기"는 끝났다. 남은 것은 **여러 서버를 동시에 붙잡고 있기**다.
지금 코드는 세션을 **정확히 하나만** 가질 수 있게 되어 있다:

- `App.session`·`App.emu`·`App.gen`·`App.scrollOff`가 전부 단수 필드다
  (`internal/ui/app.go:65~78`). 다른 서버를 고르면 `teardownSession()`이 먼저 돌고
  `resetEmulator(ESC c)`로 화면을 지운 뒤 그 자리에 새 세션이 들어온다.
- 즉 **서버를 바꾸면 이전 세션이 죽는다.** 배포 로그를 띄워 둔 채 다른 장비를 볼 수 없다.
- 그리고 **끊기면 그걸로 끝이다.** 노트북 뚜껑을 닫았다 열거나 VPN이 잠깐 끊기면
  `sessionEndedMsg`가 와서 `rightEmpty`로 떨어지고, 사용자가 직접 다시 고르는 수밖에 없다.
  게다가 지금은 **끊긴 걸 즉시 알지도 못한다** — TCP가 죽어도 읽기 goroutine은
  다음 패킷을 기다리며 조용히 앉아 있는다(keepalive가 없다).

v4는 이 둘을 닫는다. **새 화면 모드를 만들지 않는다** — `rightTerminal`은 그대로고,
그 안에서 "지금 보이는 세션"이 여러 개 중 하나가 될 뿐이다.

## 범위
| 포함 | 제외 (→ 이후 버전) |
|---|---|
| 다중 세션 탭 (열기·전환·닫기, 최대 8) | 셸 상태 복원 (재연결은 **새 셸**이다) |
| 백그라운드 탭 유지 — 안 보여도 출력을 계속 받는다 | SFTP 탭 / 다중 SFTP 연결 (v2~v3 결정 유지) |
| keepalive 기반 끊김 감지 (`ErrConnectionLost`) | 세션 분할(split pane), 탭 재정렬·이름변경 (v7) |
| 지수 백오프 자동 재연결 + 수동 재시도 | 사이드바 검색/필터·그룹 (v5) |
| 탭별 스크롤백·리사이즈 | 비밀번호 키체인 · ssh-agent · 점프호스트 (v6) |

## 확정된 결정 (임의로 뒤집지 말 것)

- **에뮬레이터는 여전히 Close 하지 않는다.** CLAUDE.md의 `keyPump` 규약(`vt.Emulator.Close`가
  블록된 `Read`와 data race)은 v4에서도 그대로다. 대신 "프로세스에 하나"를 **"풀에 N개"**로
  넓힌다: 탭이 닫히면 슬롯을 **반납**하고, 재사용할 때 `resetEmulator`로 화면만 지운다.
  goroutine 수는 동시에 열린 적 있는 탭 수(최대 8)로 **묶여 있다** — 탭을 100번 열었다 닫아도
  goroutine은 8개를 넘지 않는다.
- **탭 최대 8개.** 위 슬롯 풀의 상한이자, 탭 스트립이 헤더 한 줄에 들어가는 상한이다.
  9번째는 만들지 않고 상태줄에 거절 메시지를 띄운다.
- **탭 스트립은 새 행을 쓰지 않는다.** 기존 `rightHeaderRows`(제목+빈줄)의 제목 줄을
  탭 스트립이 대체한다. 세로 예산(`topMargin`/`statusRows`/`panelHeight()`)은 **한 글자도**
  건드리지 않는다 — `TestLayoutAlignment`가 그대로 통과해야 한다.
- **탭 전환은 `alt` 조합**이다(`alt+1..9`, `alt+left/right`, `alt+w`). `ctrl+b`는 **지금 그대로**
  사이드바 탈출로 남긴다. tmux식 프리픽스(`ctrl+b` 다음 키)로 바꾸면 기존 근육 기억과
  `TestEscapeKeyLeavesSession`이 둘 다 깨지고, 세션 포커스에서 프리픽스 상태를 들고 있는
  동안 들어오는 키의 소유권이 애매해진다. Alt는 셸이 거의 쓰지 않아 가로채도 손해가 적다.
  (뒤집고 싶으면 `tabKey()` 한 함수만 고치면 된다.)
- **재연결은 새 셸이다.** 원격 프로세스는 이미 죽었다. 스크롤백을 이어 붙이거나 명령을
  재생하지 않는다 — 화면은 재연결 **성공 시점에** `ESC c`로 지운다. 그 전까지는 마지막 화면을
  **흐리게 그대로 둔다**(뭘 하던 중이었는지 읽을 수 있어야 한다). tmux 대체품이 아니라는 것을
  UI가 분명히 말해야 한다.
- **자동 재연결은 네트워크 사망에만.** 원격 셸이 정상 종료(`exit`, 종료코드 있음)하면 탭을
  닫는다. `ErrConnectionLost`(keepalive 실패 / 갑작스러운 EOF)일 때만 다시 붙는다.
  아니면 `exit` 한 번이 무한 재접속 루프가 된다.
- **호스트키 검증은 재연결에서도 그대로다.** 재연결도 `ssh.Connect`를 다시 부를 뿐이다.
  "재연결이니까 이미 신뢰한 걸로 치자"는 예외를 만들지 않는다 — 그 순간 MITM 창이 열린다.
  다만 자동 재연결 중에는 TOFU 프롬프트를 띄우지 않고 **실패로 처리한다**(사용자가 안 보는
  사이에 새 호스트키를 승인시키지 않는다). 사용자가 `r`로 다시 붙으면 정상 프롬프트가 뜬다.
- **SFTP는 여전히 하나다.** SFTP 연결은 터미널 세션과 별개라는 v2 결정이 여기서 이득이 된다 —
  탭이 늘어도 `App.remote` 쪽은 손대지 않는다. 탭마다 SFTP를 붙이는 것은 v4 범위 밖이다.

---

## 배경 — 기존 코드에서 반드시 재사용할 것
- `internal/ui/terminal.go`의 `keyPump`. **동작을 바꾸지 말 것.** 슬롯 하나가 지금의
  `emu`+`pump` 쌍 그대로다. 바뀌는 것은 "몇 개인가"뿐이다.
- `gen` 세대 카운터 규약. v4에서도 **단조 증가 카운터 하나**를 유지하고, 각 탭이 자기 `gen`을
  들고 있는다. 늦게 도착한 메시지는 "현재 탭"이 아니라 **`gen`으로 탭을 찾아** 라우팅하고,
  맞는 탭이 없으면 버린다.
- `waitForOutput(sess, gen)` 펌프 패턴. 탭마다 하나씩 돌고, `outputMsg.gen`이 그대로 목적지다.
- `internal/ssh/errors.go`의 센티널 + `errorAdvice`. 새 실패 모드도 **센티널로만** 분류한다.
- `internal/ssh/session_test.go`의 in-process SSH 서버. 재연결·keepalive 테스트도 여기 붙인다
  (이 시스템에는 sshd가 없다).
- `resize()`가 "패널 레이아웃 / vt / WindowChange 세 곳을 모두 갱신"하는 규약 — v4에서는
  **모든 탭에 대해** 그래야 한다.

## 의존성
새 외부 의존성 없음. `x/crypto/ssh`의 `client.SendRequest`(keepalive)와 `time`, `math` 정도.

---

## 구현

### 1. 에뮬레이터 슬롯 풀 — `internal/ui/terminal.go`

```go
// termSlot is one emulator plus the pump goroutine that drains its reply pipe.
// Neither is ever closed — see keyPump for why — so slots are recycled instead.
type termSlot struct {
    emu  *vt.Emulator
    pump *keyPump
}

// termPool hands out slots and takes them back. It never grows past maxTabs, so
// the number of pump goroutines is bounded by the number of tabs, not by how
// many sessions the user has opened over the life of the program.
type termPool struct {
    free []*termSlot
    live int
}

func (p *termPool) get(cols, rows int) (*termSlot, bool) // false: 상한 초과
func (p *termPool) put(s *termSlot)                      // detach + ESC c 후 free로
```
- `get`은 free에 있으면 꺼내 `resetEmulator(cols, rows)`, 없고 `live < maxTabs`면 새로 만들고
  `go slot.pump.run(slot.emu)`를 **한 번만** 띄운다.
- `put`은 `pump.detach()` → `resetEmulator` → free 반납. **Close 하지 않는다.**
- `maxTabs = 8`을 여기에 둔다(UI가 아니라 풀이 상한의 주인).

> 이 파일에서 바뀌는 건 이게 전부다. `renderEmulator`/`renderScrolled`/`keyToVT`는
> 이미 `*vt.Emulator`를 인자로 받으므로 **그대로 쓴다**.

### 2. 탭 — `internal/ui/tabs.go` (신규)

```go
type tabState int

const (
    tabConnecting tabState = iota
    tabLive
    tabLost        // 연결이 끊겼고 재연결 대기 중
    tabReconnecting
)

// sessionTab is one remote shell. Everything the old App held as a single
// field now lives here, one copy per tab.
type sessionTab struct {
    id       string // model.Server.ID
    name     string // 타이틀바·탭 스트립 라벨
    addr     string // user@host:port
    srv      model.Server

    gen     int          // 이 탭의 현재 세대 (재연결하면 올라간다)
    session *sshpkg.Session
    slot    *termSlot

    state     tabState
    scrollOff int
    activity  bool // 마지막으로 본 뒤 출력이 있었나 (탭 스트립의 •)

    // 재연결 상태. attempt는 백오프 계산에, until은 상태줄 카운트다운에 쓴다.
    attempt  int
    until    time.Time
    lastErr  error
}

func (t *sessionTab) emu() *vt.Emulator // slot.emu (nil-safe)
```

`App`에서는 단수 필드가 사라지고 이렇게 바뀐다:
```go
tabs   []*sessionTab
active int          // tabs 인덱스, -1이면 세션 없음
pool   termPool
gen    int          // 여전히 프로세스 전역 단조 카운터
```
`session`/`emu`/`pump`/`scrollOff`/`connectedID`/`sessionName`/`sessionAddr`는 전부
`a.cur()`(활성 탭, 없으면 nil)를 거쳐 접근한다. **`a.cur()`가 nil일 수 있다는 것이
v4에서 가장 흔한 버그 원인이다** — 렌더·키 라우팅 모두 nil 분기를 먼저 둔다.

메시지 라우팅은 한 함수로 모은다:
```go
// tabByGen finds the tab a message belongs to. A miss means the message
// outlived its session — drop it, exactly like the old gen check did.
func (a *App) tabByGen(gen int) (*sessionTab, bool)
```
`outputMsg`/`sessionEndedMsg`/`connectedMsg`/`connectFailedMsg` 처리부에서
`msg.gen != a.gen` 비교를 **전부 이걸로 바꾼다**. 배경 탭의 출력도 이 경로로 그 탭의
에뮬레이터에 그대로 들어간다 — 그게 "백그라운드 유지"의 전부다(따로 버퍼를 만들지 말 것).

### 3. 탭 열기 · 닫기 · 전환

- **열기**: 사이드바에서 서버를 고르면
  - 그 서버의 탭이 **이미 있으면 그 탭으로 전환**한다(다시 다이얼하지 않는다).
  - 없으면 새 탭을 만든다. 풀이 꽉 찼으면 상태줄에 `too many sessions (8) — close one first`.
  - 같은 서버에 **두 번째 세션**이 필요하면 사이드바에서 `n`(new session). 라벨은
    `web-1`, `web-1 (2)`처럼 뒤에 번호를 붙인다.
- **닫기**(`alt+w` 또는 원격 셸 정상 종료): `teardownTab(i)` — 세션 Close, `pool.put(slot)`,
  슬라이스에서 제거, `active` 보정(오른쪽 탭 → 없으면 왼쪽 → 없으면 `-1`+`rightEmpty`).
  기존 `teardownSession()`은 "활성 탭 닫기"의 얇은 껍데기로 남긴다.
- **전환**(`switchTo(i)`): `active`만 바꾸고 **`scrollOff`는 탭이 들고 있으므로 유지된다**.
  새 활성 탭의 `activity=false`. 세션에는 아무 것도 보내지 않는다(리사이즈는 §5).
- 키맵 (세션 포커스에서도 동작 — `handleKey` 앞쪽에서 `tabKey()`가 먼저 먹는다):

| 키 | 동작 |
|---|---|
| `alt+1` … `alt+8` | n번째 탭으로 |
| `alt+left` / `alt+right` (`alt+h`/`alt+l`) | 이전 / 다음 탭 |
| `alt+w` | 현재 탭 닫기 (연결 중이면 다이얼 취소) |
| `ctrl+b` | **변경 없음** — 사이드바로 포커스만 이동 |
| 사이드바 `n` | 선택한 서버로 **새** 세션 (이미 있어도 하나 더) |
| 사이드바 `enter` | 있으면 그 탭으로, 없으면 새 탭 |

`tabKey`는 `a.confirm`/`a.pending`/`a.rename`/`a.sftpErr`가 non-nil이면 **아무것도 하지
않는다** — 모달이 모든 키를 먹는다는 규약이 탭 키에도 그대로 적용된다.

### 4. 탭 스트립 렌더 — `rightHeader`

제목 줄을 탭이 하나일 때와 여럿일 때로 가른다(하나면 **v3와 픽셀 단위로 동일**해야 한다 —
회귀 없음).
```
 ▏web-1 ▕ db-2 • ▕ ⟳ cache-3 ▕                         deploy@10.0.0.1:22
```
- 활성 탭만 `styleTabActive`, 나머지는 `styleTabIdle`. `•`는 `activity`,
  `⟳`는 `tabLost`/`tabReconnecting`.
- 폭이 모자라면 **활성 탭을 중심으로 창을 잘라내고** 양 끝에 `‹`/`›`를 붙인다.
  탭 라벨은 `ansiTruncate`(이미 있다)로 자른다. 줄은 마지막에 반드시 `padLine(line, cols)`.
- 우측 detail(주소 / `SCROLL −n` / `connecting…`)은 지금 규칙 그대로, 탭 스트립 **뒤에**
  붙이되 폭이 부족하면 detail을 먼저 버린다.

### 5. 리사이즈 — 모든 탭

`resize()`는 지금 세 곳을 갱신한다. v4에서는 그 중 뒤 둘이 **탭 전체 루프**가 된다:
```go
for _, t := range a.tabs {
    resizeEmulator(t.emu(), cols, rows)   // 배경 탭도 지금 크기로
    if t.session != nil { _ = t.session.Resize(cols, rows) }
    t.scrollOff = 0                        // 리플로우된 과거 줄과 옛 offset은 안 맞는다
}
```
배경 탭을 빼먹으면 전환하는 순간 화면이 어긋난다(v0 규약이 말하는 "하나라도 빠지면"의
탭 버전이다).

### 6. 끊김 감지 — `internal/ssh/session.go`

```go
// KeepaliveInterval is how often we poke the server. Two missed replies (or one
// error) mean the connection is gone: without this a dead TCP connection just
// sits there and the read goroutine blocks forever.
const KeepaliveInterval = 30 * time.Second
```
- `Connect` 성공 시 keepalive goroutine을 띄운다: `client.SendRequest("keepalive@openssh.com",
  true, nil)`. 에러가 나면 `s.fail(ErrConnectionLost)` → `finish()`로 세션을 끝낸다.
  goroutine은 `s.closed`를 보고 빠져나온다(세션 하나당 goroutine 하나, 누수 없음).
- `internal/ssh/errors.go`에 센티널 추가:
  ```go
  var ErrConnectionLost = errors.New("connection lost")
  ```
  `ExitErr()`가 이 에러를 돌려주면 UI는 **정상 종료가 아니라 사망**으로 읽는다.
  정상 종료(`*xssh.ExitError` 또는 nil)와의 구분은 `errors.Is` 하나로 끝난다 — 문자열 매칭 금지.
- `errorAdvice`에 문구를 추가한다: `connection lost — reconnecting… [r] now  [alt+w] close tab`.

### 7. 자동 재연결 — `internal/ui/app.go`

`sessionEndedMsg` 처리가 두 갈래로 갈린다:
```go
switch {
case errors.Is(msg.err, sshpkg.ErrConnectionLost):
    t.state = tabLost
    t.attempt++
    d := backoff(t.attempt)            // 1s, 2s, 4s, 8s, 16s, 30s, 30s…
    t.until = time.Now().Add(d)
    return tea.Tick(d, func(time.Time) tea.Msg { return reconnectMsg{gen: t.gen} })
default:                                // 정상 종료
    a.teardownTab(i)
}
```
- `reconnectMsg`를 받으면 `t.state = tabReconnecting`, **새 `gen`을 발급**하고
  `connect(...)`를 그대로 다시 부른다. 성공(`connectedMsg`) 시 `resetEmulator` →
  `pump.attach` → `attempt = 0`. 실패하면 다시 `tabLost`로 떨어져 백오프가 이어진다.
- `backoff(n) = min(2^(n-1) * time.Second, 30*time.Second)`. **무한히 시도한다** —
  횟수 상한을 두면 자리를 비운 사이에 포기해 버린다. 대신 언제든 `alt+w`로 닫을 수 있다.
- 자동 재연결 중에는 `ssh.Options.Prompts`에 **nil을 넣지 않고**, TOFU 질문이 오면
  자동으로 거절한다(§확정된 결정). 그 경우 에러는 `ErrHostKeyUnknown`이므로 백오프를
  멈추고 `tabLost` + 에러 카드로 간다 — 조용히 재시도하면 안 되는 유일한 실패다.
- 화면: 끊긴 탭은 마지막 화면을 **그대로** 두고(구현에서 dim 처리는 뺐다 — vt 셀 그리드는
  자기 SGR을 들고 있어서 위에 흐리게 덧칠하려면 모든 셀의 스타일을 다시 쓰는 수밖에 없고,
  그건 렌더 경로에서 감당할 비용이 아니다. 대신 헤더 detail과 상태줄이 상태를 말한다), 헤더에 `⟳`,
  상태줄에 `reconnecting in 4s · [r] now · [alt+w] close`. **재연결 성공 전에는 화면을
  지우지 않는다.**
- `r`은 백오프를 무시하고 즉시 재시도한다(`t.until`을 지금으로 당긴다). 세션 포커스에서도
  `tabLost`/`tabReconnecting` 상태면 `r`을 가로챈다 — 그 상태에서는 stdin에 보낼 곳이 없다.
- **종료 경로**: `q`/`tea.Quit`과 앱 종료는 모든 탭을 `teardownTab` 한다. 백오프 타이머가
  살아 있어도 `tabByGen`이 못 찾으므로 메시지는 버려진다.

### 8. 상태 전이 (v3에 추가되는 부분)
```
empty ──(서버 선택)──▶ tab[connecting] ──(성공)──▶ tab[live]
                                        └──(실패)──▶ error 카드 (탭은 만들지 않는다)
tab[live] ──(alt+숫자/←→)──▶ 다른 탭 (이전 탭은 계속 출력을 받는다)
tab[live] ──(원격 정상 종료)──▶ 탭 닫힘 ──▶ 남은 탭 / empty
tab[live] ──(keepalive 실패)──▶ tab[lost] ──(백오프 만료 | r)──▶ tab[reconnecting]
tab[reconnecting] ──(성공)──▶ tab[live] (화면 초기화, 새 셸)
                  └──(실패)──▶ tab[lost] (백오프 증가)
any tab ──(alt+w)──▶ 탭 닫힘 (슬롯은 풀로 반납)
```

---

## 변경 / 추가 파일

| 파일 | 내용 |
|---|---|
| `internal/ui/tabs.go` | **신규** `sessionTab` / `tabState` / `tabByGen` / `switchTo` / `teardownTab` / `tabKey` / 탭 스트립 렌더 |
| `internal/ui/terminal.go` | `termSlot` / `termPool`(get·put), `maxTabs`. 그 외 **무변경** |
| `internal/ui/app.go` | 단수 세션 필드 → `tabs`/`active`/`pool`, 메시지 라우팅 `tabByGen`, `resize` 전 탭 루프, `reconnectMsg`/`backoff`, `rightHeader` 탭 스트립, 상태줄 재연결 카운트다운 |
| `internal/ui/sidebar.go` | 이미 열린 서버 표시(`●`, 2개 이상이면 `● name (2)`). `n`은 `app.go`의 사이드바 키 분기에 있다 |
| `internal/ui/styles.go` | `styleTabIdle` (활성 탭은 기존 `styleTitleBar` 재사용) |
| `internal/ssh/session.go` | keepalive goroutine, `fail(ErrConnectionLost)` |
| `internal/ssh/errors.go` | `ErrConnectionLost` 센티널 + 분류 |
| `internal/ui/errorcard.go` | `errorAdvice`에 연결 끊김 문구·액션 |
| `docs/V0_plan.md` | 로드맵 v4 항목에 이 문서 링크 |
| `CLAUDE.md` | 구현 시 갱신 — 탭 상태머신, 슬롯 풀(“에뮬레이터 하나” 규약의 확장), 재연결 규칙 |

## 검증 (end-to-end)

**자동 테스트** (`go test ./internal/...`)
1. `internal/ui/tabs_test.go`
   - `TestTabPoolRecyclesSlots`: 탭을 20번 열고 닫아도 `runtime.NumGoroutine()`이
     초기치 + maxTabs를 넘지 않는다(슬롯 반납 확인). 반납된 슬롯의 에뮬레이터가
     이전 내용을 갖고 있지 않다.
   - `TestNinthTabRefused`: 8개까지 열리고 9번째는 상태줄 메시지 + 탭 수 불변.
   - `TestBackgroundTabKeepsReceivingOutput`: 탭 A·B를 열고 A로 전환한 뒤 B의 `gen`으로
     `outputMsg`를 넣으면 **B의 에뮬레이터**에 반영되고 화면(A)은 그대로. `activity`가 켜진다.
   - `TestStaleGenDropped`: 닫힌 탭의 `gen`으로 온 `outputMsg`가 아무 에뮬레이터도 건드리지 않는다.
   - `TestAltKeysSwitchTabsAndCtrlBStillEscapes`: `alt+2`가 전환하고 `ctrl+b`는 v3처럼
     사이드바로 간다. 모달(`pending`/`rename`/`confirm`)이 떠 있으면 `alt+2`가 **먹히지 않는다**.
   - `TestResizeUpdatesEveryTab`: `WindowSizeMsg` 후 모든 탭의 에뮬레이터 크기가 같고
     `scrollOff`가 0이다.
2. `internal/ui/smoke_test.go` 확장 —
   - `TestLayoutAlignmentWithTabs`: 탭 8개 + 긴 이름에서도 모든 행이 정확히 width이고
     패널 높이가 v3과 **동일**하다(세로 예산 불변).
   - `TestSingleTabHeaderUnchanged`: 탭 1개일 때 헤더 문자열이 v3 형식 그대로.
   - `TestReconnectingTabRendersDimmedScreen`: `tabLost`에서 마지막 화면이 남아 있고
     상태줄에 카운트다운이 뜬다.
3. `internal/ssh/session_test.go` 확장 (in-process 서버) —
   - `TestKeepaliveDetectsDeadConnection`: 서버 쪽 TCP를 끊으면 `KeepaliveInterval` 안에
     `ExitErr()`가 `ErrConnectionLost`가 된다(테스트에서는 간격을 주입해 짧게).
   - `TestCleanExitIsNotConnectionLost`: 원격이 정상 종료하면 `errors.Is(err,
     ErrConnectionLost)`가 false.
4. `internal/ui`에서 `TestBackoffSchedule`: 1,2,4,8,16,30,30초. `TestCleanExitClosesTab`,
   `TestLostSessionSchedulesReconnect`, `TestManualRetrySkipsBackoff`.
5. `go vet ./...`, `go build ./...`, `go test -race ./internal/...` 통과.
   **`-race`는 v4에서 특히 중요하다** — 펌프 goroutine이 8개로 늘어난다.

**수동 확인 (v4 수용 기준 — 자동화하지 않음)**
1. 서버 셋을 연달아 열고 `alt+1/2/3`으로 전환 → 각 탭이 자기 셸·자기 스크롤백을 유지한다.
2. 탭 A에서 `ping`을 돌려 두고 B로 전환했다가 돌아오면 **그 사이 출력이 쌓여 있다**.
   B에 있는 동안 A의 탭 라벨에 `•`가 뜬다.
3. 백그라운드 탭에서 vim을 띄운 채 창을 리사이즈 → 그 탭으로 돌아갔을 때 화면이 안 깨진다.
4. 연결된 상태에서 네트워크를 끊는다(Wi-Fi off 또는 `sudo iptables -A OUTPUT -d <host> -j DROP`)
   → 30초 안에 `⟳`와 카운트다운이 뜨고, 네트워크를 되살리면 **자동으로 다시 붙는다**.
   재연결 후 화면은 새 셸(초기화됨)이고 이전 내용은 사라진다 — 의도된 동작.
5. 끊긴 탭에서 `r` → 카운트다운을 기다리지 않고 즉시 재시도.
6. 원격에서 `exit` → 그 탭만 닫히고 나머지 탭은 멀쩡하다. 마지막 탭이면 `empty`로.
7. 탭을 8개 열고 9번째 시도 → 거절 메시지. 몇 개 닫고 다시 열면 정상 동작(슬롯 재사용).
8. 탭이 여러 개인 상태에서 `f`로 SFTP 모드 진입 → SFTP는 여전히 하나이고, 나갔다 오면
   탭들이 그대로 살아 있다.

## v4에서 하지 않는 것 (의도적 제외)
- 셸 상태 복원 / 명령 재생 / 원격 세션 유지(tmux) — 재연결은 새 셸이다.
- 탭 재정렬·이름변경·분할(split) → v7
- 탭마다 SFTP 연결 — SFTP는 하나로 유지한다.
- 사이드바 검색/필터, 그룹/폴더, `~/.ssh/config` import → **v5**
- 비밀번호 키체인, ssh-agent, 점프호스트 → **v6**
- 포트포워딩, 테마/키맵 설정 → **v7**
