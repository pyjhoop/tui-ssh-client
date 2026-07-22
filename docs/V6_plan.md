# TUI SSH Client — v6 구현 계획 (암호화 금고 · ssh-agent · 비공개 레포 동기화)

## Context (왜 만드는가)
v0부터 지금까지 **비밀번호는 평문으로 디스크에 있다.** `servers.json`의
`Password string \`json:"password,omitempty"\`` 그대로고, 폼에 붙여넣은 개인키도
`keys/<id>.pem`에 0600 평문으로 떨어진다. v0은 이걸 알고 한 결정이었다 — 대신 경고를 한 번
띄운다(`config.PlaintextWarningSeen`). 하지만 두 가지가 달라졌다:

1. **배포를 하려 한다.** `go install`로 남이 쓰는 순간, "설정 파일에 SSH 비밀번호가 평문"은
   더 이상 내 노트북의 문제가 아니다.
2. **접속정보를 여러 PC에서 쓰려 한다.** 평문 파일을 GitHub에 올리는 건 논외다.

v6는 이 둘을 한 번에 닫는다. **비밀은 암호화된 금고 안에만 존재하고**, 공유는 그 금고를 포함한
**번들 전체를 암호화한 파일 하나**를 비공개 레포에 올리는 것으로 한다.

그리고 애초에 저장할 비밀을 줄이기 위해 **ssh-agent 지원**을 같이 넣는다 — agent가 키를 들고
있으면 우리가 보관할 것이 아무것도 없다. 가장 안전한 비밀은 갖고 있지 않은 비밀이다.

## 범위
| 포함 | 제외 (→ 이후 버전) |
|---|---|
| age(scrypt) 기반 암호화 금고 — 비밀번호·개인키 | OS 키체인(Keychain/Credential Manager/Secret Service) |
| 평문 `password` / `keys/*.pem` 마이그레이션·제거 | 하드웨어 키(FIDO2/PIV), YubiKey |
| ssh-agent 인증 (`SSH_AUTH_SOCK`) | agent forwarding (`-A`) |
| 패스프레이즈로 잠긴 개인키 지원 | 점프호스트 / ProxyJump (v7로 이동) |
| 비공개 GitHub 레포 동기화 (push/pull, 명시적 실행) | 자동 동기화·백그라운드 폴링·다중 기기 실시간 병합 |
| 새 PC 부트스트랩 (`--pull`) | 팀 공유, 다중 사용자 금고, 키 회전 정책 |

## 확정된 결정 (임의로 뒤집지 말 것)

- **OS 키체인은 쓰지 않는다.** 두 가지 이유다: (1) 키체인에 넣은 값은 그 기기 밖으로 못 나가서
  이 버전의 두 번째 목표(동기화)와 정면으로 충돌한다. (2) 개발·실행 환경인 WSL2에는 Secret
  Service 데몬이 없어 `go-keyring`이 D-Bus 에러로 실패한다 — 우리가 테스트조차 못 하는 저장소를
  기본 경로로 삼을 수 없다. 로드맵 v6의 "OS 키체인" 항목을 **암호화 금고로 대체**한다.
- **암호화는 `filippo.io/age`의 scrypt(패스프레이즈) recipient**를 쓴다. Argon2id +
  XChaCha20-Poly1305를 직접 조립하지 않는다 — 감사받은 포맷과 20줄짜리 호출이 있는데
  직접 만든 KDF 파라미터·nonce 관리로 위험을 떠안을 이유가 없다. **암호 프리미티브를 직접
  구현하지 말 것.**
- **패스프레이즈는 어디에도 저장하지 않는다.** "이 기기에서 기억하기" 옵션을 만들지 않는다 —
  그 옵션이 있으면 금고는 평문 파일에 자물쇠 그림을 그려 넣은 것이 된다. 세션 동안 메모리에만
  두고, 잠금 해제는 앱 시작 시 한 번이다.
- **비밀이 없으면 패스프레이즈를 묻지 않는다.** 키 인증 + agent만 쓰는 사용자에게 프롬프트를
  강요하면 안 된다. 금고 파일이 존재할 때, 또는 비밀을 처음 저장할 때만 묻는다.
- **로컬은 메타데이터 평문 / 비밀 암호화로 나눈다.** `servers.json`은 사람이 손으로 고칠 수 있는
  파일로 남는다(이름·host·port·user·group). 비밀은 `vault.age` 안에만 있다.
  **공유 번들은 다르다** — 아래 항목.
- **공유 번들은 통째로 암호화한다.** 원격에 올라가는 것은 `ssh-client.age` **파일 하나**뿐이고,
  그 안에 `servers.json` + 비밀 + `known_hosts`가 전부 들어 있다. 비공개 레포라도 그것을
  신뢰하지 않는다 — 토큰이 새거나 실수로 public이 되어도 패스프레이즈 없이는 **호스트 이름
  하나도** 드러나지 않아야 한다. diff를 못 보는 것은 이 목적에 대해 지불할 만한 값이다.
- **동기화는 옵트인이다.** 기능을 켜기 전에는 네트워크로 나가는 코드가 **한 줄도 실행되지
  않는다.** 토큰도 레포 설정도 없고, 앱 시작 시 원격을 확인하지 않는다. 등록(`sync setup`)을
  해야 비로소 존재한다.
- **자동 동기화는 없다.** push도 pull도 사용자가 키를 눌러야 일어난다. 시작할 때마다 네트워크로
  나가면 오프라인·토큰 만료가 시작 경로를 오염시키고, 조용한 자동 push는 "실수로 지운 서버"를
  원격까지 지운다.
- **public 레포에는 절대 push하지 않는다.** 등록 시점과 **매 push 직전에** `GET /repos/{o}/{r}`의
  `private` 필드를 확인하고, false면 `ErrRepoPublic`으로 거절한다. 레포가 나중에 공개로
  바뀔 수 있으므로 등록 때 한 번 확인하는 것으로는 부족하다.
- **충돌은 병합하지 않는다.** GitHub Contents API의 `sha`로 낙관적 잠금을 건다. 원격이 더
  최신이면 push를 거절하고 "pull 먼저"라고 말한다. 접속정보를 자동 병합하다 잘못 합치는 것보다
  멈추는 게 낫다.
- **`known_hosts`만은 예외적으로 합집합 병합한다.** pull은 서버 목록·비밀을 통째로 교체하지만
  known_hosts는 로컬 + 원격의 합집합으로 만든다. 통째로 덮으면 이 기기에서만 승인한 호스트가
  사라지고 **다음 접속에서 TOFU 프롬프트가 다시 뜬다** — 사용자를 호스트키 승인에 무뎌지게
  만드는 것은 보안 회귀다. 같은 호스트에 **다른 키**가 있으면 로컬을 유지하고 화면에 알린다
  (그게 정확히 MITM이 보이는 모습이므로 조용히 고르면 안 된다).
- **`ui.json`(v5의 접힘·정렬 상태)은 동기화하지 않는다.** 기기마다 다른 게 자연스럽다.
- **ssh-agent가 있으면 그것을 먼저 쓴다.** `AuthAgent`를 새 인증 방식으로 추가하되, 기존
  `AuthKey`/`AuthPassword`의 의미는 바꾸지 않는다. agent 소켓이 없으면 명확한 에러
  (`ErrAgentUnavailable`)로 떨어지고, 조용히 다른 방식으로 넘어가지 않는다 — 어떤 자격증명이
  쓰였는지 사용자가 알 수 없게 되는 게 더 나쁘다.
- **마이그레이션은 자동이되 파괴적이지 않다.** 평문 `password`/`keys/*.pem`을 발견하면 잠금
  해제 후 금고로 옮기고 **원본을 지운다**. 지우기 전에 `servers.json.plaintext.bak`을 0600으로
  남기고, 상태줄이 그 경로를 알려준다. 저널링 파일시스템·SSD에서 삭제가 물리적 소거를
  보장하지 못한다는 점은 문서에 명시한다(우리가 해결할 수 있는 문제가 아니다).

---

## 배경 — 기존 코드에서 반드시 재사용할 것
- `internal/config/store.go`의 원자적 쓰기 패턴(`tmp` → `os.Rename`, 0600, `MkdirAll` 0700).
  `vault.age`도 **같은 방식**으로 쓴다. 새 저장 규약을 만들지 말 것.
- `Store.OwnsKey` — "우리가 만든 키만 우리 것"이라는 v1 규약. 마이그레이션이 `~/.ssh/id_ed25519`를
  금고로 빨아들이면 안 된다. **`OwnsKey`가 true인 것만 옮긴다.**
- `internal/ssh/errors.go`의 센티널 + `errorAdvice`. 새 실패(`ErrBadPassphrase`,
  `ErrRepoPublic`, `ErrSyncConflict`)도 **센티널로만** 분류한다. 문자열 매칭 금지.
- `internal/ssh/session.go:authMethods`. 여기에 갈래가 하나 늘 뿐이고, `Dial`이 유일한 네트워크
  진입점이라는 규약은 그대로다.
- 모달 규약(`confirm`/`pending`/`rename`이 모든 키를 먹는다). 잠금 해제 화면이 그 규약을
  가장 강하게 적용한 형태다.
- `internal/ssh/session_test.go`의 in-process SSH 서버 — agent 인증 테스트도 여기 붙인다.

## 의존성
- **신규**: `filippo.io/age` (순수 Go, 의존성 가벼움).
- `golang.org/x/crypto/ssh/agent` — 이미 있는 `x/crypto` 안에 들어 있다(새 모듈 아님).
- GitHub는 **`net/http`로 직접** 부른다. `go-github` SDK를 넣지 않는다 — 우리가 쓰는 것은
  엔드포인트 세 개뿐이다.

---

## 구현

### 1. 금고 — `internal/vault` (신규 패키지)

바이트만 다루는 순수 암호 계층이다. **경로도 파일도 모른다**(그건 `config`의 일).

```go
// Package vault encrypts a byte slice under a passphrase using age's scrypt
// recipient. It owns no files and knows no paths: config decides where the
// ciphertext lives, ui decides when to ask for the passphrase.
package vault

// Encrypt seals plain under passphrase. The work factor is deliberately high:
// this runs once at startup, not per keystroke.
func Encrypt(plain []byte, passphrase string) ([]byte, error)

// Decrypt returns ErrBadPassphrase for a wrong passphrase — the one error the
// UI must distinguish, because it is the only one the user can fix by retrying.
func Decrypt(cipher []byte, passphrase string) ([]byte, error)

var ErrBadPassphrase = errors.New("wrong passphrase")
```

금고의 평문 내용:
```go
// Secrets is everything we must never write unencrypted.
type Secrets struct {
    Version   int               `json:"version"`
    Passwords map[string]string `json:"passwords,omitempty"` // serverID → password
    Keys      map[string]string `json:"keys,omitempty"`      // serverID → private key PEM
    KeyPass   map[string]string `json:"key_pass,omitempty"`  // serverID → key passphrase
    GitHub    *GitHubAuth       `json:"github,omitempty"`    // 동기화 토큰 (옵트인 시에만)
}
```
`Version`은 지금 `1`이다. 뒤에 포맷이 바뀌어도 **복호화 후에** 마이그레이션할 수 있게 넣어 둔다.

### 2. 저장소 연결 — `internal/config/vaultstore.go` (신규)

```go
func (s *Store) VaultPath() string        // <dir>/vault.age
func (s *Store) HasVault() bool
func (s *Store) LoadSecrets(pass string) (vault.Secrets, error)
func (s *Store) SaveSecrets(sec vault.Secrets, pass string) error // 원자적, 0600
```
- `Server.Password`는 **더 이상 직렬화되지 않는다**: `json:"-"`로 바꾼다. 필드 자체는 남기므로
  `ssh.Connect`에 넘기는 경로는 그대로고, 값은 연결 직전에 금고에서 채운다.
- 개인키도 같다: `Server.KeyPEM []byte \`json:"-"\``를 추가하고, 우리가 만든 키는
  `KeyPath` 대신 금고의 `Keys[serverID]`에 산다. 사용자가 가리킨 경로(`~/.ssh/id_ed25519`)는
  **계속 `KeyPath`**다 — 남의 파일을 우리 금고로 빨아들이지 않는다.

### 3. 인증 경로 — `internal/ssh/session.go`

```go
// model
const AuthAgent AuthMethod = "agent"
```
`authMethods`에 갈래 추가:
- `AuthAgent`: `net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))` → `agent.NewClient(conn)` →
  `xssh.PublicKeysCallback(ag.Signers)`. 소켓이 없거나 못 열면 `ErrAgentUnavailable`.
- `AuthKey`: `srv.KeyPEM`이 있으면 **그것을 쓰고**, 없을 때만 `KeyPath`를 읽는다(기존 동작).
- 패스프레이즈 잠긴 키: 지금은 `PassphraseMissingError`를 "not supported yet"으로 돌려주는데,
  v6에서는 `xssh.ParsePrivateKeyWithPassphrase`로 금고의 `KeyPass[serverID]`를 써서 푼다.
  금고에 없으면 `ErrKeyPassphraseRequired` — UI가 한 줄 입력을 띄우고, 성공하면 금고에 넣는다.

`errors.go`에 센티널 추가: `ErrAgentUnavailable`, `ErrKeyPassphraseRequired`.
`errorAdvice`에 문구·액션 추가.

### 4. 잠금 해제 화면 — `internal/ui/unlock.go` (신규)

앱 시작 시 `store.HasVault()`이면 **다른 어떤 것도 그리기 전에** 잠금 화면을 띄운다.
```go
type unlockState struct {
    input   textinput.Model // EchoMode = EchoPassword
    attempt int
    err     string
}
```
- `App.unlock != nil`이면 `handleKey` 맨 앞에서 **모든 키를 먹는다**(모달 규약의 가장 강한 형태).
  마우스도 막는다. 사이드바·탭·SFTP는 그 뒤에서 그려지지도 않는다.
- 복호화는 scrypt라 수백 ms가 걸린다 → **`tea.Cmd`로 돌린다**(`unlockCmd` → `unlockedMsg`).
  `Update`에서 복호화하면 UI가 멈춘다. 진행 중에는 `unlocking…`.
- `ErrBadPassphrase`면 입력만 비우고 시도 횟수를 센다. **3회 실패하면 종료한다** — 무한 재시도를
  붙잡고 있을 이유가 없고, 오프라인 브루트포스는 어차피 파일을 가져가서 한다.
- 새로 금고를 만들 때(첫 비밀 저장)는 **패스프레이즈 2회 입력 + 강도 경고**. 8자 미만이면
  거부한다. 여기서의 패스프레이즈 강도가 이 설계의 보안 전부다.

### 5. 공유 번들 — `internal/config/bundle.go` (신규)

```go
// Bundle packs everything that defines "my servers" into one blob: the server
// list, the secrets and known_hosts. The caller encrypts it — nothing in this
// format is ever written or sent in the clear.
func (s *Store) Bundle(sec vault.Secrets) ([]byte, error)

// ApplyBundle replaces the local list and secrets with the bundle's, and
// merges known_hosts as a union (see ApplyBundle's doc for why it is the one
// thing we never replace wholesale).
func (s *Store) ApplyBundle(b []byte, sec *vault.Secrets) (ApplyReport, error)
```
번들 포맷은 **JSON 하나**다(tar를 쓰지 않는다 — 멤버가 셋뿐이고 스트리밍이 필요 없다):
```json
{
  "version": 1,
  "updated_at": "2026-07-23T10:00:00Z",
  "device": "laptop",
  "servers": [ … ],
  "secrets": { … },
  "known_hosts": "…\n…\n"
}
```
`ApplyReport`는 화면에 뜰 요약이다: 서버 n개 교체, known_hosts m줄 추가, **호스트키가 충돌해
로컬을 유지한 호스트 목록**.

### 6. 동기화 — `internal/sync` (신규 패키지)

HTTP만 하는 얇은 계층. 파일도 금고도 모른다.
```go
// Package sync talks to the GitHub Contents API. It moves opaque bytes: the
// caller has already encrypted them, and this package must never be given
// anything it could leak in the clear.
package sync

type Repo struct { Owner, Name, Path, Branch string }

type Remote struct{ Token string; HTTP *http.Client }

// Check verifies the repo exists and is private. Called at setup AND before
// every push: a repo can be flipped to public after we registered it.
func (r *Remote) Check(repo Repo) error   // ErrRepoPublic / ErrRepoNotFound / ErrBadToken

// Get returns the ciphertext and its blob sha (the optimistic lock).
func (r *Remote) Get(repo Repo) (data []byte, sha string, err error)

// Put uploads under the sha we last saw. A mismatch is ErrSyncConflict: the
// remote moved on, and we refuse to merge server lists automatically.
func (r *Remote) Put(repo Repo, data []byte, sha, message string) (newSha string, err error)
```
- 엔드포인트는 셋뿐: `GET /repos/{o}/{r}`, `GET|PUT /repos/{o}/{r}/contents/{path}`.
  `Authorization: Bearer <token>`, `Accept: application/vnd.github+json`.
- 타임아웃 필수(`http.Client{Timeout: 30s}`). 재시도는 하지 않는다.
- **토큰은 로그·에러 문자열에 절대 넣지 않는다.** 에러는 상태코드로만 분류한다
  (401/403 → `ErrBadToken`, 404 → `ErrRepoNotFound`, 409 → `ErrSyncConflict`).

### 7. 동기화 UX — `internal/ui/sync.go` (신규)

**설정(옵트인)**: 사이드바에서 `Y` → 우측에 `rightSync` 폼. 입력은 `owner/repo`, 경로
(기본 `ssh-client.age`), 토큰. 저장 전에 `Check`를 돌려 **private임을 확인**하고, 통과하면
토큰을 금고에 넣는다. 여기까지 하지 않으면 동기화 코드는 실행되지 않는다.

권장 토큰: fine-grained PAT, **그 레포 하나에 Contents: Read and write만**. 폼 안내문에 적는다.

| 키 (사이드바) | 동작 |
|---|---|
| `Y` | 동기화 설정 (없으면 등록, 있으면 상태·재설정) |
| `S` | push — 로컬 → 원격 |
| `P` | pull — 원격 → 로컬 (확인 패널을 거친다) |

- push: `Check` → `Bundle` → `vault.Encrypt` → `Put(sha)`. 성공하면 상태줄에
  `synced · 12 servers · 2026-07-23 19:04`.
- pull: `Get` → `Decrypt` → **미리보기 확인 패널**("서버 12개를 원격 버전으로 교체한다,
  로컬 백업: …"). `enter`를 눌러야 적용한다. 적용 전 `ssh-client.local.bak`을 남긴다.
- `ErrSyncConflict`면 push를 멈추고 `remote is newer — pull first (P)`.
- **네트워크는 전부 `tea.Cmd`**다. `Update`에서 HTTP를 부르지 말 것.
- 세션 탭은 동기화에 영향받지 않는다 — 열린 탭이 가리키는 서버가 pull로 사라져도 탭은 살아
  있다(연결은 이미 맺어졌다). 사이드바만 다시 그린다.

### 8. 새 PC 부트스트랩 — `main.go`

UI를 띄우기 전에 서버가 있어야 하므로 플래그 하나를 둔다:
```bash
GITHUB_TOKEN=… ssh-client --pull      # 패스프레이즈 입력 → 로컬 구성 생성 → 그대로 UI 시작
```
- 토큰 출처는 **금고 → `GITHUB_TOKEN` 환경변수 → `gh auth token`** 순이다. 첫 PC가 아니면
  금고가 없으니 환경변수·`gh`가 유일한 진입이다(닭-달걀 문제를 여기서 푼다).
- `--pull`은 레포 좌표도 필요하다: `--repo owner/name`(첫 실행), 이후에는 금고에 남는다.

### 9. 마이그레이션 — `internal/config/migrate.go` (신규)

시작 시 `servers.json`에 `password`가 있거나 `KeysDir()`에 pem이 있으면:
1. 없으면 금고 생성 프롬프트(§4), 있으면 잠금 해제.
2. `servers.json.plaintext.bak`(0600)을 남긴다.
3. 비밀번호 → `Secrets.Passwords`, **`OwnsKey`가 true인 pem만** → `Secrets.Keys`.
4. 금고 저장이 **성공한 뒤에** `servers.json`을 비밀 없이 다시 쓰고 pem을 지운다.
   순서를 뒤집지 말 것 — 중간에 죽으면 비밀이 사라진다.
5. 상태줄: `migrated 4 passwords, 2 keys into the vault · plaintext backup: <path>`.
6. `.plaintext-warning-ack`는 지운다(그 경고는 더 이상 참이 아니다).

### 10. 상태 전이 (v5에 추가되는 부분)
```
start ──(금고 있음)──▶ unlock ──(성공)──▶ 기존 empty
                          └──(3회 실패)──▶ 종료
start ──(평문 발견)──▶ unlock|create ──▶ migrate ──▶ empty + 결과 상태줄
sidebar ──(Y)──▶ rightSync(설정) ──(Check 통과)──▶ empty + 토큰 금고 저장
sidebar ──(S)──▶ pushing ──▶ empty + 결과 / ErrSyncConflict 안내
sidebar ──(P)──▶ pulling ──▶ confirm(교체 미리보기) ──(enter)──▶ empty + 목록 리로드
```

---

## 변경 / 추가 파일

| 파일 | 내용 |
|---|---|
| `internal/vault/vault.go` | **신규** `Encrypt`/`Decrypt`(age scrypt), `Secrets`, `ErrBadPassphrase` |
| `internal/config/vaultstore.go` | **신규** `VaultPath`/`HasVault`/`LoadSecrets`/`SaveSecrets` |
| `internal/config/bundle.go` | **신규** `Bundle`/`ApplyBundle`/`ApplyReport`(known_hosts 합집합) |
| `internal/config/migrate.go` | **신규** 평문 → 금고 이관, 백업, 원본 제거 |
| `internal/sync/github.go` | **신규** `Repo`/`Remote`/`Check`/`Get`/`Put` + 센티널 |
| `internal/model/server.go` | `Password`를 `json:"-"`로, `KeyPEM []byte json:"-"`, `AuthAgent` |
| `internal/ssh/session.go` | `authMethods`에 agent 갈래, `KeyPEM` 우선, 잠긴 키 패스프레이즈 |
| `internal/ssh/errors.go` | `ErrAgentUnavailable`, `ErrKeyPassphraseRequired` |
| `internal/ui/unlock.go` | **신규** 잠금 해제·금고 생성 화면 (모든 키를 먹는다) |
| `internal/ui/sync.go` | **신규** 동기화 설정 폼, push/pull 커맨드·확인·결과 |
| `internal/ui/app.go` | `rightSync`/`focusSync`, `unlock` 게이트, 사이드바 `Y`/`S`/`P`, 연결 직전 금고에서 비밀 주입 |
| `internal/ui/form.go` | 인증 방식에 `agent` 추가, 비밀번호는 금고로 저장 |
| `internal/ui/errorcard.go` | 새 센티널 문구·액션 |
| `main.go` | `--pull` / `--repo` 플래그 |
| `docs/V0_plan.md` | 로드맵 v6 항목 갱신(키체인 → 금고+동기화), 점프호스트를 v7로 |
| `CLAUDE.md` | 구현 시 갱신 — "평문 저장" 규약 폐기, 금고·동기화 규칙, 옵트인 원칙 |

## 검증 (end-to-end)

**자동 테스트** (`go test ./internal/...`)
1. `internal/vault/vault_test.go`
   - `TestRoundTrip`, `TestWrongPassphraseIsErrBadPassphrase`,
     `TestCiphertextLeaksNothing`: 암호문에 host·user·password 문자열이 **바이트로도** 없다.
2. `internal/config/vault_test.go` / `bundle_test.go` / `migrate_test.go`
   - `TestVaultFileIs0600`, `TestSaveSecretsIsAtomic`(중간 실패 시 원본 유지).
   - `TestServersJSONHasNoPassword`: 저장 후 파일에 `password` 키가 **없다**.
   - `TestMigrationMovesPlaintextAndDeletesIt`: 이관 후 `servers.json`에 비밀이 없고
     `keys/*.pem`이 사라졌으며 백업이 0600으로 남는다.
   - `TestMigrationLeavesUserKeysAlone`: `~/.ssh/id_ed25519`를 가리키는 `KeyPath`는
     금고로 들어가지 않고 파일도 그대로다(`OwnsKey` 규약).
   - `TestBundleRoundTrip`, `TestApplyMergesKnownHosts`: 합집합이 되고 중복이 없다.
   - `TestConflictingHostKeyKeepsLocal`: 같은 호스트에 다른 키면 **로컬 유지** +
     `ApplyReport`에 보고된다.
3. `internal/sync/github_test.go` — `httptest.Server`로 GitHub를 흉내 낸다.
   - `TestRefusesPublicRepo`: `private:false` → `ErrRepoPublic`, **`Put`이 호출되지 않는다**.
   - `TestPushSendsShaAndConflictIs409`: 낡은 sha → `ErrSyncConflict`.
   - `TestTokenNeverAppearsInError`: 모든 에러 문자열에 토큰 문자열이 없다.
   - `TestUploadsCiphertextOnly`: 업로드 본문을 base64 디코드해도 평문 필드가 없다.
4. `internal/ssh/session_test.go` 확장
   - `TestAgentAuth`: `agent.NewKeyring`을 유닉스 소켓에 붙이고 `SSH_AUTH_SOCK`을 가리켜
     in-process 서버에 접속된다.
   - `TestMissingAgentIsErrAgentUnavailable`: 소켓이 없으면 **다른 방식으로 넘어가지 않는다**.
   - `TestKeyPEMFromVaultBeatsKeyPath`, `TestEncryptedKeyUsesStoredPassphrase`.
5. `internal/ui`
   - `TestUnlockSwallowsEverything`: 잠금 상태에서 `q`/`alt+2`/마우스가 **아무것도 하지 않는다**.
   - `TestWrongPassphraseThriceQuits`.
   - `TestNoVaultNoPrompt`: 비밀이 없으면 잠금 화면이 뜨지 않는다.
   - `TestSyncDisabledMakesNoRequests`: 옵트인 전에는 HTTP 클라이언트가 **한 번도** 안 불린다
     (주입한 fake transport의 호출 수 0).
   - `TestPullAsksBeforeReplacing`, `TestPullKeepsOpenTabs`.
   - `TestLayoutAlignmentWithUnlockAndSync`: 두 화면에서도 모든 행이 정확히 width.
6. `go vet ./...`, `go build ./...`, `go test -race ./internal/...` 통과.

**수동 확인 (v6 수용 기준 — 자동화하지 않음)**
1. 평문 `servers.json`(비밀번호 + 우리 pem 2개)으로 시작 → 패스프레이즈 생성 → 이관 후
   파일을 열어 **비밀번호가 안 보이고** `keys/`가 비었는지 눈으로 확인. 그 서버로 실제 접속된다.
2. 앱 재시작 → 잠금 화면 → 맞는 패스프레이즈로 열리고, 틀리면 3회 후 종료.
3. `ssh-add`로 키를 올린 뒤 인증 방식 `agent`로 접속 → 금고에 아무것도 저장되지 않는다.
   `SSH_AUTH_SOCK`을 지우고 다시 시도하면 명확한 에러가 뜬다.
4. 패스프레이즈로 잠긴 키 등록 → 첫 접속에서 한 번 묻고, 두 번째부터는 묻지 않는다.
5. 비공개 레포를 등록하고 `S` → GitHub 웹에서 `ssh-client.age`가 **읽을 수 없는 이진 파일**로
   올라간 것을 확인. 레포를 public으로 바꾸고 다시 `S` → **거절된다.**
6. 다른 PC(또는 `XDG_CONFIG_HOME`을 바꾼 빈 디렉터리)에서
   `GITHUB_TOKEN=… ssh-client --pull --repo o/n` → 패스프레이즈 입력 → 서버 목록이 그대로 뜨고
   실제 접속까지 된다.
7. 두 기기에서 각각 수정 후 둘 다 `S` → 뒤에 올린 쪽이 `remote is newer — pull first`로 막힌다.
8. A 기기에서만 승인한 호스트키가 B에서 pull한 뒤에도 **살아 있다**(합집합 확인).
9. 동기화를 등록하지 않은 상태로 앱을 켜고 종료 → 네트워크 트래픽이 GitHub로 나가지 않는다
   (`ss`/방화벽 로그로 확인).

## v6에서 하지 않는 것 (의도적 제외)
- OS 키체인·하드웨어 키(FIDO2/PIV) — 동기화와 충돌하고 WSL2에서 테스트 불가
- agent forwarding, 점프호스트/ProxyJump → **v7**
- 자동·백그라운드 동기화, 실시간 병합, 팀 공유 금고
- 패스프레이즈 기억/생체 인증 — 있으면 금고가 무의미해진다
- 키 회전·만료 정책, 감사 로그
