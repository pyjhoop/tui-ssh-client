# CLAUDE.md

TUI SSH 클라이언트. 좌측 사이드바에 서버 목록, 우측 패널에 **실제 SSH PTY 세션을 임베딩**해서 렌더한다.
사이드바에서 `f`를 누르면 우측이 Local | Remote 두 파일 패널로 갈라지는 **SFTP 모드**가 된다.
상세 설계와 범위는 `docs/V0_plan.md`(기반 구조)·`docs/V1_plan.md`(세션 안정화)·
`docs/V2_plan.md`(SFTP) 참조 — 구현 결정이 바뀌면 그 문서도 같이 갱신할 것.
`docs/V3_plan.md`(SFTP 심화 — 재귀 전송·진행률·다중 선택·삭제/이름변경)는 **아직 계획**이다.

## 현재 상태
v0 + v1 + v2 구현 완료. `go build`/`go test -race`/`go vet` 모두 통과한다.
아직 실제 sshd 상대로 한 수동 확인은 남아 있다 — 각 계획서의 "수동 확인" 절이 수용 기준이다
(v0: vim 같은 풀스크린 앱 갱신 / v1: TOFU 승인, 키 변경 거부, 스크롤백, 편집·삭제, 드래그 리사이즈 /
v2: 드래그 전송, 양방향 전송, 디렉터리 거부, 덮어쓰기 경고).

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

SFTP 모드에서는 우측이 두 패널로 갈라진다(`sftpPanels`). 폭은 `sftpWidths()` 한 곳에서만
쪼개고(`localOuter = total/2`, 나머지는 remote가 흡수) — 렌더와 마우스 히트테스트(`sideAt`)가
**같은 함수**를 부르므로 클릭이 보이는 곳에 떨어진다. 각 패널은 여전히 `rightHeaderRows`짜리
자체 타이틀바(라벨 + 현재 경로)를 갖고 본문 높이도 `panelHeight() - rightHeaderRows`로 같아서,
세 패널이 같은 행에서 열리고 닫힌다(`TestSFTPLayoutAlignment`가 `╭` 3개를 확인).

### SFTP 모드의 다이얼로그만 예외적으로 떠 있는다 (`overlay`)
확인·에러 카드는 **패널을 대체하지 않고 화면 정중앙에 겹쳐 그린다**(`sftpModal` → `overlay`).
세 패널은 그대로 서 있고 `TestSFTPModalFloatsOverThePanes`가 `╭` 3개를 계속 확인한다.
`overlay`는 **ANSI가 섞인 행을 자르는 프로젝트 내 유일한 지점**이다:
- 각 행을 `left | reset | box | reset | right`로 다시 만든다. `ansi.Truncate` /
  `ansi.TruncateLeft`가 자른 지점까지의 SGR 상태를 **누적해서 다시 내보내므로**
  박스 오른쪽 패널이 색을 잃지 않는다.
- 두 조각 모두 `padLine`으로 정확한 폭에 다시 맞춘다 — 경계에 2칸짜리 글자가 걸리면
  조각이 1칸 짧아진다.
- 박스가 프레임 밖으로 나가면 **그 행을 건너뛴다**(폭을 늘리느니 안 그리는 쪽).
`go test`는 stdout이 TTY가 아니라 lipgloss가 색을 다 지운다 — 그래서 스타일 보존은
`TestOverlayPreservesWidthAndStyle`가 이스케이프를 직접 써서 검증하고,
`TestSFTPModalWithColour`가 `SetColorProfile(TrueColor)`로 전체 화면을 한 번 더 확인한다.
**둘 중 하나라도 지우면 색 있는 터미널에서만 레이아웃이 깨진다.**

## 개발 명령
```bash
go build ./...
go test ./internal/...
go run .            # TUI라 비대화형으로 실행하면 화면이 깨진 채로 멈춘 것처럼 보인다
```

### 스크롤백은 `App.scrollOff` 하나로만 표현한다
과거 화면 버퍼를 따로 만들지 말 것 — vt 에뮬레이터가 이미 스크롤백을 갖고 있고,
`renderScrolled`가 "위 `offset`줄은 스크롤백, 나머지는 라이브 화면"으로 합성한다.
새 출력이 와도 offset은 유지된다(읽는 중 화면이 튀면 안 된다). 아무 키나 누르면 0으로 복귀하고,
`resize`도 0으로 되돌린다(리플로우된 과거 줄과 옛 offset은 맞지 않는다).
**대체화면(vim, less)에서는 `maxScrollOffset`이 0이고**, 휠은 `altScreenScroll`로 화살표 키가 된다.

## 확정된 설계 결정 (임의로 뒤집지 말 것)
- **Go + Bubble Tea**. SSH는 `golang.org/x/crypto/ssh`로 직접 연결하며, `ssh` 바이너리를 exec 하지 않는다.
- 세션은 **전체화면 핸드오프가 아니라 우측 패널 임베딩**. SSH stdout 바이트를 `github.com/charmbracelet/x/vt` 가상 터미널에 먹이고, 그 셀 그리드를 매 프레임 우측 패널 크기로 렌더한다.
- 인증은 password / key 둘 다 지원. 폼에 키 본문을 붙여넣으면 `~/.config/ssh-client/keys/<id>.pem`에 **0600**으로 저장하고 경로만 `KeyPath`에 기록한다.
- 저장소는 평문 JSON: `${XDG_CONFIG_HOME:-~/.config}/ssh-client/servers.json`. 비밀번호도 평문 — 대신 저장 시 경고를 1회 노출한다. 암호화/키체인은 v4 항목이므로 앞당기지 않는다.
- **호스트키는 항상 검증한다.** `InsecureIgnoreHostKey`는 v1에서 없어졌고 다시 넣지 말 것.
  읽기는 `~/.ssh/known_hosts` + 우리 파일, **쓰기는 우리 파일에만**. 키가 바뀐 경우에는
  **승인 단축키를 만들지 않는다** — 그게 정확히 MITM이 보이는 모습이라 한 키로 넘길 수 있으면
  검증이 무의미해진다. 사용자가 known_hosts를 직접 고쳐야 하고, 에러 카드가 그 파일·줄 번호를 알려준다.
- **연결 실패는 `internal/ssh/errors.go`의 센티널로 분류**하고 UI는 `errors.Is`로만 갈라진다.
  에러 문자열 매칭 금지(`errorAdvice`가 유일한 문구 매핑 지점, `TestErrorCardOffersActions`가 고정).
- **SFTP 연결은 터미널 세션과 별개의 TCP 연결**이다. 한쪽을 끊어도 다른 쪽은 산다 —
  `teardownSession`과 `teardownSFTP`는 서로를 부르지 않고, `gen`/`sftpGen`도 각자 센다.
- v2는 **파일만, 한 번에 한 건만** 전송한다. 디렉터리는 `sftp.ErrIsDir`로 즉시 거절하고
  (재귀 전송은 v3), `busy` 플래그가 두 번째 전송을 막는다. 진행률·취소·재개도 v3.

## 아키텍처

```
main.go                    tea.NewProgram(ui.New(store), tea.WithAltScreen(), tea.WithMouseCellMotion())
└─ internal/ui/app.go      루트 model — 레이아웃/포커스/모드 상태머신, 키 라우팅
   ├─ sidebar.go           좌측 서버 리스트 (bubbles/list)  ── 고정 폭 30
   ├─ form.go              우측 연결정보 입력 폼 (textinput/textarea), 신규·편집 겸용
   ├─ confirm.go           우측 패널 본문을 대체하는 공용 확인 패널 (호스트키·삭제·전송)
   ├─ errorcard.go         센티널 에러 → 안내 문구·액션 (errorAdvice)
   ├─ sftp.go              filePane(1행/항목), 드래그 3단계, SFTP 키 라우팅, 3-패널 렌더
   └─ terminal.go          우측 임베디드 터미널 뷰 (x/vt 셀 그리드 → string), 스크롤백 합성
internal/config/store.go   servers.json + keys/ + known_hosts 관리
internal/ssh/session.go    Dial → RequestPty → Shell, stdout 펌프, WindowChange
internal/ssh/hostkey.go    known_hosts 검증, TOFU 프롬프트 채널
internal/ssh/errors.go     센티널 에러 + net/ssh 에러 분류
internal/sftp/browser.go   Browser 인터페이스 + Local (os.ReadDir)
internal/sftp/remote.go    ssh.Dial → sftp.NewClient, ReadDir/Stat/Getwd
internal/sftp/transfer.go  Upload / Download / StatLocal
internal/model/server.go   Server·FileEntry (UI·config·ssh·sftp가 공유하는 유일한 자료구조)
```

`ssh.Dial`이 네트워크로 나가는 **유일한 지점**이다. SFTP도 그 위에 올라간다(`sftp.Connect`) —
검증되지 않은 다이얼 경로를 두 번째로 만들지 말 것. 호스트키 검증도 그래서 공짜로 따라온다.
`ssh`는 `config`를 import 하지 않으므로 known_hosts 경로와 append 함수는 `ssh.Options`로 UI가 주입한다.

의존 방향은 `ui → {config, ssh, sftp} → model`, 그리고 `sftp → ssh` 한 방향이다.
`config`와 `ssh`는 서로를 모르고, `model`은 아무것도 import 하지 않는다.

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
empty ──(사이드바 e)──▶ form(editingID 채움) ──(저장)──▶ empty
empty ──(서버 선택)──▶ connecting ──(성공)──▶ terminal
                          └──(실패)──▶ error 카드 ──(r 재시도 / e 편집 / esc 닫기)
terminal ──(Ctrl+B)──▶ focus만 sidebar로 (세션은 살아있고 화면도 계속 렌더)
terminal ──(원격 셸 종료)──▶ empty

empty|terminal ──(사이드바 f)──▶ sftp(connecting) ──(성공)──▶ sftp
                                    └──(실패)──▶ sftp + 떠 있는 에러 카드
                                                 (패널은 안 내려간다 — r 재시도 / e 편집 / esc 닫기)
sftp ──(드롭 / space / 파일에서 enter)──▶ pending(확인) ──(enter)──▶ 전송 ──▶ sftp
sftp ──(Ctrl+B|esc)──▶ focus만 sidebar로 (SFTP 연결은 유지)
```
`Ctrl+B` 탈출은 세션을 끊는 게 아니라 **포커스만 옮기는 것**이다. 세션 종료와 포커스 이동을 헷갈리지 말 것.

`App.confirm`은 이 축과 **직교한다**: non-nil이면 `rightMode`가 뭐든 우측 패널 본문을 대체하고
`handleKey` 맨 앞에서 모든 키를 먹는다(답이 아닌 키는 버린다 — 뒤의 세션으로 새면 안 된다).
lipgloss v1에는 안전한 오버레이 합성이 **없다**. 그래서 기본은 여전히 영역 교체이고,
SFTP 모드에서만 직접 만든 `overlay`로 띄운다(위 절 참조) — 다른 모드에 오버레이를 퍼뜨리기 전에
그 절의 폭 계산 규칙을 먼저 읽을 것.
`App.pending`(전송 확인)과 `App.sftpErr`(연결 실패)도 키 규칙은 같다 — `handleKey`/`handleSFTPKey`
맨 앞에서 모든 키를 먹고(`TestPendingSwallowsKeys`, `TestSFTPErrorCardFloatsAndDismisses`),
마우스 드래그도 함께 막힌다. 렌더는 셋 다 같은 `confirm`/`errorCard` 위젯을 재사용한다.

### 드래그와 키보드는 `buildTransfer` 하나로 수렴한다
드롭(`handleSFTPMouse`의 release)이든 `space`/`enter`든 만들어내는 것은 `transferReq` 뿐이고,
확인 화면·전송 실행은 그 뒤로 완전히 공유된다(`TestKeyboardTransferMatchesDrag`가 고정).
- 릴리스 이벤트는 터미널에 따라 버튼을 `MouseButtonNone`으로 보고한다. **버튼 값으로 거르지 말고**
  "드래그 중이었는가"(`a.drag != nil`)로만 판단할 것.
- 덮어쓰기 여부는 **목적지 패널이 이미 들고 있는 목록**으로 판단한다. `Update`에서 원격 `Stat`을
  부르면 UI가 블로킹된다. 목록이 낡으면 경고가 안 뜰 뿐, 엉뚱한 곳으로 전송되지는 않는다.

### 호스트키 프롬프트는 goroutine 간 랑데부다
호스트키 콜백은 **Dial 중인 goroutine**에서 불리므로 `Update`에서 물어볼 수 없다.
콜백이 `Options.Prompts` 채널로 질문을 넘기고 **그 goroutine에서 블록**하며, UI는
`waitForOutput`과 같은 펌프 패턴(`waitForHostKey`)으로 받아 확인 패널을 띄운다.
`Accept`/`Reject`가 답을 돌려주면 핸드셰이크가 이어진다. 대기는 `HostKeyPromptTimeout`(60s)로
막혀 있다 — 앱이 먼저 죽어도 goroutine이 영원히 남지 않는다.

## 코드 규약
- 모든 구현은 `internal/` 아래. `config`(저장소) / `ssh`(연결·PTY) / `ui`(뷰) / `model`(자료구조) 경계를 지키고, `ui`가 파일 IO나 net 연결을 직접 하지 않게 한다.
- 블로킹 작업(SSH Dial, 파일 IO)은 반드시 `tea.Cmd`로 비동기 처리. Bubble Tea의 `Update`에서 블로킹하면 UI 전체가 멈춘다.
- 루트 model의 상태는 `focus`(sidebar|form|session|local|remote)와
  `rightMode`(empty|form|terminal|error|sftp) 두 축. **키 라우팅은 항상 `focus` 기준**이며,
  session 포커스일 때는 탈출키(`Ctrl+B`)만 가로채고 나머지 키는 전부 SSH stdin으로 흘린다.
- `tea.WindowSizeMsg`를 받으면 세 곳을 모두 갱신해야 한다: 패널 레이아웃, `vt` 리사이즈, SSH `WindowChange`. 하나라도 빠지면 화면이 어긋난다.

## 테스트
- `config`/`model`은 순수 로직 유닛 테스트(저장/로드 라운드트립, `Update`, 키 파일 권한 0600,
  `Remove`가 `KeysDir()` 안의 pem만 지우는지).
- `internal/ssh`는 in-process SSH 서버(`session_test.go`의 `startTestServer`)로 검증한다 —
  이 시스템에는 sshd가 없다. 호스트키 세 갈래와 에러 분류가 여기 걸려 있다.
- `internal/sftp`도 같은 하네스를 본떠(`remote_test.go`의 `startSFTPServer`) `subsystem sftp`에
  `pkgsftp.NewServer(ch)`를 붙여 업로드·다운로드 라운드트립까지 실제로 돈다.
- `internal/ui`는 터미널 없이 root model을 직접 두드린다. 레이아웃 불변식(`TestLayoutAlignment`,
  `TestLayoutAlignmentWithPanels`)은 확인 패널·에러 카드 상태에서도 모든 행이 정확히 width여야 한다.
- 풀스크린 앱(vim) 갱신만 자동화하지 않는다 — 로컬 sshd 수동 확인이 수용 기준.

## 환경 주의
- Go는 `/usr/local/go`가 아니라 **`~/.local/go`**에 설치돼 있다(go1.25.0, root 없이 설치).
  `~/.bashrc` 마지막 줄 PATH에 `$HOME/.local/go/bin`이 들어 있으므로 새 셸에서는 그냥 `go`가 잡힌다.
  PATH를 물려받지 못하는 환경에서만 `export PATH=$HOME/.local/go/bin:$PATH`.
- 의존성이 요구해서 `go.mod`의 go 디렉티브는 **1.25.0**이다(계획서의 "1.22+"보다 높음). 툴체인을 낮추면 빌드가 안 된다.
- 이 시스템에는 **sshd가 없다**. 세션 계층은 `internal/ssh/session_test.go`가 in-process SSH 서버를 띄워 검증한다.
