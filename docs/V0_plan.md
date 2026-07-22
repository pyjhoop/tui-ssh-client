# TUI SSH Client — v0 구현 계획

## Context (왜 만드는가)
사이드바 레이아웃을 가진 **TUI 기반 SSH 클라이언트**를 만든다. 사용자가 서버 접속 정보를 등록해 좌측 리스트로 관리하고, 리스트에서 서버를 선택하면 우측 패널에 실제 SSH 세션이 임베딩되어 동작하는 것이 목표다. 현재 프로젝트 디렉토리는 비어 있어 처음부터(greenfield) 스캐폴딩한다.

이 문서는 전체 로드맵을 개괄하되, **v0 구현**에 초점을 둔다.

## 확정된 결정 (사용자 합의)
- **언어/스택**: Go + Bubble Tea (Charmbracelet 생태계)
- **세션 렌더링**: 우측 패널에 실제 PTY 세션 **임베딩** (전체화면 핸드오프 아님)
- **인증**: 비밀번호 + 키 파일 **둘 다** 지원. 키를 폼에 직접 붙여넣으면 파일로 저장 후 그 경로를 사용
- **저장소**: 평문 설정 파일 `~/.config/ssh-client/servers.json`

## 선행 조건
- **Go 미설치** — 현재 시스템에 Go 툴체인이 없다. 구현 시작 전 Go 1.22+ 설치 필요.
- OpenSSH 클라이언트는 설치돼 있음(참고용, 우리는 라이브러리로 직접 연결).

## 기술 스택 / 의존성
| 목적 | 패키지 |
|---|---|
| TUI 프레임워크 | `github.com/charmbracelet/bubbletea` |
| 스타일/레이아웃 | `github.com/charmbracelet/lipgloss` |
| 리스트/입력 위젯 | `github.com/charmbracelet/bubbles` (list, textinput, textarea) |
| SSH 연결 | `golang.org/x/crypto/ssh` |
| 임베디드 터미널 에뮬레이터 | `github.com/charmbracelet/x/vt` (ANSI 스트림 → 셀 그리드) |

> 핵심 난이도는 **우측 패널 임베딩**이다. Bubble Tea가 화면 전체를 그리므로, SSH 세션의 raw 바이트 출력을 `x/vt` 가상 터미널에 먹여 셀 그리드로 만들고 그것을 우측 패널 크기에 맞춰 매 프레임 렌더한다. 키 입력은 세션 모드일 때 SSH stdin으로 전달한다.

## 프로젝트 구조 (제안)
```
ssh-client/
├─ go.mod
├─ main.go                 # 진입점: tea.NewProgram(model).Run()
├─ internal/
│  ├─ config/
│  │  └─ store.go          # servers.json 로드/저장, ~/.config/ssh-client 관리
│  ├─ ssh/
│  │  └─ session.go        # x/crypto/ssh 연결 + PTY + shell, resize 처리
│  ├─ ui/
│  │  ├─ app.go            # 루트 model: 레이아웃/포커스/모드 상태머신
│  │  ├─ sidebar.go        # 좌측 서버 리스트 (bubbles/list)
│  │  ├─ form.go           # 연결정보 입력 폼 (textinput/textarea)
│  │  └─ terminal.go       # 우측 임베디드 터미널 뷰 (x/vt 렌더)
│  └─ model/
│     └─ server.go         # Server 구조체 (host/port/user/auth/...)
```

## 데이터 모델 & 저장
`internal/model/server.go`:
```go
type AuthMethod string // "password" | "key"

type Server struct {
    ID       string     // uuid 또는 host+user 해시
    Name     string     // 리스트 표시명 (없으면 user@host)
    Host     string
    Port     int        // 기본 22
    User     string
    Auth     AuthMethod
    Password string     // v0: 평문 저장(경고 표시). Auth=password일 때
    KeyPath  string     // Auth=key일 때 개인키 파일 경로
}
```
- 저장 위치: `${XDG_CONFIG_HOME:-~/.config}/ssh-client/servers.json` (배열).
- **키 직접 입력 처리**: 폼에서 키 본문을 붙여넣으면 `~/.config/ssh-client/keys/<id>.pem` 으로 `0600` 권한 저장 후 `KeyPath`에 그 경로 기록.
- v0는 평문 저장. 앱 최초 실행/저장 시 "비밀번호가 평문으로 저장됩니다" 경고 1회 노출.

## v0 기능 분해 (요청한 4가지 매핑)
**1. 명령어 실행 → ssh-client 화면**
- `ssh-client` 실행 시 Bubble Tea 앱 시작. 전체 레이아웃: 좌측 사이드바(고정 폭, 예: 30) + 우측 콘텐츠 영역. lipgloss로 테두리/포커스 하이라이트.
- 사이드바 최상단에 `+ Connect` 항목(신규 등록), 그 아래 등록된 서버 리스트.

**2. Connect 클릭/엔터 → 연결정보 입력 폼**
- 사이드바에서 `+ Connect` 선택(마우스 클릭 또는 커서+Enter) 시 우측 영역이 **입력 폼**으로 전환.
- 필드: Name(선택), Host, Port(기본 22), User, Auth 토글(Password/Key), Password 또는 KeyPath/키본문(textarea).
- Tab/Shift+Tab 필드 이동, Enter로 저장, Esc로 취소.
- 마우스 지원: Bubble Tea `tea.WithMouseCellMotion()` 활성화 → 클릭으로 항목/필드 포커스.

**3. 저장 완료 → 좌측 리스트에 등록**
- 폼 검증 후 `config.Store.Add()` → servers.json 갱신 → 사이드바 리스트 리로드.
- 리스트 항목 표시: `Name` 또는 `user@host`.

**4. 등록 서버 클릭/엔터 → 우측에 SSH 연결**
- 리스트 항목 선택 시 `ssh.Connect(server)` 실행(비동기 `tea.Cmd`).
- `x/crypto/ssh`로 Dial → `RequestPty` → `Shell()`. 세션 stdout/stderr 바이트를 채널로 읽어 `x/vt` 터미널에 write.
- 우측 패널이 **터미널 모드**로 전환: `x/vt`의 셀 그리드를 패널 크기에 맞춰 렌더. 앱 모드가 "session"일 때 키 입력을 세션 stdin으로 전달(특수키→ANSI 시퀀스 변환).
- 창 리사이즈(`tea.WindowSizeMsg`) 시 우측 패널 셀 크기 재계산 + SSH `WindowChange` 전송 + vt 리사이즈.
- 연결 실패/종료 시 우측에 상태 메시지 표시 후 리스트 모드로 복귀. `Ctrl+B` 등 지정 키로 세션 → 사이드바 포커스 탈출.

### 상태 머신 (루트 model)
- `focus`: `sidebar | form | session`
- `rightMode`: `empty | form | terminal`
- 키 라우팅은 `focus` 기준. 세션 포커스일 때만 키를 SSH로 흘리고, 탈출키만 가로챔.

## 향후 MVP 로드맵 (합의 후 상세화)
원래 v3에 "다중 세션 + 재연결 + 검색/필터 + 그룹/폴더 + SFTP 재귀·진행률"이 뭉쳐 있었으나
한 버전에 담기엔 커서 **버전을 늘려 재배치**했다. v3는 SFTP 심화 한 축만 다룬다.

- **v1**: 세션 안정화 — 스크롤백/리사이즈 견고화, known_hosts 호스트키 검증, 연결 에러 UX, 서버 편집/삭제. 상세는 `docs/V1_plan.md`. (구현 완료)
- **v2**: SFTP 파일 브라우저 — 우측을 Local | Remote로 분할, 드래그 앤 드롭 전송. 상세는 `docs/V2_plan.md`. (구현 완료)
- **v3**: SFTP 심화 — 디렉터리 재귀 전송, 진행률·취소, 다중 선택, 원격 삭제/이름변경. 상세는 `docs/V3_plan.md`. (구현 완료)
- **v4**: 다중 세션(탭), 세션 유지·자동 재연결. 상세는 `docs/V4_plan.md`. (구현 완료)
- **v5**: 목록 UX — 검색/필터, 그룹/폴더, `~/.ssh/config` import. 상세는 `docs/V5_plan.md`.
- **v6**: 보안·이식성 — 암호화 금고(age), ssh-agent, 비공개 레포 동기화. 상세는 `docs/V6_plan.md`.
  (OS 키체인은 동기화와 충돌해 **금고로 대체**됐고, 점프호스트는 v7로 옮겼다.)
- **v7**: 편의 — 포트포워딩, 점프호스트/ProxyJump, 테마/키맵 설정.

## 검증 (end-to-end)
1. **선행**: Go 1.22+ 설치 (`go version` 확인), `go mod init` + 의존성 `go get`.
2. `go build ./...` 성공.
3. 로컬 SSH 서버(`localhost`)로 실제 흐름 테스트:
   - 앱 실행 → `+ Connect` → localhost/22/현재사용자/키 등록 → 리스트에 표시.
   - 리스트 선택 → 우측 패널에 셸 프롬프트 표시, `ls`/`echo` 입력·출력 확인.
   - 터미널 리사이즈 시 깨지지 않는지, 탈출키로 사이드바 복귀되는지 확인.
4. `config`/`model` 순수 로직은 유닛 테스트(`store_test.go`): 저장/로드 라운드트립, 키 본문→파일 저장 권한 0600.
5. 임베디드 터미널 렌더는 수동 확인(자동화 어려움) — vim 같은 풀스크린 앱 실행해 화면 갱신 확인을 v0 수용 기준으로.
