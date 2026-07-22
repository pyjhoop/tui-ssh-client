# TUI SSH Client — v5 구현 계획 (목록 UX — 검색/필터 · 그룹/폴더 · `~/.ssh/config` import)

## Context (왜 만드는가)
v4까지로 "붙어서 일하기"는 끝났다. 남은 병목은 **붙기 전**, 즉 사이드바다.

- 사이드바는 **평평한 목록 하나**다(`newSidebar` → `list.New(itemsFor(servers, nil), …)`).
  서버가 40개가 되면 화살표로 훑는 수밖에 없다.
- 필터는 **꺼져 있다** — `internal/ui/sidebar.go`의 `l.SetFilteringEnabled(false)`.
  bubbles 목록이 필터 입력 상태를 갖는 순간 `n`/`e`/`d`/`f` 같은 **한 글자 키가 전부
  필터 문자로 먹히기** 때문에 v0에서 의도적으로 껐다. 이 충돌을 먼저 풀어야 켤 수 있다.
- 그룹이 없다. `model.Server`에는 이름·호스트·인증뿐이고, `prod-web-1`, `prod-web-2`,
  `staging-…`을 묶는 수단이 이름 접두사밖에 없다.
- 이미 `~/.ssh/config`에 서버가 다 있는 사람도 **폼으로 한 대씩 다시 입력해야 한다.**

v5는 이 셋을 닫는다. **연결 계층은 한 줄도 건드리지 않는다** — `ssh`/`sftp` 패키지,
탭·재연결(`tabs.go`), 렌더 파이프라인은 그대로다. 바뀌는 것은 `model.Server`에 필드 하나,
`config`의 파서 하나, 그리고 사이드바다.

## 범위
| 포함 | 제외 (→ 이후 버전) |
|---|---|
| `/` 검색 필터 (이름·user·host·그룹 매칭) | 정규식/퍼지 스코어링 필터 — 부분 문자열이면 충분 |
| 그룹(폴더) — 접기/펴기, 접힘 상태 영속 | 중첩 그룹(`a/b/c`), 드래그로 그룹 이동 (v7) |
| `~/.ssh/config` import (미리보기 후 선택 import) | `Include`·`Match`·와일드카드 `Host *` 해석 |
| 사이드바 1행 delegate로 전환 | ProxyJump / 점프호스트 (v6) |
| 폼에 Group 필드 | 서버별 색·아이콘, 정렬 규칙 설정 (v7) |
| 최근 접속 순 정렬 옵션 | 서버 목록 내보내기(export) |

## 확정된 결정 (임의로 뒤집지 말 것)

- **사이드바는 항목당 1행이 된다.** 지금은 bubbles의 기본 delegate라 항목당 3행
  (`sidebarRowsPerItem = 3`: 제목 + 설명 + 빈 줄)이다. 그룹 헤더를 넣으려면 헤더와 서버 행의
  높이가 달라야 하는데, `list.ItemDelegate.Height()`는 **모든 항목에 하나의 값**이라
  가변 높이를 지원하지 않는다. 헤더를 3행으로 부풀리느니 전체를 1행으로 줄인다 —
  `filePane`이 이미 1행/항목이라 프로젝트 안에 선례가 있고, 화면에 들어가는 서버 수가 3배가 된다.
  행 형식은 `● web-1  deploy@10.0.0.1`(마커 · 이름 · 흐린 detail, 폭이 모자라면 detail부터 버림).
  `sidebarRowsPerItem = 1`로 바뀌면 **`rowToIndex`도 같이 바뀐다** — 마우스 클릭이 보이는 곳에
  떨어지는지 테스트가 고정한다(`TestSidebarClickHitsVisibleRow`).
- **필터 중에는 사이드바가 모달처럼 군다.** `list.FilterState() != list.Unfiltered`이면
  사이드바 키 분기(`n`/`e`/`d`/`f`/`enter`)를 **전부 건너뛰고** 키를 목록에 그대로 넘긴다.
  `App.confirm`/`pending`/`rename`이 "떠 있으면 모든 키를 먹는다"는 기존 규약의 재사용이다 —
  새 규칙을 만드는 게 아니라 같은 규칙을 하나 더 적용한다. 필터 확정(`enter`) 후에는
  `FilterApplied` 상태라 단축키가 다시 산다.
- **필터는 부분 문자열 하나로만 매칭한다** — 이름 / `user@host` / 그룹을 소문자로 이어 붙인
  문자열에 `strings.Contains`. bubbles 기본 필터(Levenshtein 유사 랭킹)를 쓰면 `prod`가
  `podman-host`를 끌어올리는 식으로 순서가 흔들린다. `list.SetFilterFunc`로 우리 함수를 꽂고,
  **순서는 원래 목록 순서를 유지한다**(랭킹 없음).
- **그룹은 한 단계뿐이고, `Server.Group` 문자열 하나로 표현한다.** 별도의 그룹 테이블·ID를
  만들지 않는다 — 그룹은 "같은 문자열을 가진 서버들"일 뿐이라 이름을 바꾸면 그 자리에서
  옮겨지고, 마지막 서버가 나가면 그룹은 저절로 사라진다. 빈 그룹을 저장하지 않는다.
  중첩(`prod/web`)은 슬래시를 그냥 이름의 일부로 취급한다(트리를 만들지 않는다 → v7).
- **그룹 없는 서버가 맨 위, 그룹은 그 아래 이름순.** 그룹이 하나도 없으면 화면은 v4와
  **완전히 같다**(헤더 행이 하나도 안 생긴다). 그룹을 안 쓰는 사용자는 v5로 올려도
  달라진 게 없어야 한다.
- **접힘 상태는 `servers.json`에 넣지 않는다.** 서버 목록은 사용자가 손으로 고칠 수 있는
  데이터고, 접힘은 UI 잔여물이다. `${config}/ui.json`에 `{"collapsed":["prod"]}`로 따로 쓰고,
  **읽기 실패는 조용히 무시한다**(기본값 = 전부 펼침). 이 파일이 없어도 앱은 정상 동작해야 한다.
- **`~/.ssh/config` 파서는 직접 쓴다(`internal/config/sshconfig.go`).** 새 의존성을 넣지 않는다.
  읽는 키워드는 **`Host` / `HostName` / `User` / `Port` / `IdentityFile` 넷뿐**이고,
  나머지는 전부 무시한다. `Include`·`Match`·와일드카드 패턴(`Host *`, `Host web-?`)은
  **건너뛴다** — 지원하는 척하면 "OpenSSH에서는 붙는데 여기서는 안 붙는" 조용한 불일치가
  생긴다. 건너뛴 항목은 import 미리보기에 **이유와 함께 회색으로 남겨** 보여준다.
- **import는 절대 자동으로 돌지 않는다.** 시작할 때 몰래 읽어 목록을 채우지 않는다.
  사용자가 사이드바에서 `i`를 눌러 미리보기를 열고, `space`로 고르고, `enter`로 가져온다.
  중복(같은 `user`+`host`+`port`)은 기본 해제 상태로 표시하고 `dup` 배지를 단다.
- **IdentityFile은 복사하지 않는다.** 폼에 붙여넣은 키 본문만 `keys/`에 0600으로 저장한다는
  v0 결정은 그대로다. import는 **경로만** `KeyPath`에 적는다 — 그 파일은 사용자와 OpenSSH의
  것이고, 우리가 사본을 만들면 원본이 교체됐을 때 조용히 옛 키를 쓰게 된다.
- **import한 서버도 그냥 서버다.** "ssh_config에서 왔음" 플래그로 동기화하거나 다시 덮어쓰지
  않는다. 가져온 뒤에는 우리 `servers.json`이 유일한 사본이고, 편집·삭제가 자유롭다.
  기본 그룹만 `ssh_config`로 넣어 준다(사용자가 바꿔도 무방).

---

## 배경 — 기존 코드에서 반드시 재사용할 것
- `internal/ui/sidebar.go`의 `item` / `itemsFor` / `SetOpen`. **탭 마커(`●`, `(2)`)는 v4 동작
  그대로 유지**된다 — 1행 delegate로 바뀌어도 마커는 같은 자리에 있어야 한다.
- `internal/config/store.go`의 `Load`/`Save`/`Dir()` 규약. `ui.json`도 같은 디렉터리에
  같은 방식(원자적 쓰기)으로 쓴다. 새 저장소 개념을 만들지 말 것.
- `internal/ui/form.go`. Group 필드는 **입력 한 줄 추가**일 뿐이다. 폼의 레이아웃 계산과
  마우스 좌표 변환은 이미 필드 수를 기준으로 돌므로, 상수를 하나 늘리는 것으로 끝나야 한다.
- `filePane`(`internal/ui/sftp.go`)의 1행 렌더 + 좌우 폭 배분 방식 — 사이드바 delegate가
  참고할 선례다(마커 · 이름 · 오른쪽 정렬 detail · `padLine`).
- `rowToIndex`와 그 상수(`topMargin`/`borderSize`/`padX`/`padY`). 상수는 **여전히 유일한
  출처**이고, 바뀌는 것은 `sidebarRowsPerItem` 하나뿐이다.

## 의존성
새 외부 의존성 없음. `bufio`/`strings`(파서), 이미 있는 `bubbles/list`·`textinput`.

---

## 구현

### 1. 모델 — `internal/model/server.go`

```go
type Server struct {
    // …기존 필드…
    Group    string    `json:"group,omitempty"`
    LastUsed time.Time `json:"last_used,omitempty"` // 정렬 옵션용, 없으면 zero
}

// FilterKey is the haystack the sidebar filter matches against: name, user,
// host and group in one lowercased string, so "prod db" style typing works.
func (s Server) FilterKey() string
```
- `Group`이 빈 문자열이면 "그룹 없음"이다. `omitempty` 덕에 기존 `servers.json`은 그대로 읽힌다
  (마이그레이션 코드 불필요 — `TestLoadsV4File`이 고정).
- `LastUsed`는 탭을 **성공적으로 연** 시점에만 갱신한다(연결 실패는 아니다).

### 2. UI 상태 저장 — `internal/config/uistate.go` (신규)

```go
// UIState is view-only sludge: it must never hold anything the user would miss
// if the file were deleted.
type UIState struct {
    Collapsed []string `json:"collapsed"`
    SortRecent bool    `json:"sort_recent"`
}

func (s *Store) LoadUIState() UIState   // 파일 없음/깨짐 → zero value, 에러 아님
func (s *Store) SaveUIState(UIState) error
```

### 3. `~/.ssh/config` 파서 — `internal/config/sshconfig.go` (신규)

```go
// SSHConfigEntry is one importable Host block. Skip is set when we deliberately
// refuse it, and Reason says why — the preview shows both.
type SSHConfigEntry struct {
    Alias    string
    Host     string
    User     string
    Port     int
    Identity string
    Skip     bool
    Reason   string // "wildcard pattern", "no HostName", "Include not supported"
}

// ParseSSHConfig reads path and returns one entry per Host block, in file
// order. A missing file is not an error: it returns nil, nil.
func ParseSSHConfig(path string) ([]SSHConfigEntry, error)
```
- 파싱 규칙: 줄 단위, `#` 주석 제거, `키 값` 또는 `키=값`, 키는 대소문자 무시.
  `Host` 줄이 새 블록을 시작하고, 그 다음 `HostName`/`User`/`Port`/`IdentityFile`만 채운다.
- `Host` 값에 `*`/`?`/`!`가 있거나 토큰이 여러 개면 `Skip=true, Reason="wildcard pattern"`.
- `HostName`이 없으면 `Host` 별칭을 호스트로 쓴다(OpenSSH와 같다). 별칭도 없으면 skip.
- `Identity`의 `~`는 홈으로 펼친다. **파일 존재 여부는 확인하지 않는다** — 없는 경로도
  그대로 적고, 연결 실패는 기존 에러 카드가 설명한다.
- `Include`를 만나면 그 줄만 `Skip` 항목으로 기록하고 계속 읽는다.
- 변환은 UI가 아니라 여기서: `func (e SSHConfigEntry) Server() model.Server` —
  `Auth`는 `Identity != ""`면 `AuthKey`, 아니면 `AuthPassword`(빈 비밀번호 → 첫 연결 시
  기존 에러 경로가 폼으로 유도한다). `Group`은 `"ssh_config"`.

### 4. 사이드바 — `internal/ui/sidebar.go`

#### 4-1. 1행 delegate
```go
// rowDelegate draws one line per entry. bubbles' default delegate is three rows
// tall, which leaves no room for group headers (Height() is per-delegate, not
// per-item) and wastes two thirds of the sidebar.
type rowDelegate struct{ width int }

func (rowDelegate) Height() int  { return 1 }
func (rowDelegate) Spacing() int { return 0 }
```
- 렌더: `marker + name`을 왼쪽, `user@host`를 오른쪽에 흐리게, 남는 폭이 없으면 detail 생략,
  마지막은 `padLine`. 선택 행은 기존 `colorAccent` 스타일을 그대로 쓴다.
- `sidebarRowsPerItem = 1`, `rowToIndex`는 `rel`을 그대로 인덱스로 쓴다(리스트 헤더 오프셋은
  기존 계산 유지).

#### 4-2. 그룹 행
`item`에 종류를 하나 더한다:
```go
type item struct {
    connect  bool
    header   bool   // 그룹 헤더 행
    group    string // header면 그룹 이름, 서버면 소속
    collapsed bool
    count    int    // header: 그 그룹의 서버 수
    server   model.Server
    sessions int
}
```
`itemsFor`가 평평한 목록을 만든다: `+ Connect` → 그룹 없는 서버들 → 그룹별 `▸/▾ prod (4)` +
(펼쳐졌으면) 그 서버들. **접힌 그룹의 서버는 목록에 아예 넣지 않는다** — 숨기는 게 아니라
없는 것으로 만들어야 `list`의 커서·페이지 계산이 맞는다.

- 헤더에서 `enter`/`space`/`←`/`→`: 접기·펴기 → `UIState.Collapsed` 갱신 → `SaveUIState`.
- 헤더에서 `e`/`d`/`n`/`f`: **아무것도 하지 않는다**(서버 대상 동작이다). `it.connect`를
  거르는 기존 분기에 `it.header`를 같이 넣는다.
- 접힌 그룹 안에 **열린 탭이 있으면 헤더에 `●`**를 단다 — 접어 둔 채로도 세션이 살아 있다는 걸
  알 수 있어야 한다.

#### 4-3. 필터
- `l.SetFilteringEnabled(true)`, `l.SetFilterFunc(containsFilter)`, `l.FilterInput.Prompt = "/ "`.
- **필터가 걸린 동안에는 그룹 헤더를 만들지 않는다**(평평한 결과만). 헤더가 매칭에 끼어들면
  "검색 결과"라는 개념이 흐려진다.
- `App.handleKey`의 사이드바 분기 맨 앞:
  ```go
  if a.sidebar.Filtering() { // FilterState() != list.Unfiltered
      return a.sidebar.Update(msg)
  }
  ```
  `q`/`ctrl+c` 종료도 여기서는 넘기지 않는다 — 필터에 `q`를 못 치면 검색이 아니다.
  종료는 필터를 `esc`로 닫고 하면 된다(`TestFilterSwallowsShortcutKeys`가 고정).

#### 4-4. 정렬
`UIState.SortRecent`가 켜져 있으면 각 그룹 안에서 `LastUsed` 내림차순, 아니면 저장 순서.
토글 키는 사이드바 `s`. 그룹 자체의 순서는 항상 이름순이다.

### 5. 폼 — `internal/ui/form.go`
`Name` 아래에 `Group` 입력 한 줄을 추가한다. placeholder는 **이미 존재하는 그룹 목록**을
콤마로 이어 보여준다(자동완성은 없다 — 한 줄 입력에 팝업을 붙이지 않는다).
저장 시 `strings.TrimSpace`. 필드 수 상수 하나만 늘고, 폼 클릭 좌표 변환은 그대로여야 한다
(`TestFormClickHitsField`가 고정).

### 6. import 화면 — `internal/ui/importer.go` (신규)

새 우측 모드 하나를 추가한다: `rightMode = rightImport`, `focus = focusImport`.
```go
type importer struct {
    entries []importRow // config.SSHConfigEntry + marked bool + dup bool
    cursor  int
    path    string
}
```
- 진입: 사이드바에서 `i`. 파싱은 파일 IO이므로 **`tea.Cmd`로** 돌린다
  (`parseSSHConfigCmd` → `sshConfigParsedMsg`). `Update`에서 파일을 읽지 말 것.
- 렌더는 `filePane`과 같은 1행/항목: `[x] web-1   deploy@10.0.0.1:22   ~/.ssh/id_ed25519`.
  skip 항목은 `[-]`에 흐린 이유, 중복은 `dup` 배지 + 기본 해제.
- 키: `space` 토글 / `a` 전체 선택·해제 / `enter` import 실행 / `esc` 취소.
  **모달 규약**: `focusImport`인 동안 탭 키(`tabKey`)와 사이드바 키는 죽는다.
- import 실행은 `store.Save`를 **한 번만** 부른다(항목마다 저장하면 N번 쓴다).
  결과는 상태줄에 `imported 6 servers (2 skipped)`.
- 파일이 없으면 진입하지 않고 상태줄에 `no ~/.ssh/config`.

### 7. 키맵 (v5에서 추가되는 것)

| 키 (사이드바) | 동작 |
|---|---|
| `/` | 필터 시작 (이후 모든 키는 필터가 먹는다) |
| `esc` | 필터 해제 |
| `enter` / `space` (그룹 헤더에서) | 접기 / 펴기 |
| `←` / `→` (그룹 헤더에서) | 접기 / 펴기 |
| `i` | `~/.ssh/config` import 미리보기 |
| `s` | 최근 접속 순 정렬 토글 |

기존 키(`enter`·`n`·`e`·`d`·`f`·`q`)의 의미는 **하나도 바뀌지 않는다**. `alt+*` 탭 키도 그대로다.

### 8. 상태 전이 (v4에 추가되는 부분)
```
empty|any ──(사이드바 /)──▶ sidebar[filtering] ──(esc)──▶ sidebar
                              └──(enter)──▶ sidebar[filtered] (단축키 복귀)
sidebar ──(헤더에서 enter)──▶ 접힘 토글 (+ ui.json 저장)
sidebar ──(i)──▶ rightImport(parsing) ──(파싱 완료)──▶ rightImport
rightImport ──(enter)──▶ empty + 목록 리로드 + 상태줄 결과
            └──(esc)──▶ 이전 rightMode 복귀 (세션 탭은 건드리지 않는다)
```

---

## 변경 / 추가 파일

| 파일 | 내용 |
|---|---|
| `internal/model/server.go` | `Group`·`LastUsed` 필드, `FilterKey()` |
| `internal/config/uistate.go` | **신규** `UIState` / `LoadUIState` / `SaveUIState` |
| `internal/config/sshconfig.go` | **신규** `ParseSSHConfig` / `SSHConfigEntry.Server()` |
| `internal/ui/sidebar.go` | `rowDelegate`(1행), 그룹 헤더 항목, 접힘 상태, 필터 함수·`Filtering()`, 정렬 |
| `internal/ui/importer.go` | **신규** import 미리보기 모델·렌더·키 |
| `internal/ui/app.go` | `rightImport`/`focusImport`, 필터 중 키 위임, 사이드바 `i`·`s`, `sidebarRowsPerItem = 1`에 맞춘 `rowToIndex`, 연결 성공 시 `LastUsed` 갱신 |
| `internal/ui/form.go` | `Group` 입력 한 줄 |
| `internal/ui/styles.go` | 그룹 헤더·흐린 detail·`dup` 배지 스타일 |
| `docs/V0_plan.md` | 로드맵 v5 항목에 이 문서 링크 |
| `CLAUDE.md` | 구현 시 갱신 — 1행 delegate와 `rowToIndex`, 필터의 모달 규약, 그룹 = `Server.Group` 문자열, `ui.json`은 지워도 되는 파일 |

## 검증 (end-to-end)

**자동 테스트** (`go test ./internal/...`)
1. `internal/config/sshconfig_test.go`
   - `TestParsesHostBlocks`: `HostName`/`User`/`Port`/`IdentityFile`이 정확히 붙는다.
     `키=값` 형식과 대소문자 혼용(`hostname`)도 같다.
   - `TestWildcardAndIncludeAreSkipped`: `Host *`, `Host web-?`, `Include x`가 `Skip=true`이고
     **이유 문자열이 채워진다**. 나머지 블록은 계속 파싱된다.
   - `TestMissingFileIsNotAnError`: 없는 경로 → `nil, nil`.
   - `TestIdentityTildeExpanded`, `TestHostNameDefaultsToAlias`.
2. `internal/config/uistate_test.go`
   - `TestUIStateRoundTrip`, `TestCorruptUIStateIsIgnored`(깨진 JSON → zero value, 에러 없음).
   - `TestLoadsV4ServersFile`: `group` 키가 없는 v4 `servers.json`이 그대로 읽힌다.
3. `internal/ui/sidebar_test.go` (신규)
   - `TestNoGroupsLooksLikeV4`: 그룹이 하나도 없으면 헤더 행이 0개다.
   - `TestCollapsedGroupHidesItsServers`: 접으면 목록 길이가 줄고, 다시 펴면 원래대로.
     접힌 그룹에 열린 탭이 있으면 헤더에 `●`.
   - `TestFilterMatchesNameUserHostGroup`: `prod`가 그룹 `prod`의 서버와 이름에 `prod`가 든
     서버를 모두 잡고, **원래 순서를 유지**한다.
   - `TestFilterSwallowsShortcutKeys`: 필터 중 `d`/`n`/`q`가 삭제·새 세션·종료를 **일으키지 않고**
     필터 문자열에 들어간다. `esc` 후에는 다시 동작한다.
   - `TestSidebarClickHitsVisibleRow`: 1행 delegate에서 화면 y좌표 → 인덱스가 맞는다
     (헤더 행이 섞여 있어도).
4. `internal/ui/importer_test.go` (신규)
   - `TestImportMarksDuplicates`: 이미 있는 `user@host:port`는 `dup` + 기본 해제.
   - `TestImportSavesOnce`: 6개를 고르면 `Save` 호출은 1회, 목록에 6개가 늘어난다.
   - `TestImportKeepsIdentityPath`: `KeyPath`가 원본 경로 그대로고 `keys/`에 **아무것도
     복사되지 않는다**.
   - `TestImportSwallowsTabKeys`: `focusImport`에서 `alt+2`가 탭을 바꾸지 않는다.
5. `internal/ui/smoke_test.go` 확장
   - `TestLayoutAlignmentWithGroups`: 그룹·긴 이름·필터 입력 상태에서도 모든 행이 정확히
     width이고 패널 높이가 v4와 **동일**하다(세로 예산 불변).
   - `TestLayoutAlignmentWithImport`: `rightImport` 모드에서도 같다.
   - `TestTabsSurviveImportAndFilter`: 탭이 열린 채로 필터·import를 오가도 세션 수가 그대로다.
6. `go vet ./...`, `go build ./...`, `go test -race ./internal/...` 통과.

**수동 확인 (v5 수용 기준 — 자동화하지 않음)**
1. 서버 20개 상태에서 `/`로 `prod` 검색 → 결과만 남고, `enter`로 확정한 뒤 `n`/`e`/`d`가
   정상 동작한다. `esc`로 전체 목록 복귀.
2. 폼에서 그룹 `prod`를 지정한 서버 3개를 만들고 접기·펴기 → 앱을 껐다 켜도 **접힌 채로**
   뜬다. `~/.config/ssh-client/ui.json`을 지우고 켜면 전부 펼쳐진 채로 정상 동작.
3. 접힌 그룹 안 서버에 세션이 열려 있으면 헤더에 `●`가 보인다.
4. 마우스로 서버 행·그룹 헤더를 클릭 → **보이는 그 줄**이 선택된다(1행 delegate 회귀 확인).
5. 실제 `~/.ssh/config`(와일드카드·`Include`가 섞인 것)로 `i` → 미리보기에서 skip 사유가
   보이고, 몇 개만 골라 import한 뒤 그 서버로 실제 접속된다.
6. import한 서버의 `KeyPath`가 `~/.ssh/id_*` 원본을 가리키고 `keys/`에는 새 파일이 없다.
7. 그룹을 전혀 쓰지 않는 기존 `servers.json`으로 실행하면 화면이 v4와 다를 게 없다
   (행 높이만 1행으로 줄어든다).
8. 탭 3개를 연 상태에서 필터·import·그룹 접기를 오가도 세션이 끊기지 않는다.

## v5에서 하지 않는 것 (의도적 제외)
- 중첩 그룹 트리, 드래그로 그룹 이동, 서버별 색/아이콘 → v7
- 퍼지 검색·정규식 필터 — 부분 문자열로 고정
- `~/.ssh/config`의 `Include`/`Match`/와일드카드 해석, 역방향 export
- ProxyJump·ssh-agent·비밀번호 키체인 → **v6**
- 포트포워딩, 테마/키맵 설정 → **v7**
