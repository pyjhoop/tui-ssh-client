# TUI SSH Client — v1 구현 계획 (세션 안정화)

## Context (왜 만드는가)
v0는 "붙고, 보이고, 친다"까지만 한다. 실사용에 들어가면 바로 걸리는 것들이 남아 있다:

- **호스트키를 검증하지 않는다.** `session.go`가 `InsecureIgnoreHostKey()`를 쓴다. MITM에
  무방비이고, v2 SFTP가 같은 다이얼 경로를 재사용하므로 **v2보다 먼저 고쳐야 검증되지 않은
  연결 경로가 둘로 늘어나지 않는다.**
- **스크롤백이 없다.** 화면 밖으로 흘러간 출력은 되돌려 볼 방법이 없다.
- **에러가 한 줄짜리 원문**이다. `dial tcp ...: i/o timeout`을 그대로 상태줄에 뱉는다.
  원인도 다음 행동도 알려주지 않는다.
- **등록한 서버를 고칠 수 없다.** 오타가 나면 지우고 다시 만들어야 하고, `d`는 확인 없이
  즉시 지운다. 지워도 `keys/<id>.pem`은 남는다.

v1은 이 네 가지를 닫는다. 새 화면 모드를 만들지 않고 **기존 상태머신에 붙이는 것**이 원칙이다.

## 범위
| 포함 | 제외 (→ 이후 버전) |
|---|---|
| known_hosts 검증 + TOFU 승인 흐름 | 자동 재연결 / 세션 유지 (v3) |
| 터미널 스크롤백 (휠·shift+pgup) | 다중 세션 탭 (v3) |
| 연결 에러 분류 + 재시도 UX | ssh-agent / 점프호스트 (v4) |
| 서버 편집 + 삭제 확인 + 키 파일 정리 | 비밀번호 키체인 저장 (v4) |
| 리사이즈 견고화 | 패스프레이즈 걸린 키 (v4) |

---

## 1. known_hosts 호스트키 검증

### 파일 정책
- **읽기**: `~/.ssh/known_hosts` + `${XDG_CONFIG_HOME:-~/.config}/ssh-client/known_hosts`
  (존재하는 것만). `knownhosts.New(paths...)`가 여러 파일을 받는다.
- **쓰기**: **앱 소유 파일에만** 쓴다 (`config.Store.KnownHostsPath()`, 0600). 사용자의
  OpenSSH `known_hosts`는 우리가 건드리지 않는다 — 사용자가 다른 도구로 관리하는 파일이다.
- 줄 생성은 `knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)`를 쓴다. 직접
  포맷하지 말 것 (해시된 호스트명·포트 표기 규칙이 있다).

### 세 가지 결과
`knownhosts` 콜백이 주는 `*knownhosts.KeyError`로 판별한다:

| 상황 | 판별 | 동작 |
|---|---|---|
| 알려진 키와 일치 | err == nil | 그대로 접속 |
| 처음 보는 호스트 | `KeyError` + `len(Want) == 0` | **사용자에게 확인** (TOFU) |
| 키가 바뀜 | `KeyError` + `len(Want) > 0` | **거부.** 승인 옵션을 주지 않는다 |
| 폐기된 키 | `*knownhosts.RevokedError` | 거부 |

> 키 불일치에 "그래도 접속" 단축키를 두지 않는 것은 의도적이다. 이게 정확히 MITM이 보이는
> 모습이고, 한 키스트로크로 넘길 수 있으면 검증을 넣는 의미가 없다. 사용자는 known_hosts
> 파일을 직접 고쳐야 한다 — 에러 화면이 그 파일 경로와 지워야 할 줄 번호를 알려준다.

### TOFU 승인 흐름 (비동기 문제)
호스트키 콜백은 **Dial 중인 goroutine**에서 불린다. `Update`에서 물어볼 수가 없다.
→ 콜백이 채널로 질문을 넘기고 **그 goroutine에서 블록**한다. UI goroutine은 멈추지 않는다.

```go
// internal/ssh/hostkey.go
type HostKeyPrompt struct {
    Addr        string
    Fingerprint string        // ssh.FingerprintSHA256(key)
    KeyType     string
    Line        string        // 승인 시 known_hosts에 추가될 줄
    reply       chan bool
}

func (p *HostKeyPrompt) Accept() { ... }   // reply <- true
func (p *HostKeyPrompt) Reject() { ... }   // reply <- false
```
- `Connect`/`Dial`은 `prompts chan<- *HostKeyPrompt`를 받는다. UI는 이 채널을
  `waitForOutput`과 **같은 펌프 패턴**으로 읽어 `hostKeyPromptMsg`로 바꾼다.
- 대기는 무한이 아니다: `select`에 `time.After(HostKeyPromptTimeout = 60s)`를 걸어 타임아웃 시
  거부로 처리한다. 앱이 먼저 죽어도 goroutine이 영원히 남지 않는다.
- 승인되면 `config.Store.AppendKnownHost(line)`을 호출한 뒤 핸드셰이크를 계속한다.

### UI
확인 패널은 **§4에서 만드는 공용 confirm 패널**을 그대로 쓴다:
```
  Unknown host

  10.0.0.1:22
  ED25519 SHA256:9oQ4x…/Kk2s

  이 호스트를 처음 봅니다. 지문이 맞는지 서버에서 직접 확인하세요.
  승인하면 ~/.config/ssh-client/known_hosts 에 저장됩니다.

  [enter] 신뢰하고 접속   [esc] 취소
```

---

## 2. 연결 에러 UX

### 타입 있는 에러 (핵심)
지금은 `err.Error()` 문자열이 그대로 상태줄에 간다. UI가 문자열을 파싱하게 두면 안 되므로
`internal/ssh/errors.go`에 센티널을 정의하고 `%w`로 감싼다:

```go
var (
    ErrAuth            = errors.New("authentication failed")
    ErrUnreachable     = errors.New("host unreachable")
    ErrTimeout         = errors.New("connection timed out")
    ErrHostKeyUnknown  = errors.New("host key not accepted")
    ErrHostKeyMismatch = errors.New("host key mismatch")
    ErrKeyFile         = errors.New("private key problem")
)
```
`Dial`이 `x/crypto/ssh`·`net` 에러를 분류해 감싼다:
- `*net.OpError` / `*net.DNSError` → `ErrUnreachable`
- `os.IsTimeout(err)` 또는 `context deadline` → `ErrTimeout`
- `ssh: unable to authenticate` / `*ssh.PartialSuccessError` → `ErrAuth`
- `*knownhosts.KeyError` (Want 있음) → `ErrHostKeyMismatch`
- 키 읽기·파싱 실패(`authMethods` 안) → `ErrKeyFile`

UI는 `errors.Is`로만 갈라진다. **문자열 매칭 금지** — 테스트가 이걸 고정한다.

### 에러 카드
`rightMode == rightEmpty`에서 한 줄로 뱉던 것을 우측 패널의 에러 카드로 바꾼다:
```
  Connection failed — prod-web

  ✗ authentication failed
    deploy@10.0.0.1:22

  비밀번호나 키가 맞지 않습니다.

  [r] 다시 시도   [e] 접속 정보 수정   [esc] 닫기
```
원인별 안내 문구와 제공 액션은 `errorAdvice(err) (headline, hint string, actions)` 한 함수에
모은다. `r`은 마지막 접속 시도를 그대로 재실행, `e`는 §4의 편집 폼을 그 서버로 연다.

---

## 3. 스크롤백 & 리사이즈 견고화

### 스크롤백은 이미 있다
`x/vt` 에뮬레이터가 스크롤백 버퍼를 갖고 있다 (`DefaultScrollbackSize = 10000`):
`emu.ScrollbackLen()`, `emu.ScrollbackCellAt(x, y)`, `emu.SetScrollbackSize(n)`.
**직접 버퍼를 만들지 말 것** — v0 규약대로 터미널 상태의 소유자는 vt 하나다.

### 렌더
`terminal.go`에 `renderScrolled(emu, cols, rows, offset)`를 추가한다. `offset == 0`이면
기존 `renderEmulator` 그대로(빠른 경로), `offset > 0`이면 위쪽 `offset`줄을
`ScrollbackCellAt`로, 나머지를 화면 셀로 채워 합성한다. 셀 → 스타일 문자열 변환은
`highlightCursor`가 이미 다루는 `uv.Cell`을 쓰므로 같은 방식이다. 스크롤 중에는 커서를
그리지 않는다.

### 조작
| 입력 | 동작 |
|---|---|
| 마우스 휠 (포인터가 터미널 패널 위) | 3줄씩 스크롤 |
| `shift+pgup` / `shift+pgdn` | 반 화면씩 |
| 그 외 아무 키 | **바닥으로 복귀 후** 그 키를 세션에 전달 |

- `offset`은 `[0, ScrollbackLen()]`로 클램프. 새 출력이 와도 **offset을 유지**한다(읽는 중에
  화면이 튀면 안 된다). 대신 패널 타이틀바에 `SCROLL −120`을 띄워 지금 과거를 보고 있음을
  명시하고, 아무 키나 누르면 바닥으로 돌아간다.
- **대체화면(vim, less 등)에서는 스크롤백을 끈다.** vt의 스크롤백은 메인 스크린 것이고,
  alt screen에서 위로 올리면 무의미한 과거가 보인다. 대신 휠을 **위/아래 화살표 3개**로
  변환해 `emu.SendKey`로 보낸다(실제 터미널의 alternate-scroll 동작). alt screen 판별은
  `emu`의 모드 조회를 쓴다.

### 리사이즈
- `resize()`에서 스크롤 오프셋을 0으로 되돌린다(리플로우된 과거 줄과 오프셋은 맞지 않는다).
- **같은 크기면 `WindowChange`를 보내지 않는다.** 창을 드래그하면 `WindowSizeMsg`가 수십 번
  오는데 지금은 매번 goroutine을 띄운다. `Session`에 마지막으로 보낸 `cols/rows`를 기억시켜
  변화가 없으면 즉시 반환하도록 한다 (`Session.Resize` 안에서, 기존 mutex 아래).
- 0 이하 크기 방어는 v0에 이미 있다(`resize`의 80x24 폴백, `Session.Resize`의 `< 1` 반환).

---

## 4. 서버 편집 · 삭제 · 공용 확인 패널

### 공용 확인 패널 (v2가 재사용한다)
`internal/ui/confirm.go`:
```go
type confirm struct {
    title   string
    lines   []string
    warn    string          // 있으면 경고색 한 줄
    accept  string          // "[enter] 삭제"
    onYes   tea.Cmd
}
```
`App.confirm *confirm`이 non-nil이면 **우측 패널 본문을 이것으로 대체**하고 다른 키를 전부
가로챈다 (`enter`/`y` → `onYes`, `esc`/`n` → 취소). lipgloss v1에는 안전한 오버레이 합성이
없고 ANSI 섞인 행을 스플라이싱하면 폭 계산이 깨지므로, **영역 교체가 레이아웃 불변식을
지키는 유일한 방법**이다. 사이드바는 그대로 남아 화면은 여전히 2-박스다.

v1에서 호스트키 승인·삭제 확인이 이걸 쓰고, v2의 전송 확인 alert가 같은 것을 재사용한다.

### 편집
- `config.Store`에 `Update(srv model.Server) error` 추가 (ID로 찾아 교체, 없으면 에러).
- `form`에 `editingID string` 추가, `newFormFor(srv, w, h)`로 필드를 채운다. 저장 시
  `editingID != ""`면 `saveServer` 대신 `updateServer` 커맨드로 간다.
- 키 인증 편집 시 **키 본문 textarea는 비워 둔다.** 비어 있으면 기존 `KeyPath`를 유지하고,
  새로 붙여넣으면 같은 `keys/<id>.pem`을 덮어쓴다(ID가 그대로이므로 경로도 그대로).
- 사이드바에서 `e` → 선택된 서버 편집 (`+ Connect` 항목이면 무시). 패널 타이틀은
  "Edit connection".

### 삭제
- `d`는 이제 즉시 지우지 않고 확인 패널을 띄운다: `alpha (deploy@10.0.0.1) 를 삭제합니다.`
- `Store.Remove(id)`가 **`keys/<id>.pem`도 지운다** — 단, `KeyPath`가 우리 `KeysDir()` 아래에
  있을 때만. 사용자가 직접 지정한 `~/.ssh/id_ed25519`는 절대 지우면 안 된다.

---

## 변경 / 추가 파일

| 파일 | 내용 |
|---|---|
| `internal/ssh/hostkey.go` | **신규** known_hosts 콜백, `HostKeyPrompt`, TOFU 채널 |
| `internal/ssh/errors.go` | **신규** 센티널 에러 + 분류 |
| `internal/ssh/session.go` | `Dial()` 추출(v2와 공유), 호스트키 콜백 주입, `Resize` no-op 스킵 |
| `internal/config/store.go` | `Update`, `KnownHostsPath`, `AppendKnownHost`, `Remove`의 키 파일 정리 |
| `internal/ui/confirm.go` | **신규** 공용 확인 패널 (v2 재사용) |
| `internal/ui/terminal.go` | `renderScrolled`, alt screen 판별, 휠→화살표 변환 |
| `internal/ui/form.go` | `editingID`, `newFormFor` 프리필 |
| `internal/ui/app.go` | 호스트키 프롬프트 펌프, 에러 카드, 스크롤 오프셋 상태, `e`/`d`/`r` 키, 휠 처리 |

> `internal/ssh/session.go`의 `Dial()` 추출은 v1과 v2가 **같이 필요로 한다.** v1을 먼저 하면
> v2는 이미 검증된 다이얼 경로 위에 SFTP를 올리기만 하면 된다.

## 검증 (end-to-end)

**자동 테스트** (`go test ./internal/...`)
1. `internal/ssh/hostkey_test.go` — `session_test.go`의 in-process 서버 하네스를 쓴다.
   서버 호스트키를 임시 known_hosts에 써 두면 접속 성공. 다른 키를 써 두면
   `errors.Is(err, ErrHostKeyMismatch)`. 빈 파일이면 프롬프트가 채널로 오고,
   `Accept()` 후 접속이 이어지며 파일에 줄이 추가된다. `Reject()`면 `ErrHostKeyUnknown`.
2. `internal/ssh/errors_test.go` — 틀린 비밀번호 → `errors.Is(err, ErrAuth)`,
   닫힌 포트 → `ErrUnreachable`. **문자열 비교를 쓰지 않는다.**
3. `internal/config/store_test.go` 확장 — `Update` 라운드트립, `Remove`가 `KeysDir()` 안의
   pem은 지우고 바깥 경로는 **건드리지 않는지**.
4. `internal/ui/smoke_test.go` 확장 —
   - `TestScrollbackRendersHistory`: 에뮬레이터에 200줄을 쓰고 `offset=10`일 때 첫 행이
     기대한 과거 줄인지, 행 수·폭이 여전히 정확한지.
   - `TestConfirmPanelSwallowsKeys`: 확인 패널이 떠 있으면 다른 키가 세션으로 새지 않는다.
   - `TestEditFormPrefills`: `newFormFor(srv)`가 필드를 채우고 `Server()`가 같은 값을 돌려준다.
   - `TestLayoutAlignment`가 확인 패널·에러 카드 상태에서도 통과 (모든 행이 정확히 width).
5. `go vet ./...`, `go build ./...` 통과.

**수동 확인 (v1 수용 기준)**
1. 새 호스트 접속 → 지문 확인 패널 → 승인 → 접속되고 `~/.config/ssh-client/known_hosts`에
   줄이 생긴다. 재접속 시 다시 묻지 않는다.
2. known_hosts의 그 줄을 아무 키로 바꾼 뒤 접속 → 거부되고, 승인 단축키가 **없다**.
3. 틀린 비밀번호로 접속 → "authentication failed" 카드 → `e`로 폼이 열리고 고쳐서 `r`로 재시도.
4. `yes | head -500` 실행 후 휠/`shift+pgup`으로 과거가 보이고, 아무 키나 누르면 바닥 복귀.
5. `vim` 안에서 휠이 커서 이동으로 동작하고 스크롤백이 끼어들지 않는다.
6. 서버를 `e`로 편집해 포트를 바꾸면 리스트와 접속 모두 반영된다. `d`는 확인을 거치고,
   삭제 후 `keys/<id>.pem`이 사라진다.
7. 창을 빠르게 드래그 리사이즈해도 화면이 깨지지 않고 원격 `stty size`가 최종 크기와 일치한다.
