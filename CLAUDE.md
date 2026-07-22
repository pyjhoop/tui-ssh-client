# CLAUDE.md

TUI SSH 클라이언트. 좌측 사이드바에 서버 목록, 우측 패널에 **실제 SSH PTY 세션을 임베딩**해서 렌더한다.
상세 설계와 v0 범위는 `docs/V0_plan.md` 참조 — 구현 결정이 바뀌면 그 문서도 같이 갱신할 것.

## 현재 상태
v0 구현 완료. `docs/V0_plan.md`의 구조대로 스캐폴딩됐고 `go build`/`go test`/`go vet` 모두 통과한다.
아직 실제 sshd 상대로 한 수동 확인(vim 같은 풀스크린 앱 갱신)은 남아 있다 — 이게 v0 수용 기준.

키 입력은 변환 테이블(`terminal.go:keyToVT`)로 `tea.KeyMsg` → `uv.KeyPressEvent`까지만 바꾸고,
ANSI 인코딩은 `emu.SendKey`에 맡긴다. 이 경로 덕에 application cursor keys 모드(DECCKM)가
자동으로 맞는다 — 직접 시퀀스를 만들지 말 것.

### 절대 건드리면 안 되는 것: `terminal.go:keyPump`
인코딩된 바이트는 `emu.Read()`(io.Pipe)로 나오고 `keyPump`가 SSH stdin에 흘린다. 이 pump는
**항상 읽고 있어야 하고, 에뮬레이터는 절대 Close 하면 안 된다**:
- 에뮬레이터는 `ESC[6n` 같은 터미널 질의에 **`emu.Write` 안에서** 응답을 그 파이프에 쓴다.
  `emu.Write`는 UI goroutine(`Update`)에서 호출되므로, 읽는 쪽이 없으면 bash/vim이 질의를
  던지는 순간 **앱 전체가 데드락**한다. 그래서 pump는 세션 write가 실패해도 계속 읽는다.
- `vt.Emulator.Close()`는 블록된 `Read`를 깨우는 유일한 수단이지만 라이브러리 내부에서
  그 `Read`와 **data race**가 난다(`-race`로 재현됨). 그래서 에뮬레이터와 pump는 **프로세스
  수명 내내 하나만** 두고, 세션이 바뀌면 `resetEmulator`(ESC c)로 화면만 초기화한 뒤
  `pump.attach/detach`로 대상만 바꾼다.

### 레이아웃 (세로 예산)
`row 0` 상단 마진 / `row 1..h-2` 사이드바·우측 패널(높이 동일) / `row h-1` 상태줄.
우측 패널은 사이드바의 "Servers"와 짝을 이루는 **자체 타이틀바**(`rightHeaderRows` = 제목+빈줄)를
갖는다. 터미널 모드일 때 여기에 접속 중인 세션 이름과 `user@host:port`가 뜬다.
그래서 `rightInner()`가 돌려주는 rows는 패널 높이가 아니라 **타이틀바를 뺀 본문 높이**이고,
그게 곧 vt 에뮬레이터와 원격 PTY 크기다. 좌측 패널 clamp에는 `panelHeight()`를 써야 한다
(rows를 쓰면 사이드바만 2줄 짧아진다 — `TestLayoutAlignment`가 잡는다).
두 패널은 `clampBlock`으로 테두리 적용 **전에** 같은 크기 사각형으로 잘라낸다 — lipgloss의
`Height()`는 패딩만 하고 잘라내진 않아서, 내용이 길면 한쪽 패널만 아래로 밀린다.
`topMargin`/`statusRows`/`borderSize`/`padX`/`padY`가 유일한 출처이고,
마우스 좌표 변환(`rowToIndex`, form 클릭)도 같은 상수를 쓴다. 정렬은 `TestLayoutAlignment`가 고정한다.

## 개발 명령
```bash
go build ./...
go test ./internal/...
go run .            # TUI라 비대화형으로 실행하면 화면이 깨진 채로 멈춘 것처럼 보인다
```

## 확정된 설계 결정 (임의로 뒤집지 말 것)
- **Go + Bubble Tea**. SSH는 `golang.org/x/crypto/ssh`로 직접 연결하며, `ssh` 바이너리를 exec 하지 않는다.
- 세션은 **전체화면 핸드오프가 아니라 우측 패널 임베딩**. SSH stdout 바이트를 `github.com/charmbracelet/x/vt` 가상 터미널에 먹이고, 그 셀 그리드를 매 프레임 우측 패널 크기로 렌더한다.
- 인증은 password / key 둘 다 지원. 폼에 키 본문을 붙여넣으면 `~/.config/ssh-client/keys/<id>.pem`에 **0600**으로 저장하고 경로만 `KeyPath`에 기록한다.
- 저장소는 평문 JSON: `${XDG_CONFIG_HOME:-~/.config}/ssh-client/servers.json`. v0는 비밀번호도 평문 — 대신 저장 시 경고를 1회 노출한다. 암호화/키체인은 v3 항목이므로 v0에서 앞당기지 않는다.

## 아키텍처

```
main.go                    tea.NewProgram(ui.New(store), tea.WithAltScreen(), tea.WithMouseCellMotion())
└─ internal/ui/app.go      루트 model — 레이아웃/포커스/모드 상태머신, 키 라우팅
   ├─ sidebar.go           좌측 서버 리스트 (bubbles/list)  ── 고정 폭 30
   ├─ form.go              우측 연결정보 입력 폼 (textinput/textarea)
   └─ terminal.go          우측 임베디드 터미널 뷰 (x/vt 셀 그리드 → string)
internal/config/store.go   servers.json + keys/ 디렉토리 관리
internal/ssh/session.go    Dial → RequestPty → Shell, stdout 펌프, WindowChange
internal/model/server.go   Server 구조체 (UI·config·ssh가 공유하는 유일한 자료구조)
```

의존 방향은 `ui → {config, ssh} → model` 한 방향이다. `config`와 `ssh`는 서로를 모르고, `model`은 아무것도 import 하지 않는다.

### 렌더 파이프라인 (핵심)
Bubble Tea는 매 프레임 화면 전체를 문자열로 다시 그린다. 따라서 SSH 세션도 "문자열을 만들어내는 컴포넌트"로 환원해야 한다:

```
sshd ──stdout bytes──▶ session.go 읽기 goroutine
                          │  tea.Cmd로 msg 전달 (goroutine에서 model을 직접 만지지 않는다)
                          ▼
                    app.Update(outputMsg)
                          │  vt.Write(bytes)      ← ANSI 파싱·커서·스크롤은 x/vt가 담당
                          ▼
                    app.View() → terminal.go가 vt 셀 그리드를 순회해 lipgloss 문자열로 변환
                          │
                          ▼
                    lipgloss.JoinHorizontal(sidebar, right)
```

- **터미널 상태는 `vt` 인스턴스가 유일한 소유자**다. 출력 바이트를 직접 파싱하거나 별도 스크롤백 버퍼를 만들지 말 것.
- 읽기 goroutine은 채널로 바이트를 넘기고, 그것을 받아 `tea.Msg`로 변환하는 `tea.Cmd`를 매번 다시 스케줄한다(Bubble Tea의 표준 "펌프" 패턴). goroutine 안에서 model 필드를 건드리면 데이터 레이스.

### 입력 경로
```
tea.KeyMsg ──▶ app.Update ──▶ focus == session ?
                               ├─ yes: Ctrl+B면 focus=sidebar로 탈출, 그 외엔 키 → ANSI 시퀀스 변환 → session stdin
                               └─ no : sidebar / form 컴포넌트로 위임
```
특수키(방향키, F키, Home/End 등)는 `tea.KeyMsg`를 그대로 보낼 수 없고 ANSI 이스케이프 시퀀스로 변환해서 stdin에 써야 한다. 변환 테이블은 `terminal.go`에 한 곳으로 모은다.

### 상태 전이
```
empty ──(+ Connect 선택)──▶ form ──(저장)──▶ empty        + 사이드바 리스트 리로드
empty ──(서버 선택)──▶ connecting ──(성공)──▶ terminal
                          └──(실패)──▶ empty + 에러 메시지
terminal ──(Ctrl+B)──▶ focus만 sidebar로 (세션은 살아있고 화면도 계속 렌더)
terminal ──(원격 셸 종료)──▶ empty
```
`Ctrl+B` 탈출은 세션을 끊는 게 아니라 **포커스만 옮기는 것**이다. 세션 종료와 포커스 이동을 헷갈리지 말 것.

## 코드 규약
- 모든 구현은 `internal/` 아래. `config`(저장소) / `ssh`(연결·PTY) / `ui`(뷰) / `model`(자료구조) 경계를 지키고, `ui`가 파일 IO나 net 연결을 직접 하지 않게 한다.
- 블로킹 작업(SSH Dial, 파일 IO)은 반드시 `tea.Cmd`로 비동기 처리. Bubble Tea의 `Update`에서 블로킹하면 UI 전체가 멈춘다.
- 루트 model의 상태는 `focus`(sidebar|form|session)와 `rightMode`(empty|form|terminal) 두 축. **키 라우팅은 항상 `focus` 기준**이며, session 포커스일 때는 탈출키(`Ctrl+B`)만 가로채고 나머지 키는 전부 SSH stdin으로 흘린다.
- `tea.WindowSizeMsg`를 받으면 세 곳을 모두 갱신해야 한다: 패널 레이아웃, `vt` 리사이즈, SSH `WindowChange`. 하나라도 빠지면 화면이 어긋난다.

## 테스트
- `config`/`model` 순수 로직만 유닛 테스트한다(저장/로드 라운드트립, 키 파일 권한 0600 검증).
- 임베디드 터미널 렌더는 자동화하지 않는다. 로컬 sshd에 붙어 `vim` 같은 풀스크린 앱이 정상 갱신되는지 수동 확인하는 것이 v0 수용 기준.

## 환경 주의
- Go는 `/usr/local/go`가 아니라 **`~/.local/go`**에 설치돼 있다(go1.25.0, root 없이 설치).
  `~/.bashrc` 마지막 줄 PATH에 `$HOME/.local/go/bin`이 들어 있으므로 새 셸에서는 그냥 `go`가 잡힌다.
  PATH를 물려받지 못하는 환경에서만 `export PATH=$HOME/.local/go/bin:$PATH`.
- 의존성이 요구해서 `go.mod`의 go 디렉티브는 **1.25.0**이다(계획서의 "1.22+"보다 높음). 툴체인을 낮추면 빌드가 안 된다.
- 이 시스템에는 **sshd가 없다**. 세션 계층은 `internal/ssh/session_test.go`가 in-process SSH 서버를 띄워 검증한다.
