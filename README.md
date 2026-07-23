# ssh-client

터미널 안에서 **서버 목록과 실제 SSH 세션을 나란히** 두는 TUI SSH 클라이언트입니다.
좌측 사이드바에서 서버를 고르면 우측 패널에 진짜 PTY 세션이 그대로 열리고,
`f`를 누르면 그 자리가 Local | Remote 두 개의 파일 패널로 갈라져 SFTP가 됩니다.

`ssh` 바이너리를 감싸지 않습니다. `golang.org/x/crypto/ssh`로 직접 연결하고,
출력 바이트를 내장 가상 터미널에 먹여 패널 크기로 렌더합니다 — vim·htop 같은
풀스크린 앱도 패널 안에서 그대로 돕니다.

## 설치

```sh
curl -fsSL https://raw.githubusercontent.com/pyjhoop/tui-ssh-client/main/install.sh | sh
```

```powershell
irm https://raw.githubusercontent.com/pyjhoop/tui-ssh-client/main/install.ps1 | iex
```

```sh
go install github.com/pyjhoop/tui-ssh-client@latest
```

설치 스크립트는 릴리스 아카이브와 `checksums.txt`를 함께 받아 **sha256을 대조한 뒤에만**
설치합니다. 대조할 도구(`sha256sum`/`shasum`, Windows는 `Get-FileHash`)가 없으면 설치를
중단합니다 — 검증을 건너뛰고 계속하지 않습니다.

스크립트는 **sudo를 부르지 않습니다.** 기본 설치 위치는 `~/.local/bin`
(Windows는 `%LOCALAPPDATA%\Programs\ssh-client`)이고, PATH에 없으면 넣는 방법을 안내만 합니다.
전역 설치는 직접 `INSTALL_DIR=/usr/local/bin`을 sudo와 함께 주는 경우뿐입니다.

특정 버전은 `VERSION=v0.8.0`으로 고정할 수 있습니다.

지원 타깃은 linux·darwin·windows × amd64·arm64 6개입니다. 그 밖의 조합은
`go install`로 직접 빌드하세요.

## 첫 실행

```sh
ssh-client
```

설정이 없으면 빈 목록으로 뜹니다. 사이드바의 `+ Connect`에서 접속 정보를 입력하면
그 순간부터 목록이 생기고, `~/.ssh/config`가 있으면 `i`로 골라서 가져올 수 있습니다
(자동으로 읽지 않습니다 — 무엇을 가져올지는 미리보기에서 직접 고릅니다).

- 인증은 **password · key · ssh-agent** 세 가지입니다.
- 호스트키는 **항상 검증**합니다. 처음 보는 호스트는 승인을 묻고(TOFU),
  키가 **바뀐** 경우에는 승인 단축키를 주지 않습니다 — 그게 정확히 중간자 공격이 보이는
  모습이라 한 키로 넘길 수 있으면 검증이 무의미해집니다. 에러 카드가 어느 파일 몇 번째 줄을
  고쳐야 하는지 알려줍니다.
- 세션은 탭으로 최대 8개까지 동시에 열립니다. 안 보이는 탭도 계속 출력을 받고,
  keepalive가 끊김을 감지하면 백오프로 자동 재연결합니다.

## 단축키

**이 README에는 키 표를 싣지 않습니다.** 표는 반드시 낡고, 그러면 문서가 조용히 거짓말을
하게 됩니다. 실제 바인딩은 한 곳(`internal/ui/keymap.go`)에만 있고, 두 가지 방법으로 봅니다:

- 앱 안에서 `?` — 지금 포커스에 맞는 도움말 카드가 뜹니다(`/`로 검색).
- 터미널에서 `ssh-client --keys` — 사람이 읽는 표. `--keys=json`은 `keys.json` 형식 그대로.

세션 안에서는 `?`가 원격 셸로 그대로 들어갑니다(vim·bash가 쓰는 평범한 글자입니다).
세션에서 빠져나오는 키는 `ctrl+b` 하나뿐이고, 그때 `?`를 누르면 됩니다.

재바인딩은 설정 디렉터리의 `keys.json`에 `"액션 ID": ["키", …]` 형식으로 적습니다.
출발점은 `ssh-client --keys=json > ~/.config/ssh-client/keys.json`입니다.
빈 배열은 해제이고, 문제가 있는 항목만 기본값으로 되돌린 뒤 나머지는 적용하며,
무엇을 거부했는지는 상태줄과 `?` 카드 아래에 남습니다.

## 설정 파일

| OS | 위치 |
|---|---|
| Linux · macOS | `${XDG_CONFIG_HOME:-~/.config}/ssh-client/` |
| Windows | `%AppData%\ssh-client\` |

`XDG_CONFIG_HOME`이 설정돼 있으면 **어느 OS에서든** 그것이 이깁니다.

| 파일 | 내용 |
|---|---|
| `servers.json` | 서버 **메타데이터만** (이름·user·host·port·그룹). 비밀은 들어가지 않습니다. |
| `vault.age` | 비밀번호·붙여넣은 개인키·키 패스프레이즈·동기화 토큰. age scrypt 암호문. |
| `known_hosts` | 이 앱이 승인한 호스트키. 읽기는 `~/.ssh/known_hosts`도 같이 봅니다. |
| `keys.json` | 사용자가 쓴 키 바인딩 (없어도 됩니다). |
| `ui.json` | 접힘 상태·정렬 같은 뷰 찌꺼기. 지워도 됩니다. |

## 보안

- **비밀은 `vault.age` 안에만 있습니다.** `servers.json`에는 직렬화 경로 자체가 없고,
  값은 연결 직전에 금고에서 주입됩니다.
- **패스프레이즈는 어디에도 저장되지 않습니다.** 프로세스가 사는 동안만 메모리에 있고,
  "이 기기에서 기억하기" 같은 옵션은 일부러 만들지 않았습니다.
- **비밀이 없으면 패스프레이즈를 묻지 않습니다.** key 파일과 agent만 쓴다면 금고는 아예
  만들어지지 않습니다. 금고는 첫 비밀을 저장하는 순간에 생깁니다.
- **agent는 조용히 폴백하지 않습니다.** agent 인증을 골랐는데 agent가 없으면 에러로
  끝납니다 — 엔트리에 비밀번호가 남아 있어도 쓰지 않습니다. 어떤 자격증명으로 열렸는지
  알 수 없게 되는 편이 더 나쁩니다. Windows에서는 `SSH_AUTH_SOCK`이 없으면 OpenSSH의
  기본 named pipe(`\\.\pipe\openssh-ssh-agent`)를 찾습니다. 같은 agent를 그 OS의 방식으로
  찾는 것과, 다른 자격증명으로 넘어가는 것은 다릅니다.
- **동기화는 옵트인입니다.** 설정하지 않으면 이 앱은 GitHub로 한 바이트도 나가지 않습니다.
  설정하면 **비공개 레포**에 `ssh-client.age` **파일 하나**만 올라갑니다(목록·비밀·known_hosts를
  통째로 암호화한 것). 토큰이 새더라도 호스트 이름 하나 드러나지 않고, 레포가 public이면
  push 직전에 확인해서 거절합니다.
- **Windows에서는 파일 권한이 보호가 아닙니다.** 유닉스에서는 설정 파일이 `0600`으로
  만들어지지만, Windows에서 ACL을 흉내 내지는 않습니다 — 지킬 수 없는 약속을 하지 않기
  위해서입니다. 그 플랫폼에서 비밀을 지키는 것은 **금고가 암호문이라는 사실 하나**입니다.

## macOS

배포 바이너리에는 **코드 서명도 공증도 하지 않았습니다**(유료 개발자 계정이 필요합니다).
대신 브라우저로 받는 형식(`.dmg`/`.pkg`)을 만들지 않았습니다 — `curl`/`tar`로 받은 파일에는
quarantine 속성이 붙지 않아 Gatekeeper가 막지 않습니다. 릴리스 페이지에서 브라우저로 직접
받았다면 `xattr -d com.apple.quarantine ./ssh-client`가 필요할 수 있습니다.

## 직접 빌드

```sh
go build ./...
go test ./internal/...   # 금고가 scrypt라 30초쯤 걸립니다
go run .
```

Go 1.25 이상이 필요합니다. cgo는 쓰지 않습니다(`CGO_ENABLED=0`).

## 라이선스

MIT — [LICENSE](LICENSE).
