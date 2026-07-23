# TUI SSH Client — v9 구현 계획 (드래그 선택 · 클립보드 복사 · 스크롤백 연동)

## Context (왜 만드는가)
v0에서 `tea.WithMouseCellMotion()`을 켠 순간부터 터미널 **자체의** 드래그 선택이 우리 앱 위에서는
동작하지 않는다. v1에서 스크롤백까지 만들어 놓고 거기 뜬 에러 메시지 한 줄, 원격 로그의 URL 하나를
복사할 수 없다 — **이건 우리가 만든 회귀이므로 우리가 갚아야 한다.**

v9는 **한 축**이다: `internal/ui/terminal.go`의 렌더와 `handleMouse`. 다른 패키지는 건드리지 않고,
Go 의존성도 늘지 않는다.

> 점프호스트(ProxyJump)와 포트포워딩(`-L`/`-R`)은 이 문서에서 **빠졌다**. 전송 계층
> (`internal/ssh/`)을 건드리는 별개의 축이라 v10 이후의 주제다. v3 이후의 "한 버전에 두 축을
> 넣지 않는다"가 그 이유다.

## 범위
| 포함 | 제외 (→ 이후 버전) |
|---|---|
| 세션 패널 드래그 선택 (리니어) | 단어/줄 더블·트리플 클릭, 열 블록 선택 → v10 |
| 떼는 순간 OSC 52로 시스템 클립보드에 복사 | 클립보드 **읽기**(OSC 52 질의) |
| 스크롤백 상태에서의 선택·복사, 스크롤 시 선택 해제 | 키보드 yank 모드(vi식 visual) → v10 |
| 복사 결과·잘림을 상태줄에 알림 | 선택 내용 마스킹·필터링 |
| — | ProxyJump · 포트포워딩(`-L`/`-R`/`-D`) → v10+ |
| — | Homebrew tap · Scoop · WinGet → v10 |

---

## 확정된 결정 (임의로 뒤집지 말 것)

- **드래그 선택은 세션 패널에서만.** SFTP 모드의 드래그는 전송이고 그 의미는 바뀌지 않는다.
  두 모드는 `rightMode`로 이미 갈라져 있어서 충돌이 없다. 사이드바·폼 영역의 드래그도 무시한다.
- **선택 하이라이트는 셀을 뒤집었다 되돌린다** — `highlightCursor`가 이미 하는 그대로다.
  그림자 버퍼를 만들지 말 것. **`vt` 인스턴스가 화면 상태의 유일한 소유자**라는 v0 결정이
  선택에도 적용된다.
- **떼는 순간 복사된다.** 선택 후 `y`를 누르는 두 단계로 만들면 세션 포커스에서 `y`를 가로채야
  하고, 그건 "세션에서는 탈출키만 가로챈다"는 v0 규약을 깬다. X11 방식이 우리 제약과 맞는다.
- **스크롤하면 선택이 풀린다.** 뷰포트가 움직이면 선택이 가리키던 행이 다른 내용이 된다.
  좌표를 따라 옮기느니 푸는 쪽이 맞다(v1이 "리사이즈하면 offset을 0으로"를 고른 것과 같은 판단).
  리사이즈·탭 전환·`ESC c`(재연결)도 같은 이유로 선택을 지운다.
- **선택 중에는 화면이 스크롤되지 않는다.** 드래그가 패널 밖으로 나가도 자동 스크롤을 넣지 않는다 —
  offset이 움직이면 위 규칙에 따라 선택이 풀려야 하는데, 그러면 드래그 도중에 선택이 사라진다.
  패널 경계에서 좌표를 clamp하는 쪽이 맞다.
- **스크롤백 위에서도 똑같이 선택된다.** 선택 좌표는 **뷰포트 좌표**이고, 텍스트를 뽑을 때만
  `scrollOff`를 얹어 논리 행을 찾는다. 과거 줄용 별도 경로를 만들지 말 것 — `renderScrolled`가
  이미 만드는 논리 행 배열 하나를 선택 렌더와 텍스트 추출이 같이 쓴다.
- **대체화면(vim, less)에서도 선택은 된다.** 거기서는 `maxScrollOffset`이 0이라 라이브 화면만
  잡히지만, 드래그가 앱으로 새어 들어가지 않아야 한다(마우스 리포팅을 켠 원격 앱은 예외 —
  아래 참조).
- **원격 앱이 마우스를 켰으면 선택이 이긴다.** vim처럼 마우스 리포팅을 켠 원격 앱 위에서
  드래그하면 우리 선택이 먹고 원격에는 전달하지 않는다. 화면의 글자를 긁는 것은 사용자가
  터미널에 기대하는 기본 동작이고, 그걸 원격 앱에 넘기면 v9가 고치려는 회귀가 그대로 남는다.
  (그래서 원격 앱의 마우스 드래그를 쓰려면 그 앱의 키보드 경로를 쓰거나 v10의 수식키 옵션을
  기다려야 한다 — README에 적는다.)
- **클립보드는 쓰기만 한다.** 읽기(OSC 52 질의)를 넣지 않는다 — 많은 터미널이 보안상 거부하고,
  붙여넣기는 이미 bracketed paste가 `sendKey`의 `msg.Paste` 경로로 처리한다(v0부터 동작한다).
- **복사는 64 KiB에서 자른다.** OSC 52는 한 줄짜리 이스케이프라 거대한 선택은 터미널을 멈춰
  세운다. 잘렸으면 상태줄이 그렇게 말한다.
- **복사한 내용이 어디로 가는지 문서에 적는다.** OSC 52는 우리를 감싼 터미널(그리고 tmux·중첩
  ssh를 거친다면 그 경로 전부)로 나가고, tmux는 `set-clipboard on`이 없으면 버린다. 우리가
  검증할 수 없는 구간이므로 **비밀번호를 긁으면 그 경로로 나간다**는 사실을 README와 도움말에
  적는다. 우리 쪽에서 마스킹하는 척하지 않는다.
- **소프트랩된 줄을 이어붙이지 않는다.** vt가 wrap 여부를 알려주지 않으므로 아는 척하면 틀린
  곳에서 붙는다. 화면에 보이는 줄 그대로 `\n`으로 잇는다.

---

## 배경 — 기존 코드에서 반드시 재사용할 것
- `internal/ui/terminal.go:highlightCursor` — 셀을 바꿨다 되돌리는 선례. 선택 하이라이트가 같은 모양이다.
- `internal/ui/terminal.go:renderScrolled` — "위 `offset`줄은 스크롤백, 나머지는 라이브 화면"으로
  합성하는 지점. 선택도 **같은 갈림길을 한 번만 더 표현한다**(`selCell`) — 과거 줄용 두 번째
  경로를 만들지 말 것.
- `internal/ui/app.go:handleSFTPMouse`의 press/motion/release 3단계 — 세션 드래그도 같은 상태기계다.
  릴리스 이벤트가 버튼을 `MouseButtonNone`으로 보고할 수 있다는 v3의 교훈도 그대로 적용된다
  (**버튼 값이 아니라 "드래그 중이었는가"로 판단한다**).
- `sessionTab.scrollOff`(v4) — 선택 해제 트리거가 이 값이 바뀌는 모든 지점이다.
  새 스크롤 상태를 만들지 말 것.
- `internal/ui/app.go`의 `statusMessage` 우선순위 분기 — 복사 결과는 그 목록에 한 줄 추가다.
  `? help` 셀은 v7 규약대로 그대로 남는다.

## 의존성
**Go 의존성은 늘지 않는다.** `ansi.SetSystemClipboard`(`github.com/charmbracelet/x/ansi`)는
이미 직접 의존 중이다(v0.11.7).

---

## 구현

### 1. `internal/ui/tabs.go` — 선택 상태
```go
// sessionTab에 추가. 뷰포트 좌표다 — 스크롤하면 지운다.
type point struct{ x, y int }
type selection struct {
    anchor, cursor point
    active         bool // 버튼이 아직 눌려 있는 동안만
}

sel *selection   // nil = 선택 없음
```
`active`는 드래그가 진행 중이라는 뜻이고, **떼어도 선택은 화면에 남는다**(복사가 됐다는
유일한 시각적 증거다). 남은 선택을 지우는 것은 아래의 `clearSelection` 지점들이다.
탭마다 들고 있으므로 탭을 오갔다 오면 선택은 없다(전환 시 해제). `scrollOff`를 바꾸는 모든 곳
(휠, `resize`, 재연결 후 `ESC c`)에서 `t.sel = nil`을 같이 한다 — **해제 지점을 한 함수
(`clearSelection`)로 모은다.**

### 2. `internal/ui/app.go` — 마우스 3단계
`handleMouse`에 세션 분기를 추가한다. `rightMode == rightTerminal`이고 좌표가 세션 패널 본문
안일 때만:
- **press**: `sel = &selection{anchor: p, cursor: p}`
- **motion**: `sel.cursor = clamp(p)` — 패널 경계에서 자른다(자동 스크롤 없음)
- **release**: 앵커와 커서가 같은 셀이면 **선택을 지우고 끝**(클릭은 선택이 아니다),
  아니면 `selectedText()` → 복사

press는 v3까지의 클릭과 똑같이 **포커스도 옮긴다**(세션이 뒤에 있을 때만 — 그 조건은 v4
그대로다). 선택이 생겼다고 클릭의 기존 의미를 바꾸지 않는다.

모달(`modalUp()`)·도움말·잠금 화면이 떠 있으면 v7 규약대로 마우스가 아예 막힌다 —
새 예외를 만들지 말 것.

### 3. `internal/ui/terminal.go` — 렌더와 텍스트
```go
func renderSelected(emu *vt.Emulator, cols, rows, offset int, sel *selection, showCursor bool) string
func highlightSelection(emu *vt.Emulator, cols, rows, offset int, sel selection) func()
func selectedText(emu *vt.Emulator, cols, rows, offset int, sel *selection) string
func selRange(y int, from, to point, cols int) (startX, endX int)
func selCell(emu *vt.Emulator, offset, x, y int) (*uv.Cell, func(*uv.Cell))
```
`selCell`이 **"이 뷰포트 행이 스크롤백인가 라이브 화면인가"를 아는 유일한 지점**이고
(`renderScrolled`와 같은 갈림길), 하이라이트와 텍스트 추출이 둘 다 그것을 통해서만 셀에
닿는다. 스크롤백 셀은 `emu.ScrollbackCellAt`이 돌려주는 포인터를 제자리에서 바꾸고,
화면 셀은 `emu.SetCell`로 바꾼다 — 어느 쪽이든 렌더가 끝나면 되돌린다.
- `renderSelected`는 선택 구간의 셀만 `AttrReverse`로 뒤집고 `renderScrolled`를 부른 뒤
  되돌린다. 선택은 **리니어**(첫 줄은 앵커부터 끝까지,
  중간 줄은 통째로, 마지막 줄은 커서까지)고 열 블록이 아니다. 앵커가 커서보다 뒤면 정규화한다.
- `selectedText`는 같은 셀에서 ANSI를 뺀 평문을 뽑고, **줄 끝 공백을 제거**하고 `\n`으로
  잇는다. 2칸짜리 글자는 셀 하나로만 센다(경계에 걸리면 그 글자를 포함한다).
- **선택이 떠 있는 동안 커서는 그리지 않는다** — 같은 셀을 두 번 뒤집으면 원래대로
  돌아와서, 커서가 선택 안에 들어간 순간에만 사라지는 이상한 동작이 된다.

### 4. 클립보드 — 한 프레임짜리 프리픽스
`a.clip = ansi.SetSystemClipboard(text)`를 세우고 `clipFlushMsg`를 즉시 돌려준다.
`View()`가 그 프레임에 **프리픽스로 한 번** 내보내고, 다음 `Update(clipFlushMsg)`가 지운다.
이 왕복 때문에 시퀀스는 정확히 한 프레임에만 존재한다.
- **stdout에 직접 쓰지 않는다.** 렌더러가 그 파일 디스크립터를 소유하고 있어서 프레임 중간에
  끼어들면 화면이 깨진다. bubbletea v1에는 클립보드 커맨드가 없다(v2의 기능이다).
- OSC 52는 폭이 0이라 `padLine`·레이아웃 불변식과 충돌하지 않는다.
  `TestSelectionNeverBreaksLayout`이 그걸 고정한다.
- 64 KiB 초과분은 **인코딩 전에** 자른다(base64는 3/4 비율이라 자른 뒤가 아니라 자를 때
  원본 바이트로 재야 한다). 자를 때 UTF-8 경계를 지킨다.

### 5. 상태줄
`copied 3 lines` / 잘렸으면 `copied 64 KB (truncated)` / 공백만 긁었으면 `nothing to copy`
(클립보드는 건드리지 않는다 — 조용히 아무 일도 안 하는 것과 구별돼야 한다).
v7 규약대로 `statusMessage`의 우선순위 분기에 한 줄 추가일 뿐이고, 오른쪽 `? help` 셀은 그대로다.

### 6. `keymap.go` (그리고 `help.go`는 손대지 않는다)
`session.copy`를 **`Doc: true`·`Priority: 0`으로** 넣는다 — 드래그는 키가 아니지만 사용자는
배워야 하고, `sftp.drag`가 이미 같은 선례다. 문구는 그 바인딩의 `Desc` 하나이고
**도움말용 문자열 테이블을 새로 만들지 않는다**(v7의 이유 그대로). `Desc`는 두 칸 배치의
설명 폭(≈44)에 맞춘다 — 넘치면 카드에서 `…`로 잘려 오히려 덜 말하게 된다. OSC 52가
어디까지 나가는지는 README가 길게 적는다.
`Priority: 0`인 이유는 따로 있다: 세션 바인딩이 실리는 상태줄은 **사이드바 옆의 그 줄**이라,
세션 안에서만 되는 제스처를 거기 광고하면 "화면에 보이는 키는 누르면 반드시 동작해야
한다"를 어긴다. 덕분에 `TestWideStatusLineUnchanged`(v6 문장 그대로)도 손대지 않는다.

`help.go`는 **한 줄도 바뀌지 않는다** — 카드는 레지스트리를 그리는 뷰이므로 바인딩을 넣으면
그 자리에 나타난다. v7이 그렇게 되라고 만든 것이고, 이번이 그 증거다.

**기존 키는 하나도 바뀌지 않는다.** `TestDefaultsMatchV6`는 키→액션 조회 테스트라 `Doc: true`
추가에는 영향이 없고, **한 줄도 고치지 않고 통과해야 한다.**

### 7. 문서 갱신
`CLAUDE.md`: 현재 상태에 v9, 스크롤백 절에 "선택은 뷰포트 좌표이고 스크롤하면 풀린다",
확정된 설계 결정에 위 §확정 항목 요약.
`README.md`: 선택·복사 절과 **OSC 52가 어디까지 나가는지** + tmux `set-clipboard on` 요구사항,
그리고 원격 앱의 마우스보다 선택이 우선한다는 것.

---

## 변경 / 추가 파일
| 파일 | 내용 |
|---|---|
| `internal/ui/terminal.go` | `renderSelected`/`highlightSelection`/`selectedText`/`selRange`/`selCell` |
| `internal/ui/tabs.go` | `sessionTab.sel`, `clearSelection`(스크롤·리사이즈·전환·재연결) |
| `internal/ui/app.go` | 세션 드래그 3단계, `a.clip` 프리픽스와 `clipFlushMsg`, 상태줄 문구 |
| `internal/ui/keymap.go` | `session.copy` (`Doc: true`, `Priority: 0`) 한 줄 |
| `internal/ui/selection_test.go` | 신규 — v9의 테스트 전부 |
| `CLAUDE.md` · `README.md` · `docs/V9_plan.md` | 갱신 / 신규 |

**새 패키지도, 새 파일도 없다.** `internal/ssh`·`internal/sftp`·`internal/config`·`internal/model`은
한 줄도 바뀌지 않는다.

---

## 검증 (end-to-end)

**자동**
1. `go vet ./...`, `go build ./...`, `go test -race ./internal/...`,
   `GOOS=windows go vet ./...` (기존 그대로 통과).
2. `internal/ui` 선택:
   - `TestSelectionRendersReversed` — 선택 구간만 뒤집히고 나머지 셀은 그대로다.
   - `TestSelectionIsLinearNotBlock` — 세 줄 선택에서 중간 줄은 통째로 들어간다.
   - `TestSelectionNormalizesBackwardDrag` — 아래→위 드래그도 같은 텍스트다.
   - `TestClickWithoutDragDoesNotCopy` — 포커스만 옮기고 클립보드는 건드리지 않는다.
3. `internal/ui` 스크롤 연동:
   - `TestScrollClearsSelection` / `TestResizeClearsSelection` / `TestTabSwitchClearsSelection` /
     `TestKeyToSessionClearsSelection`.
   - `TestSelectionInScrollbackCopiesPastLines` — `scrollOff > 0`에서 뽑은 텍스트가
     스크롤백의 그 줄이다.
   - `TestNoAutoScrollWhileDragging` — 패널 밖으로 나간 드래그가 `scrollOff`를 바꾸지 않는다.
4. `internal/ui` 클립보드:
   - `TestCopyEmitsOSC52Once` — 그 프레임에만 있고 다음 프레임에는 없다.
   - `TestCopyTruncatesAt64KiB` — 잘린 길이와 룬 경계.
   - `TestCopyStatusCountsLines` — 상태줄이 몇 줄이 나갔는지 말한다.
   - `TestTrailingSpacesAreStripped`, `TestSoftWrappedLinesAreNotJoined`.
5. 불변식:
   - `TestSelectionNeverBreaksLayout` — 선택·복사 프레임에서도 모든 행이 정확히 width이고
     세로 예산이 그대로다.
   - `TestSelectionBlockedByModal` — 도움말·확인·잠금 화면이 떠 있으면 드래그가 시작되지 않는다.
   - `TestDefaultsMatchV6` · `TestHelpMatchesRealBindings` · `TestEveryActionIsDispatched`
     **한 줄도 고치지 않고** 통과.

**수동 확인 (v9 수용 기준 — 자동화하지 않음)**
1. 세션 화면에서 드래그 → 떼면 **호스트 클립보드**에 들어간다(터미널 직접, tmux
   `set-clipboard on`, ssh 중첩 세 경우 각각 확인 — 안 되는 조합이 있으면 README에 적는다).
2. 스크롤백으로 올라가 드래그하면 그 **과거 줄**이 복사된다. 드래그 중 휠을 굴리면 선택이 풀린다.
3. vim(대체화면)에서 드래그해도 화면이 깨지지 않고, vim의 마우스 모드가 켜져 있어도 우리 선택이 먹는다.
4. 드래그를 패널 밖(사이드바·상태줄)까지 끌고 나가도 선택이 패널 경계에서 멈추고 화면이 안 튄다.
5. 60줄쯤 되는 로그를 선택해도 화면이 멈추지 않고, 아주 긴 선택은 잘렸다고 알린다.
6. 선택한 채로 원격에서 출력이 계속 흘러도 화면이 깨지지 않는다(선택은 뷰포트 좌표라 내용이
   바뀔 뿐 좌표는 유지된다 — 라이브 화면이 스크롤되면 `scrollOff`가 아니라 내용이 움직인다).
7. 터미널 리사이즈·탭 전환·자동 재연결 뒤에 선택이 남아 있지 않다.

---

## v9에서 하지 않는 것 (의도적 제외)
- **ProxyJump(점프호스트)와 포트포워딩(`-L`/`-R`/`-D`)** → v10 이후. 전송 계층
  (`internal/ssh/Dial`)을 건드리는 별개의 축이고, 그 자체로 한 버전짜리 주제다.
- **더블·트리플 클릭 선택, 열 블록 선택, 키보드 yank 모드** → v10.
- **클립보드 읽기(OSC 52 질의)** — 붙여넣기는 bracketed paste가 이미 한다.
- **선택 자동 스크롤**(드래그가 패널 밖으로 나갔을 때 화면을 밀어 올리는 것) — "스크롤하면
  선택이 푼다"와 정면으로 충돌한다. 넣는다면 그 규칙부터 다시 정해야 한다.
- **원격 앱에 드래그를 넘기는 수식키 옵션**(shift 누르면 vim으로) → v10. 기본값이 무엇이어야
  하는지가 먼저다.
- **Homebrew tap · Scoop · WinGet** → v10 (별도 레포·외부 토큰. v8의 "CI는 시크릿을 요구하지
  않는다"를 깨는 첫 작업이므로 그 버전의 주제가 되어야 한다).
- **세션 로깅, 접속 후 자동 명령, 다중 탭 브로드캐스트 입력** — 각각 별도 축.
