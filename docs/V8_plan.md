# TUI SSH Client — v8 구현 계획 (크로스 플랫폼 빌드 · 릴리스 · 한 줄 설치)

## Context (왜 만드는가)
v0~v7까지 기능은 다 들어갔지만 **이 앱을 쓰는 방법은 아직 "레포를 클론해서 `go build`"뿐이다.**
레포에는 README조차 없고, 태그도 릴리스도 CI도 없다. 그래서 v8은 기능이 아니라 **배포**다 —
Linux·macOS·Windows에서 터미널 한 줄로 받아 바로 실행되게 만든다.

그런데 지금 코드를 그대로 크로스 컴파일하면 **Windows에서 조용히 반쪽이 된다**:

1. `internal/ssh/session.go`의 `agentSigners`는 `net.Dial("unix", SSH_AUTH_SOCK)`이다.
   Windows OpenSSH agent는 유닉스 소켓이 아니라 **named pipe**(`\\.\pipe\openssh-ssh-agent`)라
   agent 인증이 무조건 실패한다. 게다가 v6 규약상 agent는 **조용히 폴백하지 않으므로**
   Windows 사용자는 "agent를 골랐는데 언제나 에러"를 보게 된다.
2. `internal/config/store.go:Default()`는 `XDG_CONFIG_HOME` → `~/.config` 두 갈래뿐이다.
   Windows에도 홈 디렉터리는 있으니 동작은 하지만 `C:\Users\x\.config\ssh-client`에 떨어진다.
3. `internal/ssh/auth_test.go`가 `net.Listen("unix", …)`이라 **windows CI에서 컴파일부터
   깨진다.** 빌드 태그가 없으면 매트릭스 테스트 자체를 켤 수 없다.

로드맵의 "포트포워딩 / 점프호스트(ProxyJump)"는 v7에서 v8로 밀렸지만 **다시 v9로 옮긴다.**
한 버전에 두 축을 넣지 않는다는 v3 이후의 규칙 그대로다 — 이 버전의 주제는 코드가 아니라
**배포 파이프라인**이고, 유일하게 손대는 런타임 코드는 위 세 개의 이식성 결함이다.

## 범위
| 포함 | 제외 (→ 이후 버전) |
|---|---|
| Windows 이식성 3건 (agent named pipe · 설정 경로 · 테스트 빌드 태그) | 포트포워딩 / ProxyJump → **v9** |
| `--version` (버전·커밋·빌드일, `go install` 폴백 포함) | Homebrew tap · Scoop · WinGet → v9 |
| `.goreleaser.yaml` — 6타깃 크로스 빌드 · 아카이브 · checksums | 앱 안 자동 업데이트 / 새 버전 알림 |
| GitHub Actions `ci.yml`(3 OS 매트릭스) · `release.yml`(태그 트리거) | 코드 서명 · macOS 공증 · Windows Authenticode |
| `install.sh` / `install.ps1` — 체크섬 검증 포함 | apt/rpm/nix/AUR 패키지, Docker 이미지 |
| `README.md` (설치 · 보안 주의) · `LICENSE` | 텔레메트리 · 크래시 리포트 |

---

## 확정된 결정 (임의로 뒤집지 말 것)

- **배포물은 단일 정적 바이너리 하나다.** `CGO_ENABLED=0`으로 빌드하고 설정 파일·에셋을 같이
  깔지 않는다 — 설정은 첫 실행에 `~/.config/ssh-client/`가 만들고, 없으면 없는 대로 뜬다
  (v6의 "비밀이 없으면 패스프레이즈를 묻지 않는다"가 곧 첫 실행 경험이다).
- **버전의 출처는 git 태그 하나다.** goreleaser가 `-X main.version=…`으로 주입하고, ldflags
  없이 `go install`로 깐 바이너리는 `runtime/debug.ReadBuildInfo()`의 vcs 정보로 대체한다.
  버전 상수를 소스에 적어 두고 릴리스마다 손으로 고치는 방식을 만들지 말 것 — 반드시 어긋난다.
- **`--version`은 TUI를 띄우지 않는다.** v7의 `--keys`와 같은 규약이다: 플래그를 읽고 stdout에
  찍고 끝낸다(`main.go`의 `keys.set` 분기 바로 옆).
- **릴리스 트리거는 `v*` 태그 push 하나뿐이다.** `workflow_dispatch`로 임의 릴리스를 만들 수
  있게 하면 "어느 커밋이 v1.2.0인가"의 답이 둘이 된다. 태그가 없으면 릴리스도 없다.
- **CI가 통과하지 않으면 릴리스도 안 된다.** `release.yml`은 goreleaser 앞에서
  `go vet` + `go test ./internal/...`를 다시 돌린다 — 같은 커밋이라도 릴리스는 별도 워크플로다.
- **`install.sh`는 반드시 체크섬을 검증한다.** `curl | sh`를 권하는 이상 최소한의 무결성 확인은
  타협 대상이 아니다. `checksums.txt`를 같이 받아 `sha256sum` / `shasum -a 256`으로 맞춰 보고,
  **둘 다 없으면 설치를 중단한다**(검증을 건너뛰고 계속하지 않는다).
- **`install.sh`는 sudo를 스스로 부르지 않는다.** 기본 설치 위치는 `~/.local/bin`이고, PATH에
  없으면 넣는 방법을 **안내만** 한다. 파이프로 받은 스크립트가 root를 요구하는 순간 사용자가
  확인할 수 없는 권한을 얻는다. 전역 설치는 사용자가 직접 `INSTALL_DIR=/usr/local/bin`을
  sudo와 함께 주는 경우뿐이다.
- **macOS 바이너리에 서명·공증을 하지 않는다.** 대신 **브라우저로 받는 배포물을 만들지
  않는다**(`.dmg`·`.pkg` 없음) — `curl`/`tar`로 받은 파일에는 quarantine 속성이 붙지 않아
  Gatekeeper가 막지 않는다. 서명은 유료 개발자 계정을 전제로 하므로 v8의 범위 밖이고,
  그 사실을 README에 적어 둔다.
- **Windows agent는 플랫폼 관례를 따르되 폴백하지 않는다.** `SSH_AUTH_SOCK`이 있으면 그
  이름을, 없으면 기본 named pipe(`\\.\pipe\openssh-ssh-agent`)를 연다. 둘 다 안 되면
  **`ErrAgentUnavailable`로 끝난다** — v6의 "agent는 조용히 password로 폴백하지 않는다"는
  그대로다. 같은 agent를 그 OS의 방식으로 찾는 것과, 다른 자격증명으로 넘어가는 것은 다르다.
- **named pipe 때문에 새 의존성을 넣지 않는다.** `x/crypto/ssh/agent.NewClient`는 `net.Conn`이
  아니라 `io.ReadWriter`만 요구하므로 Windows에서는 `os.OpenFile(pipe, os.O_RDWR, 0)`이면
  충분하다. `agentSigners`의 반환 타입을 `net.Conn` → `io.Closer`로 넓히기만 하면 되고,
  `Dial`이 defer로 닫는 v6의 수명 규약은 그대로다. `Microsoft/go-winio`를 들이지 말 것.
- **유닉스 쪽 설정 경로는 한 글자도 바꾸지 않는다.** `os.UserConfigDir()`로 통일하면 macOS가
  `~/Library/Application Support/`로 옮겨가 **기존 사용자의 서버 목록과 금고가 사라진 것처럼
  보인다.** Linux·macOS는 지금의 `XDG_CONFIG_HOME` → `~/.config` 그대로 두고, **Windows에서만**
  `%AppData%\ssh-client`를 쓴다. `XDG_CONFIG_HOME`이 있으면 어느 OS에서든 그게 이긴다 —
  테스트 전체가 그 변수에 의존한다.
- **Windows에서 파일 권한 0600을 흉내 내지 않는다.** ACL을 손으로 만들면 우리가 지킬 수 없는
  약속이 하나 생긴다. 보호는 금고가 **암호문**이라는 사실에서 나오고, 그 차이를 README에
  명시한다. 평문 백업(`servers.json.plaintext.bak`)을 만드는 v6 마이그레이션은 v6 이전 유닉스
  설치에만 존재하므로 Windows에서는 사실상 발생하지 않는다.
- **릴리스 자산에 `.age`·`servers.json` 같은 이름이 절대 들어가지 않는다.** goreleaser는
  `dist/`에만 쓰고 `.gitignore`가 이미 `/dist/`를 막는다 — 그 항목을 지우지 말 것.
- **CI는 시크릿을 요구하지 않는다.** 테스트는 in-process SSH 서버와 `httptest`로 전부 돌고
  (`internal/ssh/session_test.go`, `internal/sync/github_test.go`), 릴리스도 기본
  `GITHUB_TOKEN`만 쓴다. 외부 토큰이 필요해지는 순간(brew tap 등)은 v9다.

---

## 배경 — 기존 코드에서 반드시 재사용할 것
- `main.go`의 `keysFlag` 분기 — `--version`은 그 옆에 같은 모양으로 붙는다(플래그 → 출력 → 종료).
- `internal/ssh/session.go:authMethods`가 `io.Closer`를 같이 돌려주고 `Dial`이 defer로 닫는
  구조 — Windows 파이프도 **그 수명 규약에 그대로 들어간다**. 새 정리 경로를 만들지 말 것.
- `internal/config/store.go:Default()` — Windows 분기는 이 함수 **안에서만** 갈라진다.
  `Store`의 나머지(`Path`/`KeysDir`/`KnownHostsPath`)는 이미 `filepath.Join`이라 이식성이 있다.
- `internal/ssh/auth_test.go`의 agent 하네스(`agent.NewKeyring()` + 유닉스 소켓) — 파일을
  통째로 `//go:build !windows`로 묶고, Windows용으로는 "파이프가 없을 때 `ErrAgentUnavailable`"
  한 케이스만 새로 쓴다.
- `docs/V6_plan.md`의 "수동 확인" 절 형식 — v8의 수용 기준도 같은 형식으로 쓴다.

## 의존성
**Go 의존성은 늘지 않는다.** 새로 들어오는 것은 전부 레포 루트의 설정 파일과 CI다:
`goreleaser`(GitHub Actions에서 실행, 로컬 설치는 선택), `actions/checkout`, `actions/setup-go`.

---

## 구현

### 1. 이식성 — `internal/ssh/agent_unix.go` / `agent_windows.go` (신규)
`session.go`의 `agentSigners`를 두 파일로 쪼갠다. 시그니처는
`func agentSigners() (io.Closer, func() ([]xssh.Signer, error), error)`로 통일하고,
호출부(`authMethods`)는 `net.Conn` → `io.Closer` 한 줄만 바뀐다.

- `agent_unix.go` (`//go:build !windows`): **지금 코드 그대로.** `SSH_AUTH_SOCK`이 없으면
  `ErrAgentUnavailable`, 있으면 `net.Dial("unix", …)`.
- `agent_windows.go` (`//go:build windows`): `SSH_AUTH_SOCK`이 있으면 그 이름을, 없으면
  `\\.\pipe\openssh-ssh-agent`를 `os.OpenFile(…, os.O_RDWR, 0)`으로 연다. 실패는
  `ErrAgentUnavailable`에 **경로를 붙여서** 돌려준다(v1 이후 "에러가 파일 이름을 알려준다" 규칙).

### 2. 이식성 — `internal/config/store.go:Default()`
`XDG_CONFIG_HOME`이 비었을 때만 갈라진다:

```go
if base == "" {
    if runtime.GOOS == "windows" {
        base, err = os.UserConfigDir()   // %AppData%
    } else {
        home, err := os.UserHomeDir()    // 지금과 동일
        base = filepath.Join(home, ".config")
    }
}
```

유닉스 경로는 문자 하나도 바뀌지 않는다(`TestDefaultPathUnchanged`가 고정).

### 3. 이식성 — 테스트 빌드 태그
`internal/ssh/auth_test.go` 맨 위에 `//go:build !windows`. 대신
`auth_windows_test.go`에 "agent가 없을 때 agent 인증이 `ErrAgentUnavailable`" 한 케이스를 둔다.
**구현하면서 바뀐 것**: 환경변수를 지우는 대신 `SSH_AUTH_SOCK`에 **없는 파이프 이름**을 넣는다 —
비우면 기본 파이프로 폴백해서 CI 머신에 OpenSSH agent 서비스가 켜져 있는지에 결과가 좌우된다
(켜져 있으면 키가 없어 `ErrAuthFailed`가 나고 테스트가 흔들린다). `internal/sftp`·`internal/ui` 테스트는 유닉스 소켓을 쓰지 않으므로
손대지 않는다.

### 4. `--version` — `main.go`

```go
var (
    version = "dev" // goreleaser: -X main.version
    commit  = ""
    date    = ""
)
```

`buildVersion()`은 `version == "dev"`이면 `debug.ReadBuildInfo()`의 `vcs.revision`/`vcs.time`을
읽어 채운다(`go install …@latest` 사용자를 위한 것). 출력은 한 줄:
`ssh-client v1.0.0 (abc1234, 2026-07-23, go1.25.0, linux/amd64)`.

### 5. `.goreleaser.yaml` (신규, 레포 루트)
- `builds`: `env: [CGO_ENABLED=0]`, `flags: [-trimpath]`,
  `ldflags: -s -w -X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}}`.
- `goos: [linux, darwin, windows]` × `goarch: [amd64, arm64]` = **6타깃**.
  `linux/arm` 32비트는 넣지 않는다 — 수요를 확인하기 전에 매트릭스를 넓히지 않는다.
- `archives`: 유닉스 `tar.gz` / Windows `zip`, 이름은
  `ssh-client_{{.Version}}_{{.Os}}_{{.Arch}}`, 안에 바이너리 + `README.md` + `LICENSE`.
- `checksum`: `checksums.txt`(sha256) — `install.sh`가 이 이름에 의존한다.
- `changelog`: `use: github`, `feat`/`fix`/`docs` 그룹, merge 커밋 제외.
- `release.footer`: install.sh / install.ps1 / `go install` 세 줄짜리 설치 안내.

### 6. GitHub Actions — `.github/workflows/ci.yml` (신규)
push/PR 대상. `matrix.os: [ubuntu-latest, macos-latest, windows-latest]`, `go-version: 1.25.x`.
`go vet ./...` → `go build ./...` → `go test ./internal/...`.
- **`-race`는 ubuntu에서만** 돌린다. 금고가 scrypt(work factor 19)라 `-race`에서는 연산당
  수 초고(CLAUDE.md의 경고), 3 OS 전부 돌리면 CI가 몇 배로 길어진다. 나머지 두 OS는 race 없이.
- job timeout 20분, `actions/setup-go`의 모듈 캐시 사용.

### 7. GitHub Actions — `.github/workflows/release.yml` (신규)
`on: push: tags: ['v*']`, `permissions: contents: write`.
`fetch-depth: 0`(changelog에 필요) → setup-go → `go vet` + `go test ./internal/...` →
`goreleaser/goreleaser-action@v6`(`args: release --clean`), `GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}`.

### 8. `install.sh` (신규, 레포 루트 — raw URL로 제공)
`set -eu`. 흐름:
1. `uname -s` / `uname -m` → `linux|darwin` × `amd64|arm64` 매핑. 그 외 조합은 **명확한 에러와
   `go install` 안내**로 끝낸다.
2. 버전: `VERSION` 환경변수가 있으면 그것, 없으면
   `https://api.github.com/repos/pyjhoop/ssh-client/releases/latest`의 `tag_name`을
   `grep`/`sed`로 뽑는다(jq를 요구하지 않는다). curl이 없으면 wget으로.
3. tarball과 `checksums.txt`를 `mktemp -d`에 받고(`trap … EXIT`로 정리),
   `sha256sum -c` 또는 `shasum -a 256 -c`로 검증한다. **둘 다 없으면 중단.**
4. 압축을 풀어 `${INSTALL_DIR:-$HOME/.local/bin}`에 `install -m 0755`로 놓는다.
5. `command -v ssh-client`가 안 잡히면 PATH에 추가하는 방법을 안내한다.

### 9. `install.ps1` (신규)
같은 흐름의 PowerShell 판. `$env:PROCESSOR_ARCHITECTURE` → arch, zip 다운로드,
`Get-FileHash -Algorithm SHA256`으로 `checksums.txt` 대조,
`$env:LOCALAPPDATA\Programs\ssh-client`에 풀고, 사용자 PATH에 없으면 추가 명령을 **안내한다**
(레지스트리를 말없이 고치지 않는다).

### 10. `README.md` · `LICENSE` (신규)
지금 레포에는 README가 없다. 구성은:
설치 세 줄(sh / ps1 / `go install`) → 스크린샷 자리 → **키맵은 표로 적지 않고 `?` 도움말과
`--keys`를 가리킨다**(v7의 "도움말용 문자열 테이블을 만들지 말 것"이 문서에도 그대로 적용된다 —
README에 키 표를 박으면 그게 낡는다) → 설정 파일 위치와 보안 절
(**비밀은 `vault.age` 안에만 있고, Windows에서는 파일 권한이 아니라 암호화가 유일한 보호**) →
macOS 미서명 안내. `LICENSE`는 아카이브에 포함되므로 같이 만든다.

### 11. 문서 갱신
`CLAUDE.md`: 현재 상태에 v8 추가, `docs/V8_plan.md` 링크, 개발 명령에 `--version`과
`goreleaser release --snapshot --clean` 추가, 환경 주의에 "Windows agent는 named pipe" 한 줄.

---

## 변경 / 추가 파일
| 파일 | 내용 |
|---|---|
| `internal/ssh/agent_unix.go` / `agent_windows.go` | 신규 — `agentSigners` 분리(`io.Closer` 반환) |
| `internal/ssh/session.go` | `agentSigners` 제거, `authMethods`의 타입 한 줄 |
| `internal/ssh/auth_test.go` | `//go:build !windows` |
| `internal/ssh/auth_windows_test.go` | 신규 — agent 부재 경로 |
| `internal/config/store.go` | `Default()`의 Windows 분기 |
| `internal/config/store_test.go` | 유닉스 경로 불변 + XDG 우선 테스트 |
| `main.go` | `--version`, `version`/`commit`/`date` 변수, `buildVersion()` |
| `.goreleaser.yaml` | 신규 |
| `.github/workflows/ci.yml` · `release.yml` | 신규 |
| `install.sh` · `install.ps1` | 신규 |
| `README.md` · `LICENSE` | 신규 |
| `CLAUDE.md` | 갱신 |

---

## 검증 (end-to-end)

**자동**
1. `go vet ./...`, `go build ./...`, `go test -race ./internal/...` (기존 그대로 통과).
2. 크로스 컴파일: `GOOS=windows GOARCH=amd64 go build ./...`,
   `GOOS=darwin GOARCH=arm64 go build ./...` — 이식성 수정 전에는 windows 빌드가 깨지는 것이
   정상이고, 수정 후 6타깃 전부 빌드된다.
3. `GOOS=windows go vet ./...` — 빌드 태그로 갈라진 테스트 파일이 windows에서도 컴파일되는지.
4. `goreleaser release --snapshot --clean` — 태그 없이 `dist/`에 아카이브 6개 +
   `checksums.txt`가 나오고, 아카이브 안 바이너리의 `--version`이 스냅샷 버전을 찍는지.
5. `shellcheck install.sh`(있으면).
6. 새 테스트:
   - `TestDefaultPathUnchanged` — 유닉스에서 `~/.config/ssh-client` 그대로.
   - `TestXDGWinsOnEveryOS` — `XDG_CONFIG_HOME`이 있으면 OS와 무관하게 그것.
   - `TestBuildVersionFallback` — ldflags 없이도 `dev`가 아닌 무언가를 찍는다.
   - `TestMissingAgentIsErrAgentUnavailable`(v6)이 양쪽 OS에서 유지된다.

**수동 확인 (v8 수용 기준 — 자동화하지 않음)**
1. 프리릴리스 태그(`v0.8.0-rc.1`)를 밀어 `release.yml`이 끝까지 돌고, 릴리스 페이지에
   아카이브 6개 + `checksums.txt`가 붙는다.
2. `curl -fsSL …/install.sh | sh` → `~/.local/bin/ssh-client`가 생기고 `ssh-client --version`이
   릴리스 태그를 찍는다. **체크섬을 일부러 깨뜨린 파일로 돌리면 설치가 중단된다.**
3. macOS(arm64)에서 같은 스크립트 → Gatekeeper 경고 없이 실행되고 실제 sshd에 붙는다.
4. Windows PowerShell에서 `irm …/install.ps1 | iex` → 실행되고 설정이
   `%AppData%\ssh-client\`에 생긴다.
5. Windows에서 `ssh-agent` 서비스를 켜고 `ssh-add`한 키로 **agent 인증 접속**이 된다.
   서비스를 끄면 `ErrAgentUnavailable` 에러 카드가 뜬다(조용히 password로 넘어가지 않는다).
6. Windows에서 금고 생성 → 재시작 → 잠금 화면 → 접속까지(v6 수용 기준의 Windows 재현).
7. `go install github.com/pyjhoop/ssh-client@latest`로 깐 바이너리의 `--version`이 커밋 해시를
   보여준다(ldflags 폴백).
8. 릴리스 아카이브 안에 설정·비밀 파일 이름이 하나도 없다(`tar tzf`로 확인).

---

## v8에서 하지 않는 것 (의도적 제외)
- 포트포워딩 / ProxyJump → **v9** (`ssh.Dial` 경로를 건드리는 연결 계층 작업)
- Homebrew tap · Scoop · WinGet 매니페스트 → v9 (별도 레포와 토큰이 필요하다)
- 코드 서명 · macOS 공증 · Windows Authenticode (유료 개발자 계정 전제)
- 앱 내 자동 업데이트 / 새 버전 알림 — SSH 클라이언트가 시작할 때 네트워크로 나가는 것은
  v6의 "동기화는 옵트인" 정신과 정면으로 충돌한다
- apt/rpm/nix/AUR, Docker 이미지, 32비트 타깃
- 텔레메트리 · 크래시 리포트
