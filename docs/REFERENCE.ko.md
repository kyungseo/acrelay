# acRelay 동작 및 CLI 상세 안내

[English](./REFERENCE.md) · **한국어**

이 문서는 acRelay `v0.1.0-alpha.1`의 상세 동작 계약을 설명합니다. 설치와 가장
짧은 review 흐름은 [root README](../README.ko.md)에서 시작하세요.

## 이 문서를 읽기 전에

이 문서는 README와 운영 안내보다 정확한 동작이 필요한 operator와 maintainer를
위한 reference입니다. 다음 용어가 반복해서 사용됩니다.

- **review objective:** `init`부터 owner가 종료하거나 중단할 때까지 이어지는
  review 하나
- **canonical record:** 해당 review의 공식 이력을 담는 비공개 Markdown 파일
- **session reference(`ref`):** 특정 reviewer session을 이어서 사용하거나
  정리할 때 acRelay가 사용하는 식별자
- **formal round:** finding을 만들 수 있는 reviewer 검토 회차 하나
- **disposition:** finding을 수용·수정·반론하거나 owner에게 결정을 요청한다는
  driver의 기록
- **fail closed:** review를 몰래 바꾸거나 허용되지 않은 회차를 사용하거나 덜
  제한된 경로로 우회하지 않고 중단하는 동작
- **vendor egress:** 선택한 Claude Code 또는 Codex reviewer로 보내는 review
  내용, 해석된 경로와 metadata

Command name, state value와 format identifier는 machine-readable 원문을 그대로
사용합니다.

## Maintainer용 code 구성

- `internal/kernel`: collaboration과 objective type, execution/governance
  state machine, formal-round bound, attempt bound, confirmation cycle
- `internal/review`: `review-profile v0.2` examined evidence, structured
  finding, driver disposition, approval request와 fail-closed closure check
- `internal/store`: canonical Markdown, pre-dispatch revision snapshot,
  content-derived fence/base64 raw block, owner-only atomic replace
- `internal/subject`: local file, explicit files, declared subtree selector와
  resolved member manifest
- `internal/relay`: prepare, snapshot, durable journal, child start/capture,
  transaction-tagged canonical append를 수행하는 one-shot review flow
- `cmd/acrelay`: `init`, `review`, `confirm`, `disposition`,
  `request-approval`, `respond-approval`, `withdraw-approval`, `advance`,
  `close`, `terminate`, `reconcile`, `abandon-transaction`, `cleanup`,
  `status`, `briefing`, `version`

현재 format은 `store-md v0.9`, `review-profile v0.2`,
`dispatch-journal v0.1`, `briefing-output v0.2`, handle store v2입니다.
이 format들은 exact cutover이며 지원하지 않는 이전 format을 자동으로
migration하지 않습니다.

## Canonical record는 비공개로 보관합니다

Canonical Markdown은 raw reviewer output과 provenance를 보존합니다. 사용자
컴퓨터에서도 공유·동기화·publish하지 않는 위치에 두세요.

`init`은 `.git` ancestor 또는 runtime이 검증한 sync-root signal을 발견하면
fail-closed합니다. 탐지는 완전하지 않으므로 warning이 없다는 사실은 안전성
proof가 아닙니다.

위험한 위치를 사용해야 한다면 `-allow-unsafe-location`,
`-approval-actor`, `-unsafe-location-reason`을 같은 `init`에서 지정해야 합니다.
Actor, signal과 rationale은 canonical에 기록되며 재사용 가능한 policy가 되지
않습니다. V1 sync signal은 platform environment가 가리키는 existing OneDrive
root와 macOS iCloud Drive root에 한정됩니다. Dropbox, Google Drive, alias와
그 밖의 provider는 검증된 detector가 추가되기 전까지 탐지하지 않습니다.

이번 release에는 share/export path가 없습니다. Raw canonical을 복사하면 private
prompt, evidence와 provenance가 노출될 수 있습니다. Public example은 synthetic
data만 사용해야 합니다.

`~/.acrelay/handles.json`은 private session handle store입니다. POSIX에서는
`0600`, Windows에서는 current-user-only protected DACL을 사용합니다. Handle
store v2는 vendor, native handle, trust-profile identity와 reviewer working
directory를 결속합니다. Profile이나 cwd가 없는 entry 또는 v1 store는
fail-closed합니다.

Neutral reviewer cwd는 handle store의 private `runtime/` 아래에 만들어지고 같은
session resume에서 재사용됩니다. 기존 temp-bound handle은 자동으로 이동하거나
복사하지 않습니다. Canonical과 handle store mutation은 sidecar lock으로
process 간 직렬화하지만, lock을 사용하지 않는 writer까지 막는 것은 아닙니다.
Store revision guard가 비협력 writer의 stale write를 차단합니다.

## 비공개 artifact의 보관과 정리

| Artifact | 위치와 민감도 | 보존 및 삭제 authority |
| --- | --- | --- |
| Raw canonical | Owner가 선택한 private path, raw review evidence 포함 | Owner-retained. acRelay가 삭제하거나 TTL을 적용하지 않음 |
| Canonical `.lock` | Canonical 옆 private coordination state | Orphan임을 증명할 수 없어 자동 삭제하지 않음 |
| `.dispatch-*` | Canonical 옆 pending execution evidence | Captured reconcile 뒤 제거하거나 declared abandon 뒤 quarantine으로 이동 |
| `.quarantine-*` | Canonical 옆 anomaly evidence | Durable canonical disposition과 owner reason 뒤 exact digest/path purge만 허용 |
| Handle store와 lock | 기본 `~/.acrelay`, native session secret | Exact ref cleanup만 허용. Global TTL/GC 없음 |
| Neutral cwd | Handle과 짝을 이루는 private `runtime/` | Resume을 위해 보존. Verified acRelay-owned directory만 exact cleanup |
| Vendor state | Vendor가 소유한 위치 | acRelay authority 밖이며 삭제됐다고 보고하지 않음 |

Cleanup은 explicit하고 canonical/ref 범위가 명확하며 기본적으로 비파괴적입니다.

```sh
acrelay cleanup -canonical review.md -ref sref-... -mode list
acrelay cleanup -canonical review.md -ref sref-... -mode dry-run
acrelay cleanup -canonical review.md -ref sref-... -mode apply \
  -actor owner \
  -continuity-abandon-reason "No related objective will resume this session."
```

`OPEN`, `DECISION_REQUIRED`, `CLOSABLE` objective는 session cleanup을 허용하지
않습니다. Terminal objective도 owner가 exact canonical/ref와 related-objective
continuity abandon을 선언하기 전에는 handle과 cwd를 보존합니다. Apply는 해당
선언을 canonical에 먼저 기록한 뒤 mapping과 acRelay-owned neutral cwd를
제거합니다. In-target cwd는 삭제하지 않습니다. 반복 cleanup은
`already_clean`으로 수렴합니다.

Pending journal이나 `UNKNOWN` transaction은 normal `reconcile` 또는 declared
`abandon-transaction`이 끝날 때까지 session cleanup을 차단합니다. Quarantine
purge는 별도 exact-path action이며 abandoned transaction full digest가
canonical에 남아 있어야 합니다.

```sh
acrelay cleanup -canonical review.md \
  -quarantine review.md.quarantine-tx-...-<digest12>.json \
  -mode apply -actor owner \
  -sidecar-reason "The canonical disposition is durable."
```

## Reviewer 실행 실패와 복구

모든 reviewer child start 전에 private dispatch journal을 durable하게 기록합니다.
Journal이 pending인 동안 같은 canonical은 `reconcile`과 owner/arbiter가 선언한
`abandon-transaction` 외의 mutation을 거부합니다. `status`는 계속 사용할 수
있습니다.

종료 분류 우선순위는 고정되어 있습니다.

- Valid terminal contract: 정상 result path
- Typed vendor failure event: `FAILED`와 failure cause
- Signal death, parent cancellation, hard-cap, crash reconcile: `UNKNOWN`
- Clean exit이지만 terminal contract 없음:
  `FAILED` + `transport.missing-terminal`
- Startup/idle expiry: `FAILED(timeout)`
- `Prepare` 실패와 explicit child-start 실패만 attempt를 소비하지 않음

`UNKNOWN`은 reviewer가 안전하게 멈췄다는 증거가 아니라 실행 결과를 단정할 수
없다는 ambiguity marker입니다. 자동 재dispatch는 금지됩니다.

모든 `FAILED` 또는 `UNKNOWN` dispatch는 `failure-cause v0.1` record 하나를
transaction ledger에 가집니다. Cause code는 허용된 source(`observed`,
`vendor-declared`, `inferred`)와 결속됩니다. Typed cause에는 raw vendor text를
넣지 않으며 `status`는 allowlisted phrase만 표시합니다. 현재 adapter는
owner-remediation category를 직접 관측할 typed vendor field가 없으므로 text를
근거 없이 quota/auth/network로 추측하지 않습니다.

Native resume handle은 child start 전에 vendor-safe format인지 검사합니다.
Malformed handle은 attempt를 소비하지 않고 fail-closed합니다. Child start 뒤
vendor가 resume을 거부하면 consuming `FAILED`입니다.

POSIX에서는 첫 SIGINT/SIGTERM이 tracked process group에 graceful cancellation을
보내고, 두 번째 signal은 tracked group을 강제 종료합니다. Windows console
interrupt는 Job Object를 즉시 종료하며 SIGTERM과 같은 graceful stage가
없습니다. Parent가 SIGKILL로 종료되면 cleanup을 수행할 수 없으므로 journal이
recovery authority로 남습니다.

## Platform 근거와 dispatch 범위

근거는 surface와 기록된 lane별로 표시하며, 여러 lane을 합쳐 넓은 platform
claim으로 만들지 않습니다.

- macOS arm64: core runtime과 기록된 exact vendor tuple의 real-vendor dispatch
  검증. Initial release의 유일한 distributed functional artifact
- Linux amd64: hosted CI core runtime 검증. Real-vendor dispatch 비활성
- Windows Server x64: hosted CI core runtime 검증. Real-vendor dispatch
  비활성
- Windows 11 ARM64: owner-operated VM에서 deterministic core runtime 검증.
  Race와 real-vendor dispatch는 claim하지 않음

검증하지 않은 `vendor + version + GOOS + GOARCH` 조합의 real-vendor dispatch는
fail-closed합니다. 검증한 darwin/arm64 tuple만 dispatch가 활성화됩니다. 이는
일반적인 `Supported` 상태가 아니며, 어느 platform에서도 power-loss durability를
claim하지 않습니다.

이는 release 근거의 현재 경계이지 Apple Silicon만 계속 지원하겠다는 결정이
아닙니다. Linux와 Windows reviewer 검증은 다음 지원 확대 단계로 계획하고
있습니다. Platform patch와 지원 문구는 restriction 근거를 검토한 뒤 제공하며,
검증 전에 일정이나 경고 없는 실행을 약속하지 않습니다. Intel Mac은 현재 배포
artifact와 검증된 real-review 근거가 모두 없습니다.

## Reviewer 구성과 독립성 claim

acRelay는 reviewer leg만 자동화하므로 driver agent를 runtime에서 관측할 수
없습니다. `review-topology v0.1`은 independence claim 대신 source가 명시된
facet을 기록합니다.

- `reviewer_vendor`, 실제 dispatch 뒤의 session mode: runtime-observed
- `execution_surface`, `driver_vendor`, `context_relation`: operator-declared
- `vendor_relation`: derived-not-verified
- 선언 생략: explicit `undeclared`

Derived profile은 `cross-vendor-external`, `same-vendor-external`,
`undeclared` 중 하나지만 display label일 뿐입니다. `independence verified`는
어떤 profile에서도 사용하지 않습니다. Separate context 선언은
`declared-not-verified`로만 표시합니다.

`briefing`과 `status`는 facet과 source를 함께 표시합니다. `briefing`은
`same-vendor-review`, `topology-undeclared`, `reviewer-session-resumed`를
각각 별도 caution identifier로 표시해 correlated blind spot, 선언되지 않은
topology와 fresh context가 아닌 resumed/carried session을 숨기지 않습니다.

`external-cli` surface만 지원합니다. Driver host의 own subagent result를
ingest하는 `host-subagent`는 typed ingress가 없으므로 명시적으로 unsupported이며
다른 topology로 fallback하지 않습니다.

## Review 대상

단일 file은 `-target`으로 지정합니다.

```sh
acrelay init \
  -canonical review.md \
  -question "Is this ready?" \
  -target ./artifact.md \
  -approval-actor owner@example \
  -ack-vendor-egress
```

Explicit files 또는 local subtree는 `subject-spec v0.1` JSON과
`-target-spec`을 사용합니다.

```json
{
  "version": "subject-spec v0.1",
  "kind": "files",
  "root": ".",
  "members": ["README.md", "internal/relay/relay.go"]
}
```

```json
{
  "version": "subject-spec v0.1",
  "kind": "subtree",
  "root": ".",
  "include": ["cmd", "internal"],
  "exclude": ["internal/testdata/generated"]
}
```

`file` kind는 `members` 하나, `files`는 하나 이상의 explicit member를
요구합니다. `subtree`는 authoritative checkpoint마다 member를 다시 계산합니다.

모든 member는 resolved root 안의 readable regular file이어야 합니다. Broken
link, root escape, unsupported type, unreadable member, zero-member selection은
fail-closed합니다. Symlink identity와 resolution fact도 aggregate에 포함됩니다.

Canonical, lock, `.dispatch-*`, `.quarantine-*` namespace를 subject에 포함할 수
없습니다. Review, confirmation, advance와 close는 authoritative checkpoint마다
selector를 다시 resolve하고 모든 member를 hash합니다. 이는 checkpoint 사이의
변경을 탐지하지만 atomic filesystem snapshot이나 watcher를 claim하지 않습니다.
Git staged patch, commit, range, branch selector는 이번 version에서 지원하지
않습니다.

## Review input의 신뢰 경계

Objective는 첫 vendor dispatch 전에 immutable trust policy를 저장합니다.
`-ack-vendor-egress`와 `-approval-actor`가 필수입니다. Acknowledgment는 dispatch
gate이지 isolation, authentication, RBAC 또는 Close authority가 아닙니다.

기본 reviewer cwd는 subject 밖의 owner-only neutral directory입니다. Claude는
명시적인 subject read scope와 제한된 tool set으로, Codex는 user configuration과
rules를 무시하고 read-only sandbox로 실행됩니다. Initial review, resume,
confirmation은 같은 objective trust profile과 handle-bound cwd를 사용합니다.

In-target cwd는 명시적으로 unsafe한 mode입니다. `init`에서
`-allow-in-target-workdir`를 승인해야 하며 cwd는 subject root 안에 있어야
합니다. Neutral objective는 caller가 supplied한 cwd를 거부합니다.

Restricted tool policy는 convention과 관측된 regression evidence이지 OS-level
sandbox나 malicious-target isolation proof가 아닙니다. Vendor/version/platform
drift는 fail-closed하며 unrestricted fallback은 없습니다.

## Review evidence 계약

모든 result-valid review와 confirmation은 non-empty examined anchor를
제공해야 합니다.

- `content-match`: raw source range와 excerpt가 exact match
- `content-match-normalized`: raw match가 실패하고 CRLF→LF normalization 뒤
  같은 1-based range가 match
- `reviewer-declared`: opaque/empty target 등 content match를 만들 수 없는
  경우 reviewer가 명시
- `synthetic-sampled`: test-only fixture에서만 사용하는 synthetic assurance

이 evidence는 byte acquisition과 anchor validity를 나타낼 뿐 reviewer의 이해,
완전성, 정확성 또는 owner authority를 증명하지 않습니다.

Finding은 stable ID, severity, summary, evidence anchor와 blocking mapping을
가집니다. Driver는 모든 finding에 `accept`, `revise`, `defend`,
`needs-user` 중 하나와 rationale을 기록해야 합니다. `accept`/`revise`는
follow-up이 필요합니다.

```sh
acrelay disposition \
  -canonical review.md \
  -finding R0-F1 \
  -decision revise \
  -rationale "The evidence is valid." \
  -follow-up "Apply the fix and advance the target."
```

Review-time approval request는 stable ID, type, scope, reason, options와 status를
가지는 open-ended record입니다. Owner response는 actor, exact verbatim,
decision option, scope, timestamp와 durable anchor를 보존합니다. Ambiguous
response는 unresolved 상태로 남고 closure를 차단합니다.

```sh
acrelay request-approval \
  -canonical review.md \
  -request-file approval-request.json \
  -role driver \
  -requester codex

acrelay respond-approval \
  -canonical review.md \
  -request AR-1 \
  -actor owner \
  -verbatim-file owner-response.txt \
  -responded-at 2026-07-20 \
  -decision accept-current \
  -scope "R0-F1 on the current target only" \
  -durable-anchor "canonical#owner-response-AR-1" \
  -unambiguous
```

## 종료 준비 briefing

`briefing`은 allowlisted read-only projection입니다.

```sh
acrelay briefing -canonical review.md
acrelay briefing -canonical review.md -format json
acrelay briefing -canonical review.md -format json -check
```

Readiness는 `ready`, `ready-with-cautions`, `blocked`, `terminal`입니다.
`briefing`은 owner approval, acknowledgment, Close authority 또는 persisted
state를 만들지 않습니다. 실제 `close`는 canonical lock 안에서 target revision과
closure gate를 다시 확인합니다.

`-check` 없이 business state를 정상적으로 render하면 exit 0입니다. `-check`를
사용하면 stable exit classification은 `0=ready`, `3=ready-with-cautions`,
`4=blocked`, `5=terminal`이며 parse, I/O 또는 canonical integrity failure는
exit 1을 유지합니다.

Projection에는 raw prompt, raw reviewer output, native handle, absolute/private
path, owner verbatim과 raw error를 포함하지 않습니다.

## Formal round 제한

Objective별 formal round bound는 `1..5`, 생략 시 `3`입니다. 첫 review의
successful preflight 뒤, child start 전에 canonical에 immutable하게 결속합니다.
Pure preflight failure는 bound를 결속하지 않습니다. 같은 값 또는 생략은 resume할
수 있지만 다른 explicit value는 adapter preparation 전에 fail-closed합니다.

```
acrelay review -canonical review.md -reviewer claude -prompt-file packet.md -round-bound 5
```

이 제한은 `review → 수정 → 재검토` 전체에 적용됩니다. 예를 들어 `1`을 선택하면
`R0` 검토 한 번만 가능하고 재검토 회차는 없습니다. Child를 시작하지 못한 것이
명확하면 회차를 사용하지 않지만, 실행 여부를 확정할 수 없으면 `UNKNOWN`으로
기록하고 자동으로 다시 시도하지 않습니다.

Confirmation cycle과 attempt bound는 formal-round bound와 별도입니다. 마지막
formal round 뒤에도 미해결 finding이나 approval이 있으면 owner gate가 계속
closure를 차단합니다.

## CLI compatibility와 build 출처

acRelay는 Claude Code CLI와 Codex CLI의 actual command, structured output,
terminal/session contract를 capability-first로 검사합니다. Version 문자열은
provenance이지만 exact restriction evidence는
`vendor + version + GOOS + GOARCH`에 결속됩니다. Help probe는 advisory이며
platform-default model을 override하지 않습니다.

`acrelay version --short`는 release installer가 exact binary version을 확인하기
위한 machine-readable surface입니다. `acrelay version`은 release version,
source commit과 Go runtime version을 표시합니다.

## Build와 반복 가능한 검증

```sh
go build -o acrelay ./cmd/acrelay   # single binary, no runtime dependencies
go vet ./...
go test ./... -race -count=1        # deterministic suite only
```

기본 Go suite는 HOME, agent configuration, Git configuration과 vendor CLI lookup
path를 격리합니다. User auth, external network 또는 model/API cost를 사용하지
않습니다.

Installed CLI live smoke는 별도 explicit owner action입니다.

```sh
ACRELAY_LIVE_SMOKE=1 ./scripts/live-smoke.sh claude
ACRELAY_LIVE_SMOKE=1 ./scripts/live-smoke.sh codex
```

Live smoke는 user auth, network와 model/API budget을 사용합니다. Deterministic
test 성공을 live-vendor evidence로 해석하면 안 됩니다.

Binary 제거와 private state 제거는 분리합니다. Binary 제거는 `~/.acrelay`,
explicit handle store, durable neutral cwd, owner-selected canonical 또는
vendor-owned data를 삭제하지 않습니다. State를 정리하려면 exact
canonical/ref를 지정하는 `acrelay cleanup` 계약을 먼저 사용해야 하며 secure
deletion을 claim하지 않습니다.
