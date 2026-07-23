# CLAUDE.md

TUI SSH 클라이언트. 좌측 사이드바에 서버 목록, 우측 패널에 **실제 SSH PTY 세션을 임베딩**해서 렌더한다.
사이드바에서 `f`를 누르면 우측이 Local | Remote 두 파일 패널로 갈라지는 **SFTP 모드**가 된다.
상세 설계와 범위는 `docs/V0_plan.md`(기반 구조)·`docs/V1_plan.md`(세션 안정화)·
`docs/V2_plan.md`(SFTP)·`docs/V3_plan.md`(SFTP 심화 — 재귀 전송·진행률·다중 선택·삭제/이름변경)·
`docs/V4_plan.md`(다중 세션 탭·keepalive·자동 재연결)·
`docs/V5_plan.md`(목록 UX — 검색/필터·그룹·`~/.ssh/config` import)·
`docs/V6_plan.md`(암호화 금고·ssh-agent·비공개 레포 동기화)·
`docs/V7_plan.md`(키맵 레지스트리·`?` 도움말·`keys.json`)·
`docs/V8_plan.md`(크로스 플랫폼 빌드·릴리스·한 줄 설치)·
`docs/V9_plan.md`(드래그 선택·OSC 52 복사·스크롤백 연동) 참조 —
구현 결정이 바뀌면 그 문서도 같이 갱신할 것.

## 현재 상태
v0 + v1 + v2 + v3 + v4 + v5 + v6 + v7 + v8 + v9 구현 완료.
`go build`/`go test -race`/`go vet` 모두 통과하고, 6타깃 크로스 컴파일과
`goreleaser release --snapshot --clean`도 통과한다.
아직 실제 sshd 상대로 한 수동 확인은 남아 있다 — 각 계획서의 "수동 확인" 절이 수용 기준이다
(v0: vim 같은 풀스크린 앱 갱신 / v1: TOFU 승인, 키 변경 거부, 스크롤백, 편집·삭제, 드래그 리사이즈 /
v2: 드래그 전송, 양방향 전송, 덮어쓰기 경고 /
v3: 100MB 전송 중 진행률·취소·부분 파일 정리, 디렉터리 드래그, 다중 선택, 삭제/이름변경 /
v4: 탭 여러 개 동시 유지, 백그라운드 탭 출력 누적, 네트워크 차단 후 자동 재연결 /
v5: 실제 `~/.ssh/config` import 후 접속, 접힘 상태 재시작 후 유지, 마우스 클릭 회귀 /
v6: 평문 설정 마이그레이션 후 실제 접속, `ssh-add`한 키로 agent 인증, 비공개 레포 push/pull,
public으로 바꾼 뒤 push 거절, 다른 기기 `--pull` 부트스트랩 /
v7: 세션 안에서 `?`가 셸로 들어가는지, 노란 경고가 뜬 상태에서 `? help`가 남는지,
`keys.json` 재바인딩 후 실제 동작 /
v8: 태그를 밀어 릴리스가 끝까지 도는지, `install.sh`가 깨진 체크섬에서 멈추는지,
Windows에서 named pipe agent 인증과 `%AppData%` 설정 경로 /
v9: 드래그 복사가 호스트 클립보드에 실제로 들어가는지 — 터미널 직접·tmux `set-clipboard on`·
중첩 ssh 세 경우, vim(대체화면) 위에서의 드래그, 스크롤백에서 긁은 과거 줄).

**자격증명은 `.gitignore`가 막는다.** v6부터 비밀번호와 우리가 보관하는 개인키는
`vault.age`(age scrypt) 안에만 있고 `servers.json`은 메타데이터뿐이지만, 둘 다 커밋 대상이
아니다 — 암호문이라도 오프라인 브루트포스의 재료이고 호스트 이름도 정보다. 정상 위치는
레포 밖(`~/.config/ssh-client/`)이지만 `XDG_CONFIG_HOME=.` 같은 실행 한 번이면 작업 디렉터리에
떨어진다. 그래서 `servers.json`/`vault.age`/`ssh-client.age`/`*.bak`/`ui.json`/`known_hosts`/
`keys/`/`*.pem`/`id_*`를 이름으로 무시한다. 이 항목들을 지우지 말 것.
(v7의 `keys.json`도 무시하지만 이유가 다르다 — 비밀이 아니라 **이 기기의 설정**이라서다.)

키 입력은 변환 테이블(`terminal.go:keyToVT`)로 `tea.KeyMsg` → `uv.KeyPressEvent`까지만 바꾸고,
ANSI 인코딩은 `emu.SendKey`에 맡긴다. 이 경로 덕에 application cursor keys 모드(DECCKM)가
자동으로 맞는다 — 직접 시퀀스를 만들지 말 것.

### 절대 건드리면 안 되는 것: `terminal.go:keyPump` (+ v4의 `termPool`)
인코딩된 바이트는 `emu.Read()`(io.Pipe)로 나오고 `keyPump`가 SSH stdin에 흘린다. 이 pump는
**항상 읽고 있어야 하고, 에뮬레이터는 절대 Close 하면 안 된다**:
- 에뮬레이터는 `ESC[6n` 같은 터미널 질의에 **`emu.Write` 안에서** 응답을 그 파이프에 쓴다.
  `emu.Write`는 UI goroutine(`Update`)에서 호출되므로, 읽는 쪽이 없으면 bash/vim이 질의를
  던지는 순간 **앱 전체가 데드락**한다. 그래서 pump는 세션 write가 실패해도 계속 읽는다.
- `vt.Emulator.Close()`는 블록된 `Read`를 깨우는 유일한 수단이지만 라이브러리 내부에서
  그 `Read`와 **data race**가 난다(`-race`로 재현됨). 그래서 에뮬레이터와 pump는 **절대
  닫지 않고**, 화면 초기화는 `resetEmulator`(ESC c)로, 대상 교체는 `pump.attach/detach`로 한다.
- v4에서 세션이 여러 개가 되면서 "프로세스에 하나"가 **`termPool`에 최대 `maxTabs`(8)개**로
  넓어졌다. 탭이 닫히면 슬롯(`termSlot` = emu + pump)을 **반납**하고 다음 탭이 재사용한다 —
  닫는 대신 돌려쓰기 때문에 탭을 몇 번을 열고 닫아도 pump goroutine은 8개를 넘지 않는다
  (`TestTabPoolRecyclesSlots`가 goroutine 수를 직접 센다). **슬롯을 새로 만들어 쓰고 버리는
  코드를 넣지 말 것.**

### 레이아웃 (세로 예산)
`row 0` 상단 마진 / `row 1..h-2` 사이드바·우측 패널(높이 동일) / `row h-1` 상태줄.
우측 패널은 사이드바의 "Servers"와 짝을 이루는 **자체 타이틀바**(`rightHeaderRows` = 제목+빈줄)를
갖는다. 터미널 모드일 때 여기에 접속 중인 세션 이름과 `user@host:port`가 뜬다.
탭이 2개 이상이면 **같은 제목 줄이 탭 스트립**이 된다(`tabStrip`) — 행을 추가하지 않는 것이
규칙이다. 폭이 모자라면 활성 탭을 중심으로 창을 잘라내고 `‹`/`›`를 붙이며, 마지막은 항상
`padLine`으로 정확히 맞춘다. 탭 1개일 때의 헤더는 v3와 동일해야 한다
(`TestSingleTabHeaderIsUnchanged`, `TestLayoutAlignmentWithTabs`가 고정).
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

### 사이드바는 항목당 1행이다 (v5)
v4까지는 bubbles 기본 delegate라 항목당 3행이었다. v5부터 `rowDelegate`(`Height()=1`,
`Spacing()=0`)로 바뀌었고 `sidebarRowsPerItem = 1`이다. 이유는 **그룹 헤더** 때문이다 —
`list.ItemDelegate.Height()`는 **모든 항목에 하나의 값**이라 가변 높이를 못 준다. 헤더를 3행으로
부풀리느니 전부 1행으로 줄였다(`filePane`이 이미 1행/항목이라 선례가 있다).
행 형식은 `● web-1        deploy@10.0.0.1` — 왼쪽 마커+이름, 오른쪽 흐린 detail,
**폭이 모자라면 detail부터 버린다**. 폭 계산은 스타일을 입히기 전에 끝내야 한다.
`rowToIndex`는 이제 rel을 그대로 인덱스로 쓰되 **렌더된 행을 센다** — 헤더도 항목이고 접힌
그룹의 서버는 아예 항목이 아니다. 그래서 `len(a.servers)`가 아니라
`a.sidebar.list.VisibleItems()`로 범위를 잡고 페이지 오프셋을 더한다
(`TestSidebarRowGeometry`, `TestSidebarClickHitsVisibleRow`가 고정).

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
v7의 도움말 카드가 이 `overlay`의 **첫 범용 사용**이다(SFTP 모드 밖에서도 뜬다). 새 합성
함수를 만들지 말 것 — 박스 배치는 `modalBox`, 합성은 `overlay` 하나로 모여 있다.

### 키는 `keymap.go`에만 쓰여 있다 (v7)
v0의 키 6개가 40개를 넘었는데 목록이 어디에도 없었다. 원인은 하나였다 — 키 문자열이
`handleKey`/`handleSFTPKey`/`handleImportKey`/`handleSyncKey`/`handleUnlockKey`/`tabKey`의
`switch msg.String()` 리터럴로만 존재하고, 상태줄 문구는 그 switch와 아무 연결이 없어서
한쪽만 고치면 조용히 거짓말이 됐다는 것. v7은 레지스트리 하나(`Context`/`Action`/`Binding`)를
만들고 **라우팅·상태줄·도움말·`keys.json`이 전부 거기서 나오게** 했다.
- 핸들러는 `switch a.keys.Action(ctxX, msg.String())`로 갈라진다. **키 리터럴을 다시 쓰지 말 것.**
  기본 바인딩은 v6과 문자 하나까지 같고 `TestDefaultsMatchV6`가 표 전체를 고정한다.
- **`Action`에는 전역 폴백이 없다.** `q`는 사이드바에서 종료지만 파일 패널에서는 아무것도 아니다 —
  그 차이가 v6의 동작이라 `ctxGlobal`은 필요한 자리에서 **명시적으로** 조회한다.
- 도움말은 레지스트리를 그리는 뷰다(`help.go`). **도움말용 문자열 테이블을 만들지 말 것** —
  그게 상태줄이 낡은 이유다. `TestHelpMatchesRealBindings`가 카드에 뜬 키가 실제로 그 액션으로
  풀리는지 확인하고, `TestEveryActionIsDispatched`가 선언만 되고 아무도 처리하지 않는 액션을 잡는다.
- `Doc: true`는 **여기 적혀 있지만 여기서 라우팅되지 않는** 바인딩이다(연결 폼은 `msg.Type`을
  필드 상태와 함께 보고, 필터·커서는 bubbles list의 것이며, 드래그는 키가 아니다). 재바인딩 대상에서도 빠진다.
- `alt+1..9`는 키 문자열을 파싱하지 않고 **바인딩 안에서의 위치**(`KeyIndex`)로 몇 번째 탭인지 정한다.

### 도움말(`?`)은 모달이고, 세션에는 없다
`App.help != nil`이면 `handleKey` 맨 앞에서 모든 키를 먹고(모르는 키는 **닫기만** 한다),
마우스도 막힌다. `confirm`/`pending`/`rename`/`sftpErr`/`unlock`이 떠 있으면 **열리지 않는다**
(모달 위에 모달을 쌓지 않는다 — `modalUp()`이 그 판정 한 곳이다).
**세션 포커스에는 도움말 키가 없다** — `?`는 vim·bash가 쓰는 평범한 문자이고, "세션에서는
탈출키만 가로챈다"는 v0 규약을 깨지 않는다. `f1`도 예외로 만들지 말 것.
카드는 아무 상태도 바꾸지 않으므로 닫으면 포커스·모드·커서가 그대로다(`TestHelpRestoresState`).
검색(`/`)은 v5 필터와 같은 규칙 — 부분 문자열, 원래 순서, 랭킹 없음.

### 상태줄은 `[메시지 … 도움말 셀]` 두 구역이다 (v7)
v6까지 `statusLine()`은 `switch` 하나라 전송·드래그·경고·에러·`status`가 뜨면 힌트가 통째로
사라졌다 — **노란 글이 뜬 화면에서 단축키 안내가 없어지는** 게 이것 때문이었다.
- 왼쪽 메시지는 그 우선순위 분기 그대로(`statusMessage`), 오른쪽 끝에 `? help`가 **고정**된다.
  자리가 모자라면 **메시지를 자른다**(`…`). 도움말 셀은 마지막까지 남는다
  (`TestHelpCellSurvivesWarnings`). 폭이 `셀+12`도 안 되는 극단에서만 셀을 버린다.
- 셀은 **지금 열 수 있는 도움말만** 광고한다: 일반 화면 `? help`, 세션 `ctrl+b ? help`,
  모달·폼·필터 중에는 **아무것도**. 화면에 보이는 키는 누르면 반드시 동작해야 한다.
- 상태줄에 맞춰 크기를 정하는 것(힌트, 진행률 바)은 `a.width`가 아니라 **`statusRoom()`**을 쓴다 —
  안 그러면 넘치게 만들어 놓고 잘리게 된다.
- 힌트 문구는 `hintFor(ctx...)`가 바인딩의 `Short`/`Priority`로 조립한다. 폭이 모자라면
  **우선순위가 낮은 것부터** 빠진다. 필터 입력 중의 한 줄만 여전히 손으로 쓴 문장이다
  (필터는 bubbles의 입력이지 우리 바인딩이 아니다).

### `keys.json`은 `ui.json`과 정반대의 파일이다
둘 다 `~/.config/ssh-client/`에 있지만 약속이 다르다. `ui.json`은 지워도 되는 뷰 찌꺼기라
깨지면 **조용히 zero value**를 준다. `keys.json`은 사용자가 의도해서 쓴 파일이라
**깨지면 에러로 돌려주고**(`config.LoadKeys`) 상태줄이 알리며 상세는 도움말 카드 아래에 남는다 —
조용히 무시하면 "재바인딩이 안 먹는 버그"와 구별되지 않는다.
- 형식은 `액션 ID → 키 배열` 평평한 맵. 빈 배열은 **해제**다.
- 문제가 있는 **항목만** 기본값으로 되돌리고 나머지는 적용한다(`Keymap.Apply`).
  같은 컨텍스트에서 키가 겹치면 **양쪽 다** 기본값으로 — 먼저 쓴 쪽이 이기면 JSON 객체의
  순서에 의미가 생긴다.
- **`session.escape`와 `global.help`는 키가 없어질 수 없다**(비면 기본값 복구). 하나는 세션에서
  나오는 유일한 길이고, 하나는 나머지를 찾는 유일한 길이다.

## 개발 명령
```bash
go build ./...
go test ./internal/...   # 금고 때문에 느리다(scrypt) — 30초쯤 걸린다
go run .            # TUI라 비대화형으로 실행하면 화면이 깨진 채로 멈춘 것처럼 보인다

# 새 PC 부트스트랩 (v6). 토큰은 금고 → GITHUB_TOKEN → `gh auth token` 순으로 찾는다.
GITHUB_TOKEN=… go run . --pull --repo owner/name

# 현재 키맵 덤프 (v7). TUI를 띄우지 않는다 — keys.json 편집의 출발점.
go run . --keys          # 사람이 읽는 표
go run . --keys=json     # keys.json 형식 그대로

# 버전 (v8). --keys와 같은 규약 — 읽고 찍고 끝난다.
go run . --version       # go run에는 vcs 스탬프가 없어 "dev"만 나온다; go build한 것으로 확인할 것

# 크로스 컴파일과 릴리스 리허설 (v8). goreleaser는 dist/에만 쓰고 .gitignore가 막는다.
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows go vet ./...          # 빌드 태그로 갈라진 테스트가 windows에서도 컴파일되는지
goreleaser release --snapshot --clean   # 태그 없이 아카이브 6개 + checksums.txt
```

### 스크롤백은 탭의 `scrollOff` 하나로만 표현한다
과거 화면 버퍼를 따로 만들지 말 것 — vt 에뮬레이터가 이미 스크롤백을 갖고 있고,
(v4부터 offset은 `App`이 아니라 `sessionTab`이 들고 있어서 탭을 오갔다 와도 보던 자리가 남는다.)
`renderScrolled`가 "위 `offset`줄은 스크롤백, 나머지는 라이브 화면"으로 합성한다.
새 출력이 와도 offset은 유지된다(읽는 중 화면이 튀면 안 된다). 아무 키나 누르면 0으로 복귀하고,
`resize`도 0으로 되돌린다(리플로우된 과거 줄과 옛 offset은 맞지 않는다).
**대체화면(vim, less)에서는 `maxScrollOffset`이 0이고**, 휠은 `altScreenScroll`로 화살표 키가 된다.

v9의 선택(`sessionTab.sel`)도 같은 축 위에 있다 — 좌표는 **스크롤백 좌표가 아니라 뷰포트
좌표**이고, 텍스트를 뽑을 때만 `scrollOff`를 얹어 `selCell`이 "위 offset줄은 스크롤백,
나머지는 라이브 화면"으로 되짚는다(`renderScrolled`와 같은 갈림길, 같은 규칙).
그래서 offset이 움직이면 선택이 가리키던 행이 다른 내용이 되므로 **푼다** —
해제 지점은 `clearSelection` 하나이고 `scrollBy`·`resize`·`switchTo`·재연결·세션에 들어가는
아무 키가 전부 그것을 부른다. 드래그가 패널 밖으로 나가도 **자동 스크롤하지 않는다**
(offset을 움직이면 방금 만든 선택을 스스로 지우게 된다 — 경계에서 clamp한다).

## 확정된 설계 결정 (임의로 뒤집지 말 것)
- **Go + Bubble Tea**. SSH는 `golang.org/x/crypto/ssh`로 직접 연결하며, `ssh` 바이너리를 exec 하지 않는다.
- 세션은 **전체화면 핸드오프가 아니라 우측 패널 임베딩**. SSH stdout 바이트를 `github.com/charmbracelet/x/vt` 가상 터미널에 먹이고, 그 셀 그리드를 매 프레임 우측 패널 크기로 렌더한다.
- 인증은 password / key / **agent** 셋. 폼에 붙여넣은 키 본문은 v6부터 **금고 안**(`Secrets.Keys[serverID]`)에 살고 `keys/<id>.pem`을 새로 만들지 않는다.
- `servers.json`은 **메타데이터 전용**이다(`${XDG_CONFIG_HOME:-~/.config}/ssh-client/`). `Server.Password`·`KeyPEM`·`KeyPassphrase`는 `json:"-"`라 직렬화 경로 자체가 없고, 값은 연결 직전 `config.Inject`가 금고에서 채운다.
- **호스트키는 항상 검증한다.** `InsecureIgnoreHostKey`는 v1에서 없어졌고 다시 넣지 말 것.
  읽기는 `~/.ssh/known_hosts` + 우리 파일, **쓰기는 우리 파일에만**. 키가 바뀐 경우에는
  **승인 단축키를 만들지 않는다** — 그게 정확히 MITM이 보이는 모습이라 한 키로 넘길 수 있으면
  검증이 무의미해진다. 사용자가 known_hosts를 직접 고쳐야 하고, 에러 카드가 그 파일·줄 번호를 알려준다.
- **연결 실패는 `internal/ssh/errors.go`의 센티널로 분류**하고 UI는 `errors.Is`로만 갈라진다.
  에러 문자열 매칭 금지(`errorAdvice`가 유일한 문구 매핑 지점, `TestErrorCardOffersActions`가 고정).
- **SFTP 연결은 터미널 세션과 별개의 TCP 연결**이다. 한쪽을 끊어도 다른 쪽은 산다 —
  `teardownSession`과 `teardownSFTP`는 서로를 부르지 않고, `gen`/`sftpGen`도 각자 센다.
- 전송은 **한 번에 하나**다(`App.transfer != nil`이 곧 "전송 중"). 큐·병렬·재개는 v3에도 없다.
- **세션은 탭 여러 개**(`App.tabs`, 최대 `maxTabs`=8). 안 보이는 탭도 계속 출력을 받는다 —
  메시지는 `tabByGen(msg.gen)`으로 그 탭의 에뮬레이터에 들어간다. 별도 버퍼를 만들지 말 것.
  `enter`는 이미 열린 서버면 그 탭으로 **전환**하고, 같은 서버에 세션을 하나 더 열려면 `n`.
- **끊김은 keepalive로 감지한다**(`ssh.KeepaliveInterval` 30s, `Options.Keepalive`로 주입 가능).
  응답이 한 주기 안에 없으면 `ErrConnectionLost`로 세션을 끝낸다 — 없으면 죽은 TCP 위에서
  읽기 goroutine이 영원히 조용히 기다린다.
- **자동 재연결은 `ErrConnectionLost`에만.** 정상 종료(`exit`)는 탭을 닫는다. 아니면 `exit`
  한 번이 무한 재접속이 된다. 백오프는 1·2·4·8·16·30초(상한 30s, 횟수 제한 없음),
  `r`로 즉시 재시도. **재연결은 새 셸이다** — 화면은 성공 시점에 `ESC c`로 지우고, 그 전까지는
  마지막 화면을 그대로 둔다(끊긴 순간 뭘 하고 있었는지 읽을 수 있어야 한다).
- **자동 재연결에는 호스트키 프롬프트를 띄우지 않는다**(`reconnect(auto=true)`가 `Prompts`에
  nil을 넘긴다). 사용자가 안 보는 사이에 새 호스트키를 승인시키지 않기 위해서다 —
  그 실패(`ErrHostKeyUnknown`)만은 백오프를 멈추고 탭을 세워 둔 채 `r`을 기다린다.
- **진행률은 틱 기반**이다. 전송 goroutine은 `sftp.Progress`의 atomic 카운터만 갱신하고,
  UI는 `tea.Tick(progressInterval)`로 다시 그릴 뿐이다 — 틱 핸들러는 상태를 바꾸지 않는다.
  청크마다 메시지를 보내면 초당 수천 개가 되므로 채널·콜백을 만들지 말 것.
- **취소는 `context`**로 청크 루프를 끊고 **목적지 파일을 지운다**(`copyCtx` → `Upload`/`Download`).
  잘린 파일을 남기지 않는다. 반대로 **재귀 전송은 롤백하지 않는다** — 절반 지워진 원격
  디렉터리가 절반 복사된 디렉터리보다 위험하다.
- **심링크는 따라가지 않고 건너뛴다**(`Plan`). `a/b -> a` 순환이 무한 재귀가 되기 때문에
  `Browser.Stat`은 양쪽 모두 lstat 계열(`os.Lstat` / `sc.Lstat`)이어야 한다.
- `sftp.ErrIsDir`는 이제 **사용자에게 보이는 거부가 아니라** 단일 파일 API에 디렉터리를 넘긴
  내부 오류다. 재귀는 `Plan` → `RunSet`이 담당한다.
- **그룹은 `Server.Group` 문자열 하나다**(한 단계, 트리 없음). 그룹 테이블·ID를 만들지 말 것 —
  그룹은 "같은 문자열을 가진 서버들"일 뿐이라 이름을 바꾸면 그 자리에서 옮겨지고, 마지막
  서버가 나가면 저절로 사라진다. `prod/web`의 슬래시도 그냥 이름의 일부다(트리는 v7).
  그룹 없는 서버가 맨 위, 그룹은 그 아래 이름순. **그룹이 하나도 없으면 화면은 v4와 같다**
  (헤더 행이 0개 — `TestNoGroupsLooksLikeV4`가 고정).
- **`ui.json`은 지워도 되는 파일이다.** 접힘 상태·정렬 옵션만 들어가고,
  `LoadUIState`는 없거나 깨졌으면 **조용히 zero value**를 준다(에러 아님). 저장 실패도 삼킨다 —
  뷰 잔여물 때문에 에러 카드를 띄우지 않는다. 사용자가 손으로 고치는 데이터(`servers.json`)와
  UI 찌꺼기를 같은 파일에 섞지 말 것.
- **필터는 부분 문자열 하나로만 매칭한다.** `list.Filter = containsFilter`로 bubbles 기본
  퍼지 랭킹을 대체했다 — 랭킹이 있으면 `prod`가 `podman-host`를 위로 끌어올려 순서가 흔들린다.
  매칭 대상은 `Server.FilterKey()`(이름·user·host·그룹을 소문자로 이어 붙인 것) 하나뿐이고,
  **결과 순서는 원래 목록 순서**다. 그룹 헤더는 `FilterValue()`가 `"\x00"`을 돌려줘서 검색
  중에는 절대 안 걸린다(검색 결과는 평평한 목록이어야 한다).
- **`~/.ssh/config` 파서는 직접 쓴다**(`internal/config/sshconfig.go`, 새 의존성 없음).
  읽는 키워드는 `Host`/`HostName`/`User`/`Port`/`IdentityFile` **넷뿐**이고,
  `Include`·`Match`·와일드카드(`Host *`, `Host web-?`)는 **이유를 붙여 skip 항목으로 남긴다** —
  지원하는 척하면 "OpenSSH에서는 붙는데 여기서는 안 붙는" 조용한 불일치가 생긴다.
  한 블록을 건너뛰어도 **나머지는 계속 파싱한다**(`TestWildcardAndIncludeAreSkipped`).
- **import는 절대 자동으로 돌지 않는다.** 시작할 때 몰래 읽어 목록을 채우지 않는다 —
  사이드바 `i` → 미리보기 → `space`로 선택 → `enter`. 중복(같은 `user@host:port`)은 기본
  해제에 `dup` 배지. 저장은 `store.AddAll`로 **한 번만** 쓴다(항목마다 Save 하면 N번 쓴다).
- **IdentityFile은 복사하지 않는다.** 경로만 `KeyPath`에 적는다 — 그 파일은 사용자와 OpenSSH의
  것이고, 우리가 사본을 만들면 원본이 교체됐을 때 조용히 옛 키를 쓰게 된다
  (`TestImportKeepsIdentityPath`가 `keys/`가 비어 있는지 확인한다).
  import한 서버는 **그냥 서버다** — "ssh_config에서 왔음" 플래그로 동기화하지 않는다.
- **암호 프리미티브를 직접 만들지 말 것.** 금고는 `filippo.io/age`의 scrypt recipient 하나만
  쓴다(`internal/vault`, work factor 19). Argon2id + XChaCha20을 손으로 조립하면 KDF 파라미터와
  nonce 관리를 우리가 떠안게 된다. `vault`는 **바이트만 다루고 경로도 파일도 모른다**.
- **패스프레이즈는 어디에도 저장하지 않는다.** `App.pass`는 프로세스 수명뿐이고 "이 기기에서
  기억하기" 옵션을 만들지 말 것 — 그 옵션이 있으면 금고는 평문 파일에 그린 자물쇠가 된다.
- **비밀이 없으면 패스프레이즈를 묻지 않는다.** 잠금 화면은 금고가 **있을 때**(`HasVault`),
  또는 평문 설정이 발견됐을 때만 뜬다. 금고 생성은 첫 비밀을 저장하는 순간
  (`requireVault`)이지 그 전이 아니다 — key/agent만 쓰는 사용자는 이 함수를 통과만 하고
  한 번도 질문받지 않는다(`TestNoVaultNoPrompt`).
- **잠금 화면은 모달 규약의 가장 강한 형태다.** `App.unlock != nil`이면 `handleKey` 맨 앞에서
  **모든 키**를 먹고(`q`·`ctrl+b`·`alt+숫자` 포함), 마우스도 `Update`에서 막히며, `View`는
  프레임 대신 `unlockView()`를 통째로 그린다 — 뒤에 서버 이름이 비치면 암호화한 의미가 없다
  (`TestUnlockSwallowsEverything`). 복호화는 scrypt라 **반드시 `tea.Cmd`**다.
  3회 틀리면 종료한다(`TestWrongPassphraseThriceQuits`).
- **`OwnsKey`가 마이그레이션의 경계다.** 평문 이관은 `keys/` 안의 pem만 금고로 옮기고
  `~/.ssh/id_ed25519`를 가리키는 `KeyPath`는 손대지 않는다 — 사본을 만들면 원본이 교체됐을 때
  조용히 옛 키를 쓰게 된다. 순서도 뒤집지 말 것: **금고 저장이 성공한 뒤에** 원본을 지운다
  (`migrate.go`, 중간에 죽으면 비밀이 사라진다). 백업 `servers.json.plaintext.bak`(0600)을
  남기고 상태줄이 경로를 알려준다. 파일 삭제가 물리적 소거가 아니라는 점은 우리가 풀 수 없다.
- **agent는 조용히 폴백하지 않는다.** `AuthAgent`인데 `SSH_AUTH_SOCK`이 없으면
  `ErrAgentUnavailable`로 끝난다 — 엔트리에 비밀번호가 남아 있어도 쓰지 않는다. 어떤
  자격증명으로 열렸는지 알 수 없게 되는 게 더 나쁘다(`TestMissingAgentIsErrAgentUnavailable`).
  agent 소켓은 **핸드셰이크 동안만** 열려 있다(`authMethods`가 `io.Closer`를 같이 돌려주고
  `Dial`이 defer로 닫는다) — 서명은 agent가 하므로 그 전에 닫으면 안 된다.
- **잠긴 키는 에러 카드가 아니라 질문이다.** `ErrKeyPassphraseRequired`면 한 줄 입력
  (`App.keyPass`)을 띄우고, 답은 금고의 `KeyPass[serverID]`에 넣어 **키마다 한 번만** 묻는다.
- **동기화는 옵트인이다.** `Secrets.GitHub == nil`이면 `internal/sync`는 **한 줄도 실행되지
  않는다** — 시작할 때 원격을 확인하지 않고, `S`/`P`는 "not set up" 상태줄만 남긴다
  (`TestSyncDisabledMakesNoRequests`). 자동 동기화·백그라운드 폴링을 넣지 말 것.
- **public 레포에는 절대 push하지 않는다.** 등록 시점과 **매 push 직전에**
  `GET /repos/{o}/{r}`의 `private`를 확인한다(`pushCmd`가 `Check`를 다시 부르는 건 중복이
  아니다 — 레포는 등록 뒤에 공개로 바뀔 수 있다). 실패는 `ErrRepoPublic`.
- **원격에 올라가는 것은 `ssh-client.age` 파일 하나**다. 번들(`servers.json` + 비밀 +
  `known_hosts`)을 통째로 암호화하므로 diff는 못 보지만, 토큰이 새도 **호스트 이름 하나**
  드러나지 않는다(`TestPushUploadsCiphertextOnly`).
- **토큰은 로그·에러 문자열에 절대 넣지 않는다.** `internal/sync`의 에러는 **상태코드로만**
  분류한다(401/403 → `ErrBadToken`, 404 → `ErrRepoNotFound`, 409/422 → `ErrSyncConflict`).
  `TestTokenNeverAppearsInError`가 모든 실패 경로를 훑는다.
- **충돌은 병합하지 않는다.** blob `sha`로 낙관적 잠금을 걸고, 어긋나면 `ErrSyncConflict` →
  `remote is newer — pull first (P)`. 접속정보를 잘못 합치느니 멈춘다.
- **`known_hosts`만은 합집합 병합한다.** pull은 서버 목록·비밀을 통째로 교체하지만 host key는
  로컬 + 원격의 합집합이다 — 통째로 덮으면 이 기기에서만 승인한 호스트가 사라지고 다음
  접속에서 TOFU 프롬프트가 다시 뜬다(사용자를 승인에 무뎌지게 만드는 건 보안 회귀다).
  같은 호스트에 **다른 키**면 **로컬을 유지**하고 `ApplyReport.Conflicts`로 화면에 알린다
  (`TestConflictingHostKeyKeepsLocal`) — 그게 정확히 MITM이 보이는 모습이라 조용히 고르면 안 된다.
- **pull은 확인 패널을 거친다.** 적용 전 `ssh-client.local.bak`을 남기고, 열린 세션 탭은
  건드리지 않는다 — 연결은 이미 맺어졌고 목록이 바뀐 것과는 무관하다
  (`TestPullAsksBeforeReplacing`, `TestPullKeepsOpenTabs`).
- **`LastUsed`는 세션이 실제로 열렸을 때만 찍는다**(`markUsed`, `connectedMsg`에서). 연결 실패는
  어느 호스트로 일하는지에 대해 아무 말도 하지 않는다. 폼은 이 값을 `form.lastUsed`로 그대로
  통과시킨다 — `store.Update`가 엔트리 전체를 교체하므로 입력칸 없는 필드는 편집마다 지워진다.
- **배포물은 단일 정적 바이너리 하나다**(v8). `CGO_ENABLED=0` + `-trimpath`,
  `goos: [linux, darwin, windows]` × `goarch: [amd64, arm64]` 6타깃. 에셋을 같이 깔지 않는다 —
  설정은 첫 실행에 생기고, 없으면 없는 대로 뜬다.
- **버전의 출처는 git 태그 하나다.** goreleaser가 `-X main.version=…`으로 주입하고,
  ldflags 없이 `go install`로 깐 바이너리는 `debug.ReadBuildInfo()`의 vcs 정보로 대체한다
  (`buildVersion()`). 버전 상수를 소스에 적어 두고 릴리스마다 손으로 고치지 말 것 — 반드시 어긋난다.
  **릴리스 트리거는 `v*` 태그 push 하나뿐**이고 `workflow_dispatch`를 넣지 않는다
  ("어느 커밋이 v1.2.0인가"의 답이 둘이 되면 안 된다). `release.yml`은 goreleaser 앞에서
  `go vet` + `go test`를 **다시** 돈다 — 릴리스는 별도 워크플로다.
- **`install.sh`는 반드시 체크섬을 검증하고, 건너뛰는 플래그가 없다.** `curl | sh`를 권하는 이상
  무결성 확인은 타협 대상이 아니다. `sha256sum`/`shasum`이 **둘 다 없으면 설치를 중단한다.**
  그리고 **sudo를 스스로 부르지 않는다** — 기본 위치는 `~/.local/bin`이고 PATH 추가는 안내만
  한다. 파이프로 받은 스크립트가 root를 요구하는 순간 사용자가 확인할 수 없는 권한을 얻는다.
- **Windows agent는 플랫폼 관례를 따르되 폴백하지 않는다.** `SSH_AUTH_SOCK`이 있으면 그 이름을,
  없으면 `\\.\pipe\openssh-ssh-agent`를 연다. 둘 다 안 되면 `ErrAgentUnavailable`이다 —
  같은 agent를 그 OS의 방식으로 찾는 것과, 다른 자격증명으로 넘어가는 것은 다르다.
  `agent.NewClient`는 `io.ReadWriter`만 요구하므로 `os.OpenFile`이면 충분하다 —
  **`Microsoft/go-winio` 같은 의존성을 들이지 말 것**(v8은 Go 의존성을 하나도 늘리지 않았다).
- **유닉스 설정 경로는 한 글자도 바뀌지 않았다.** `os.UserConfigDir()`로 통일하면 macOS가
  `~/Library/Application Support/`로 옮겨가 기존 사용자의 목록과 금고가 사라진 것처럼 보인다.
  `Default()`는 Windows일 때만 `%AppData%`로 갈라지고, `XDG_CONFIG_HOME`은 **어느 OS에서든**
  이긴다 — 테스트 전체가 그 변수에 의존한다(`TestDefaultPathUnchanged`, `TestXDGWinsOnEveryOS`).
- **Windows에서 파일 권한 0600을 흉내 내지 않는다.** ACL을 손으로 만들면 지킬 수 없는 약속이
  하나 생긴다. 그 플랫폼의 보호는 **금고가 암호문이라는 사실**에서만 나오고, README가 그 차이를
  명시한다.
- **세션 패널의 드래그는 선택이고, 떼는 순간 복사된다**(v9). 선택 후 `y` 같은 두 번째 단계를
  만들면 세션 포커스에서 그 키를 가로채야 하고, 그건 "세션에서는 탈출키만 가로챈다"는 v0
  규약을 깬다. SFTP 모드의 드래그(전송)와는 `rightMode`로 이미 갈라져 있어 충돌하지 않는다.
  선택 하이라이트는 `highlightSelection`이 `highlightCursor`처럼 **셀을 뒤집었다 되돌린다** —
  그림자 버퍼를 만들지 말 것(`vt`가 화면 상태의 유일한 소유자라는 v0 결정이 선택에도 적용된다).
  선택이 떠 있는 동안 커서는 그리지 않는다(같은 셀을 두 번 뒤집으면 되돌아온다).
- **클립보드는 쓰기만 한다.** `ansi.SetSystemClipboard`를 `App.clip`에 세우고 `View`가
  **프리픽스로** 내보낸 뒤 `clipFlushMsg`가 지운다. **한 프레임만 유지하면 안 된다** —
  bubbletea v1의 표준 렌더러는 `write()`가 버퍼를 리셋해 **가장 최근 프레임 하나만** 들고
  60fps 티커로 그리므로, 16ms 안에 만들어졌다 교체된 프레임은 **화면에 아예 안 나간다**.
  그래서 `clipHold`(150ms) 동안 붙여 두고, 그 사이 안 바뀐 줄은 렌더러가 건너뛰므로
  시퀀스는 결국 한 번만 나간다. **stdout에 직접 쓰지 말 것**(렌더러가 그 fd를 소유한다. bubbletea v1에는
  클립보드 커맨드가 없다). 읽기(OSC 52 질의)는 넣지 않는다 — 붙여넣기는 bracketed paste가
  `sendKey`의 `msg.Paste` 경로로 이미 한다. 복사는 **64 KiB에서 룬 경계를 지켜 자르고**
  (`truncateClip`) 상태줄이 그렇게 말한다. OSC 52는 폭이 0이라 `padLine`·레이아웃 불변식과
  충돌하지 않는다(`TestSelectionNeverBreaksLayout`).
- **소프트랩된 줄을 이어붙이지 않는다.** vt가 wrap 여부를 알려주지 않으므로 아는 척하면 틀린
  곳에서 붙는다. 화면에 보이는 줄 그대로 `\n`으로 잇고, 줄 끝 공백만 지운다.
- **앱 안 자동 업데이트·새 버전 알림을 만들지 말 것.** SSH 클라이언트가 시작할 때 네트워크로
  나가는 것은 v6의 "동기화는 옵트인"과 정면으로 충돌한다.

## 아키텍처

```
main.go                    플래그(`--pull`/`--repo`/`--path`/`--keys`/`--version`) → 부트스트랩 → tea.NewProgram(...)
                           version/commit/date = goreleaser ldflags, buildVersion()이 vcs로 폴백
└─ internal/ui/app.go      루트 model — 레이아웃/포커스/모드 상태머신, 키 라우팅, 금고 상태,
   │                       statusLine = [메시지 … 도움말 셀], hintFor/statusRoom,
   │                       handleSessionMouse(드래그 3단계)·copySelection·clip 프리픽스
   ├─ keymap.go            **키의 유일한 출처** — Context/Action/Binding, DefaultKeymap,
   │                       Apply(keys.json 덮어쓰기·문제 보고), Dump/DumpJSON
   ├─ help.go              `?` 도움말 카드 (overlay로 떠 있음, 컨텍스트별 섹션·검색·스크롤)
   ├─ unlock.go            잠금/생성 화면(모든 키를 먹는다), 마이그레이션 게이트,
   │                       키 패스프레이즈 질문, persistSecrets
   ├─ sync.go              동기화 등록 폼, push/pull 커맨드, syncAdvice(센티널→문구)
   ├─ tabs.go              세션 탭(sessionTab/tabState), gen→탭 라우팅, 탭 스트립, 백오프 재연결,
   │                       selection/point·clearSelection (뷰포트 좌표 선택)
   ├─ sidebar.go           좌측 서버 리스트 (bubbles/list) ── 고정 폭 30, 열린 서버엔 ●,
   │                       rowDelegate(1행/항목), 그룹 헤더·접힘, containsFilter, 최근순 정렬
   ├─ importer.go          ~/.ssh/config import 미리보기 (선택·dup 배지·skip 사유)
   ├─ form.go              우측 연결정보 입력 폼 (textinput/textarea), 신규·편집 겸용
   ├─ confirm.go           우측 패널 본문을 대체하는 공용 확인 패널 (호스트키·삭제·전송)
   ├─ errorcard.go         센티널 에러 → 안내 문구·액션 (errorAdvice)
   ├─ sftp.go              filePane(1행/항목·다중 선택), 드래그 3단계, SFTP 키 라우팅,
   │                       3-패널 렌더, 확인 문구, 진행률 바, 이름변경 입력
   └─ terminal.go          우측 임베디드 터미널 뷰 (x/vt 셀 그리드 → string), 스크롤백 합성,
                           renderSelected/highlightSelection/selectedText/selCell (드래그 선택),
                           termSlot/termPool (에뮬레이터+pump 재활용)
internal/config/store.go   servers.json + keys/ + known_hosts 관리, AddAll(일괄 추가)
internal/config/vaultstore.go vault.age — VaultPath/HasVault/LoadSecrets/SaveSecrets(원자적·0600)
internal/config/bundle.go  Bundle/ApplyBundle/ApplyReport — known_hosts 합집합, 로컬 백업
internal/config/migrate.go ScanPlaintext/Migrate(평문→금고, OwnsKey 경계), Inject(연결 직전 주입)
internal/config/uistate.go ui.json — 접힘 상태·정렬 옵션 (없거나 깨지면 zero value)
internal/config/keymap.go  keys.json — 액션ID→키 (없으면 빈 맵, **깨지면 에러**: uistate와 반대)
internal/config/sshconfig.go ~/.ssh/config 파서 → SSHConfigEntry(.Server()로 변환)
internal/vault/vault.go    age scrypt Encrypt/Decrypt, Secrets/GitHubAuth, ErrBadPassphrase
internal/sync/github.go    Repo/Remote/Check/Get/Put + 센티널 (net/http 직접, SDK 없음)
internal/ssh/session.go    Dial → RequestPty → Shell, stdout 펌프, WindowChange, keepalive,
                           authMethods(password/key/agent) + keySigner
internal/ssh/agent_unix.go / agent_windows.go
                           agentSigners — 유닉스 소켓 / named pipe (io.Closer 반환, Dial이 닫는다)
internal/ssh/hostkey.go    known_hosts 검증, TOFU 프롬프트 채널
internal/ssh/errors.go     센티널 에러 + net/ssh 에러 분류
internal/sftp/browser.go   Browser 인터페이스(List/Stat/Remove/Rename/경로) + Local
internal/sftp/remote.go    ssh.Dial → sftp.NewClient, ReadDir/Lstat/Getwd, 재귀 Remove
internal/sftp/transfer.go  Progress(atomic) / copyCtx / Upload / Download / StatLocal
internal/sftp/tree.go      Plan(재귀 계획·심링크 skip) / Set / RunSet
internal/model/server.go   Server·FileEntry (UI·config·ssh·sftp가 공유하는 유일한 자료구조)
```

`ssh.Dial`이 네트워크로 나가는 **유일한 지점**이다. SFTP도 그 위에 올라간다(`sftp.Connect`) —
검증되지 않은 다이얼 경로를 두 번째로 만들지 말 것. 호스트키 검증도 그래서 공짜로 따라온다.
`ssh`는 `config`를 import 하지 않으므로 known_hosts 경로와 append 함수는 `ssh.Options`로 UI가 주입한다.

의존 방향은 `ui → {config, ssh, sftp, sync} → model`, 그리고 `sftp → ssh`,
`config → vault` 한 방향이다. `config`와 `ssh`는 서로를 모르고, `model`은 아무것도 import 하지
않으며, **`vault`와 `sync`는 서로도 `config`도 모른다** — `vault`는 바이트를, `sync`는 이미
암호화된 바이트를 옮길 뿐이다. `sync`가 평문을 만질 수 있는 경로를 만들지 말 것.

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
start ──(금고 있음)──▶ unlock ──(성공)──▶ empty / ──(3회 실패)──▶ 종료
start ──(평문 발견)──▶ unlock[create] ──▶ migrate ──▶ empty + 결과 상태줄
start ──(둘 다 아님)──▶ empty                       (패스프레이즈를 묻지 않는다)

empty ──(+ Connect 선택)──▶ form ──(저장)──▶ empty        + 사이드바 리스트 리로드
empty ──(사이드바 e)──▶ form(editingID 채움) ──(저장)──▶ empty
empty ──(서버 선택)──▶ tab[connecting] ──(성공)──▶ tab[live]
                          └──(실패)──▶ 탭 제거 + error 카드 ──(r 재시도 / e 편집 / esc 닫기)
tab[live] ──(alt+숫자 / alt+←→)──▶ 다른 탭 (이전 탭은 계속 출력을 받는다)
tab[live] ──(Ctrl+B)──▶ focus만 sidebar로 (세션은 살아있고 화면도 계속 렌더)
tab[live] ──(원격 셸 정상 종료 / alt+w)──▶ 탭 닫힘 ──▶ 남은 탭 / empty
tab[live] ──(keepalive 실패)──▶ tab[lost] ──(백오프 만료 | r)──▶ tab[reconnecting]
tab[reconnecting] ──(성공)──▶ tab[live] (화면 초기화, 새 셸) / (실패)──▶ tab[lost] (백오프 증가)

empty|terminal ──(사이드바 f)──▶ sftp(connecting) ──(성공)──▶ sftp
                                    └──(실패)──▶ sftp + 떠 있는 에러 카드
                                                 (패널은 안 내려간다 — r 재시도 / e 편집 / esc 닫기)
sftp ──(드롭 / t / 파일에서 enter)──▶ scanning(Plan) ──▶ pending(확인) ──(enter)──▶
                                     transferring ──(완료/실패/취소)──▶ sftp + 결과 상태줄
transferring ──(ctrl+c)──▶ cancelling ──▶ sftp + "transfer cancelled"
sftp ──(d)──▶ scanning ──▶ confirm(삭제 개수 포함) ──(enter)──▶ sftp + 목록 갱신
sftp ──(R)──▶ rename(한 줄 입력) ──(enter)──▶ sftp + 목록 갱신
sftp ──(Ctrl+B|esc)──▶ focus만 sidebar로 (SFTP 연결은 유지)

any ──(사이드바 /)──▶ sidebar[filtering] ──(esc)──▶ sidebar
                        └──(enter)──▶ sidebar[FilterApplied] (단축키 복귀, 결과는 유지)
sidebar ──(헤더에서 enter/space/←/→)──▶ 접힘 토글 + ui.json 저장
any ──(사이드바 i)──▶ rightImport(parsing) ──(sshConfigParsedMsg)──▶ rightImport
rightImport ──(enter)──▶ prevRight 복귀 + 목록 리로드 + "imported N servers (M skipped)"
            └──(esc)──▶ prevRight 복귀 (세션 탭은 건드리지 않는다)

form[저장, 첫 비밀]──▶ unlock[create] ──(성공)──▶ 원래 저장이 이어서 실행 (requireVault)
연결 실패[ErrKeyPassphraseRequired]──▶ keyPass(한 줄) ──(enter)──▶ 금고 저장 + 재연결

any ──(사이드바 Y)──▶ rightSync ──(Check 통과)──▶ prevRight 복귀 + 토큰 금고 저장
                          └──(public/토큰 거절)──▶ 폼에 에러, **아무것도 저장하지 않는다**
any ──(사이드바 S)──▶ pushing ──▶ "synced · N servers · …" / ErrSyncConflict 안내
any ──(사이드바 P)──▶ pulling ──▶ confirm(교체 미리보기) ──(enter)──▶ 목록 리로드
                                                                    (열린 탭은 그대로)

sidebar|sftp ──(?)──▶ help ──(아무 키 / 클릭)──▶ 원래 상태 그대로 (아무것도 안 바뀐다)
help ──(/)──▶ help[검색] ──(esc)──▶ help
session ──(ctrl+b)──▶ sidebar ──(?)──▶ help          (세션에는 도움말 키가 없다)
session ──(드래그)──▶ 선택 ──(떼는 순간)──▶ OSC 52 복사 + "copied N lines"
                                          (스크롤·리사이즈·탭 전환·아무 키 → 선택 해제)
start ──(keys.json 문제)──▶ 기존 시작 경로 + 경고 상태줄 (상세는 `?` 카드 아래)
```

> **v7부터 아래 표들은 요약이고, 출처는 `internal/ui/keymap.go`의 `defaultBindings`다.**
> 앱 안에서는 `?`(사이드바·파일 패널)로 같은 내용을 보고, 터미널에서는 `--keys`로 덤프한다.
> 키를 바꿀 때는 표가 아니라 그 테이블을 고칠 것 — 표만 고치면 문서만 바뀐다.

### SFTP 키맵 (v2에서 바뀐 것 포함)
`space`는 **선택 토글**이다(v2의 "즉시 전송"에서 바뀜). 전송은 `enter`/`t`/드래그 셋으로 충분하고,
다중 선택이 생긴 이상 고르는 키가 하나 필요하다.

| 키 | 동작 |
|---|---|
| `space` | 선택 토글 (커서는 다음 줄로) |
| `a` | 선택 전체 해제 |
| `t` | 전송 — 선택이 있으면 전체, 없으면 커서 항목 (디렉터리 포함) |
| `enter` | 디렉터리면 진입, 파일이면 전송 |
| `d` / `R` / `r` | 삭제 / 이름변경 / 새로고침 |
| `ctrl+c` | 전송 중이면 **취소**(앱 종료 아님) |

### 사이드바 키맵 (v5에서 추가된 것)
기존 키(`enter`·`n`·`e`·`d`·`f`·`q`)의 의미는 **하나도 바뀌지 않았다**.

| 키 | 동작 |
|---|---|
| `/` | 필터 시작 — 이후 **모든 키를 필터가 먹는다**(`q`·`ctrl+c` 포함) |
| `esc` | 필터 해제 |
| `enter`/`space`/`←`/`→` (그룹 헤더에서) | 접기 / 펴기 (+ `ui.json` 저장) |
| `i` | `~/.ssh/config` import 미리보기 |
| `s` | 최근 접속 순 정렬 토글 (+ `ui.json` 저장) |
| `Y` | 동기화 설정 (v6 — 없으면 등록, 있으면 재설정) |
| `S` | push — 로컬 → 원격 (등록 전에는 상태줄만) |
| `P` | pull — 원격 → 로컬 (확인 패널을 거친다) |
| `?` | 단축키 카드 (v7 — 파일 패널에서도 같은 키, **세션 안에서는 없다**) |

`n`/`e`/`d`/`f`는 **그룹 헤더와 `+ Connect`에서는 아무것도 하지 않는다**(`isServerRow`가
유일한 판정 지점 — `it.connect`만 거르던 곳에 `it.header`가 같이 들어갔다).

### 탭 키맵 (v4)
| 키 | 동작 |
|---|---|
| `alt+1`…`alt+9` | n번째 탭 |
| `alt+←`/`alt+→` (`alt+h`/`alt+l`) | 이전/다음 탭 (순환) |
| `alt+w` | 현재 탭 닫기 |
| 사이드바 `n` | 선택한 서버로 **새** 세션 (`enter`는 이미 열린 탭으로 전환) |
| `r` (끊긴 탭에서) | 백오프를 기다리지 않고 즉시 재연결 |

`ctrl+b`의 의미는 **바뀌지 않았다** — 세션에서 포커스만 사이드바로. 탭 전환에 tmux식
프리픽스를 쓰지 않은 이유가 그것이다(`alt`는 셸이 거의 안 쓰므로 가로채도 손해가 적다).
바꾸려면 이제 `keymap.go`의 `ctxTabs` 바인딩만 고치면 된다(v7 전에는 `tabKey()`였다).
`tabKey`는 `modalUp()`이거나 도움말 카드가 떠 있으면 **아무것도 하지 않는다** — 모달이 모든
키를 먹는다는 규약이 탭 키에도 적용된다(`TestImportSwallowsTabKeys`).

**전송 대상 결정은 `filePane.targets()` 하나로 모은다.** 드래그도 이걸 쓰므로 선택된 행을 잡고
끌면 선택 전체가 따라가고, 선택되지 않은 행을 잡으면 그 행 하나만 간다(선택은 유지).
`Ctrl+B` 탈출은 세션을 끊는 게 아니라 **포커스만 옮기는 것**이다. 세션 종료와 포커스 이동을 헷갈리지 말 것.

`App.confirm`은 이 축과 **직교한다**: non-nil이면 `rightMode`가 뭐든 우측 패널 본문을 대체하고
`handleKey` 맨 앞에서 모든 키를 먹는다(답이 아닌 키는 버린다 — 뒤의 세션으로 새면 안 된다).
lipgloss v1에는 안전한 오버레이 합성이 **없다**. 그래서 기본은 여전히 영역 교체이고,
SFTP 모드의 다이얼로그와 v7의 도움말 카드만 직접 만든 `overlay`로 띄운다(위 절 참조) — 다른 모드에 오버레이를 퍼뜨리기 전에
그 절의 폭 계산 규칙을 먼저 읽을 것.
`App.pending`(전송 확인)·`App.rename`(이름변경 입력)·`App.sftpErr`(연결 실패)도 키 규칙은 같다 —
`handleKey`/`handleSFTPKey` 맨 앞에서 모든 키를 먹고(`TestPendingSwallowsKeys`,
`TestRenameSwallowsKeys`, `TestSFTPErrorCardFloatsAndDismisses`), 마우스 드래그도 함께 막힌다.
렌더는 넷 다 `sftpModal`이 같은 자리에 띄운다(`confirm`/`errorCard`/`renameState.View`).

v5는 같은 규약을 **둘 더** 적용한다(새 규칙이 아니라 같은 규칙의 재사용이다):
- **필터 입력 중**(`sidebar.Filtering()`, 즉 `FilterState() == list.Filtering`)이면
  `handleKey` **맨 앞에서** 모든 키를 리스트로 넘긴다. `q`/`ctrl+c`도 넘긴다 — 필터에 `q`를
  못 치면 검색이 아니다. 종료는 `esc`로 필터를 닫고 하면 된다
  (`TestFilterSwallowsShortcutKeys`). `enter` 확정 후 `FilterApplied`에서는 단축키가 다시 산다
  (`TestFilterAppliedRestoresShortcuts`).
- **`App.importing`**(= `focus == focusImport`)이면 `handleImportKey`가 전부 먹는다.
  `esc`는 `prevRight`로 돌아갈 뿐 **세션 탭을 건드리지 않는다**.

### 드래그와 키보드는 `buildTransfer` 하나로 수렴한다
드롭(`handleSFTPMouse`의 release)이든 `t`/`enter`든 만들어내는 것은 `transferReq` 뿐이고,
확인 화면·전송 실행은 그 뒤로 완전히 공유된다(`TestKeyboardTransferMatchesDrag`,
`TestMarkedTransferIncludesAllSelected`가 고정).
- **`Update`에서 디렉터리를 훑지 말 것.** 총 바이트를 알아야 진행률이 퍼센트가 되므로
  `Plan`이 먼저 돌아야 하는데 그건 네트워크·파일 IO다. 그래서 한 단계가 늘었다:
  `targets()` → `planTransfer` cmd → `plannedMsg` → `pending`. 훑는 동안 상태줄은 `scanning…`.
  삭제도 같은 이유로 `planDelete` → `deletePlannedMsg`를 거친다(개수를 확인 문구에 넣어야 한다).
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
- 루트 model의 상태는 `focus`(sidebar|form|session|local|remote|import|sync)와
  `rightMode`(empty|form|terminal|error|sftp|import|sync) 두 축.
  `App.unlock`은 이 두 축과 **직교하며 둘 다 무시한다** — non-nil이면 화면도 키보드도 전부
  잠금 화면의 것이다. `App.help`도 직교하지만 훨씬 가볍다(아무 키나 닫고 아무것도 안 바꾼다).
  **키 라우팅은 항상 `focus` 기준**이며, session 포커스일 때는 탈출키(`Ctrl+B`)만 가로채고
  나머지 키는 전부 SSH stdin으로 흘린다.
- **키 문자열은 `keymap.go` 밖에 쓰지 않는다**(v7). 핸들러는
  `switch a.keys.Action(ctxX, msg.String())`로 갈라진다. 예외는 한 줄 텍스트 입력
  (rename·키 패스프레이즈)과 필터·폼처럼 컴포넌트가 `msg.Type`을 직접 보는 곳뿐이고,
  그것들은 레지스트리에 `Doc: true`로 **적혀만** 있다.
- `tea.WindowSizeMsg`를 받으면 세 곳을 모두 갱신해야 한다: 패널 레이아웃, `vt` 리사이즈, SSH `WindowChange`. 하나라도 빠지면 화면이 어긋난다.

## 테스트
- `config`/`model`은 순수 로직 유닛 테스트(저장/로드 라운드트립, `Update`, 키 파일 권한 0600,
  `Remove`가 `KeysDir()` 안의 pem만 지우는지, `servers.json`에 `"password":`가 없는지).
- `internal/vault`는 라운드트립·틀린 패스프레이즈·**암호문에 평문이 바이트로도 없는지**.
- `internal/sync`는 `httptest.Server`로 GitHub를 흉내 낸다 — public 거절 시 `Put`이 아예
  불리지 않는 것과, **모든 에러 문자열에 토큰이 없는 것**이 핵심이다.
- `internal/ui`의 v7 축은 셋이다: `TestDefaultsMatchV6`(키가 하나도 안 바뀌었다는 증거),
  `TestHelpMatchesRealBindings` + `TestEveryActionIsDispatched`(도움말이 거짓말하지 않는다),
  `TestHelpCellSurvivesWarnings` + `TestStatusHintNeverOverflows`(경고가 떠도 `? help`가 남고
  상태줄은 절대 폭을 넘지 않는다). **기존 테스트는 한 줄도 고치지 않고 통과해야 한다**
  (`confirm.resolve`가 keymap을 받게 된 시그니처 변경만 예외).
- **금고는 느리다**(scrypt work factor 19, 연산당 수백 ms·`-race`에선 수 초). ui 테스트의
  `drain`/`run`(`vault_test.go`)이 커맨드를 goroutine에서 돌리고 넉넉한 예산으로 기다린다 —
  채널을 기다리는 펌프(`waitForHostKey`)를 `drain`에 넘기면 멈추므로 넘기지 말 것.
- `internal/ssh`는 in-process SSH 서버(`session_test.go`의 `startTestServer`)로 검증한다 —
  이 시스템에는 sshd가 없다. 호스트키 세 갈래와 에러 분류가 여기 걸려 있다.
- `internal/sftp`도 같은 하네스를 본떠(`remote_test.go`의 `startSFTPServer`) `subsystem sftp`에
  `pkgsftp.NewServer(ch)`를 붙여 업로드·다운로드 라운드트립까지 실제로 돈다.
- `internal/ui`는 터미널 없이 root model을 직접 두드린다. 레이아웃 불변식(`TestLayoutAlignment`,
  `TestLayoutAlignmentWithPanels`, `TestLayoutAlignmentWithGroupsAndImport`,
  `TestLayoutAlignmentWithUnlockAndSync`)은 확인 패널·에러 카드·그룹 헤더·필터 입력·
  import 미리보기·잠금 화면·동기화 폼 어느 상태에서도 모든 행이 정확히 width여야 하고
  **세로 예산은 그대로**여야 한다(잠금 화면만은 프레임 전체를 대체하지만, 크기 규칙은 같다).
- 풀스크린 앱(vim) 갱신만 자동화하지 않는다 — 로컬 sshd 수동 확인이 수용 기준.
- v8의 테스트는 셋뿐이다: `TestDefaultPathUnchanged`/`TestXDGWinsOnEveryOS`(설정 경로가
  안 움직였다는 증거), `TestBuildVersionFallback`/`TestBuildVersionUsesLdflags`(main 패키지의
  유일한 테스트 — 릴리스 스탬프는 물론 vcs 폴백 형식도 고정한다).
  **`internal/ssh/auth_test.go`는 `//go:build !windows`**다(유닉스 소켓 agent 하네스).
  Windows 쪽은 `auth_windows_test.go`가 "agent 없음 → `ErrAgentUnavailable`" 한 케이스만
  본다 — 환경변수를 지우는 대신 **없는 파이프 이름을 넣는다**(지우면 CI 머신의 진짜 agent에
  붙어 버려서 결과가 그 머신의 서비스 상태에 좌우된다).
- v9의 축은 `selection_test.go` 하나다: 선택 렌더(`TestSelectionRendersReversed` — 뒤집었다
  **되돌리는 것까지** 확인한다)·리니어 규칙·역방향 드래그 정규화, 스크롤백 연동
  (`TestSelectionInScrollbackCopiesPastLines`, `TestScrollClearsSelection`,
  `TestNoAutoScrollWhileDragging`), 클립보드(`TestCopyEmitsOSC52Once` — **다음 프레임에는 없다**,
  `TestCopyTruncatesAt64KiB`), 그리고 규약 둘(`TestSelectionBlockedByModal`,
  `TestSelectionNeverBreaksLayout`). **기존 테스트는 한 줄도 고치지 않고 통과한다** —
  `session.copy`는 `Doc: true`에 `Priority: 0`이라 상태줄 문구가 v6 그대로다
  (`TestWideStatusLineUnchanged`가 그것을 잡는다).
- **0600 단언은 `wantPerm0600`(`store_test.go`) 하나로 모인다.** Go는 Windows에서 모든 파일을
  0666으로 보고하고 v8은 ACL을 손으로 만들지 않기로 했으므로, 그 OS에서는 **건너뛴다**
  (약하게 고치지 말 것 — 존재 확인은 그대로 하고, 그 플랫폼의 보호는 금고가 암호문이라는
  사실이며 그건 `internal/vault` 테스트가 모든 OS에서 확인한다).
- CI(`.github/workflows/ci.yml`)는 3 OS 매트릭스지만 **`-race`는 ubuntu에서만** 돈다 —
  금고가 scrypt라 race 아래서는 연산당 수 초라 3배로 늘릴 이유가 없다.
  **CI는 시크릿을 요구하지 않는다**(테스트는 in-process 서버와 `httptest`로 전부 돌고,
  릴리스도 기본 `GITHUB_TOKEN`만 쓴다). v9도 그대로다 — 외부 토큰이 필요해지는 순간은
  배포 채널(brew tap·scoop)을 넣는 v10이다.

## 환경 주의
- Go는 `/usr/local/go`가 아니라 **`~/.local/go`**에 설치돼 있다(go1.25.0, root 없이 설치).
  `~/.bashrc` 마지막 줄 PATH에 `$HOME/.local/go/bin`이 들어 있으므로 새 셸에서는 그냥 `go`가 잡힌다.
  PATH를 물려받지 못하는 환경에서만 `export PATH=$HOME/.local/go/bin:$PATH`.
- 의존성이 요구해서 `go.mod`의 go 디렉티브는 **1.25.0**이다(계획서의 "1.22+"보다 높음). 툴체인을 낮추면 빌드가 안 된다.
- 이 시스템에는 **sshd가 없다**. 세션 계층은 `internal/ssh/session_test.go`가 in-process SSH 서버를 띄워 검증한다.
  agent 인증도 같은 하네스 위에서 `agent.NewKeyring()`을 유닉스 소켓에 붙여 검증한다(`auth_test.go`).
- WSL2에는 **Secret Service 데몬이 없어** `go-keyring`이 D-Bus 에러로 실패한다. v6에서 OS
  키체인 대신 금고를 쓴 이유의 절반이 이것이다(나머지 절반은 키체인 값이 기기 밖으로 못 나가서
  동기화와 정면으로 충돌한다는 것). 키체인을 다시 꺼내지 말 것.
- **Windows agent는 유닉스 소켓이 아니라 named pipe**(`\\.\pipe\openssh-ssh-agent`)다.
  여기서는 그 경로를 실행해 볼 수 없으니 `GOOS=windows go vet ./...`로 컴파일만 확인하고,
  실제 동작은 v8 계획서의 수동 확인 항목으로 남긴다.
- `goreleaser`는 로컬에 없어도 된다(CI가 돌린다). 리허설이 필요하면
  `go install github.com/goreleaser/goreleaser/v2@latest` 후 `~/go/bin/goreleaser`.
