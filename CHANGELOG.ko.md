# Changelog

[English](./CHANGELOG.md) · **한국어**

사용자에게 영향을 주는 acRelay 변경 사항을 이 문서에 기록합니다. 현재 Alpha
단계이므로 prerelease 사이에 command와 file format이 달라질 수 있습니다.
하지만 한 번 게시한 tag는 재사용하거나 rewrite하지 않습니다.

## v0.1.0-alpha.4 — 첫 사용 신뢰성과 제한된 research

- Claude의 불투명한 one-shot output을 structured event stream으로 바꿨습니다.
  Startup, idle, hard-cap supervision이 실제 activity를 관측하며 제한된 progress
  message만 표시합니다.
- Codex reviewer invocation에서 발견한 standalone Skill을 비활성화하고 bounded
  evidence ID와 recommendation을 직접 반환해 host가 help를 probe하거나 raw
  canonical을 다시 읽지 않아도 되게 했습니다.
- Immutable `contained`, `contextual`, `research` review profile을 추가했습니다.
  Exact auxiliary context는 revision을 확인하고, research는 별도 egress 동의를
  요구하며, command 실행은 계속 read-only입니다.
- Canonical format을 `store-md v0.10`으로 cutover했습니다. Alpha.3 canonical은
  matching binary를 사용하거나 Alpha.4 objective를 새로 시작해야 합니다.
- Subject와 context 합계가 8개 member 또는 128 KiB를 넘으면 token·시간·context
  위험을 사용자가 명시적으로 수락하기 전 dispatch를 중단합니다. 기본적으로
  actionable finding은 8개로 합치되 critical/high finding은 생략하지 않습니다.
- Exact `ENOTFOUND` signature를 inferred DNS/network failure로 분류하지만 자동
  retry하지 않습니다. 실제 false mismatch를 일으킨 안전한
  trailing-empty-line evidence boundary만 허용합니다.
- Atomic JSON driver response를 추가했습니다. Disposition 하나가 잘못되면 전체
  batch가 바뀌지 않습니다.
- Checksum을 검증한 release archive에 exact official Skill을 포함했습니다.
  고정 installer는 Codex, Claude Code 또는 양쪽에 Skill을 설치하고, 로컬 차이를
  보호하며, 같은 engine version을 먼저 설치한 뒤 Skill만 추가하는 경로도
  지원합니다.

## v0.1.0-alpha.3 — Runtime 호환성과 간결한 Skill UX

- 검증된 `darwin/arm64`에서 제한 실행에 필요한 option이 유지되는 경우 Claude
  Code `2.1.217+`와 Codex CLI `0.144.1+`를 허용합니다.
- Reviewer 실행 전에 handle store 호환성을 확인합니다. 새 session은 v1 store를
  private backup으로 보존하고 v2를 시작하며, 안전하게 이어갈 수 없는 v1
  resume은 explicit reset 안내와 함께 중단합니다.
- Text file 끝까지 인용한 reviewer가 마지막 line ending을 포함해도 exact
  evidence로 인정해 잘못된 `excerpt-mismatch`를 막습니다.
- 공식 Skill이 안전한 기본값을 추론하고 owner 질문을 합치며 protocol 진단을
  숨기도록 대화 UX를 줄였습니다. 임의 raw CLI 복구도 금지합니다.

## v0.1.0-alpha.2 — 첫 Public Validation Preview

이번 release는 **Experimental** 단계이며 더 넓은 환경의 검증은 **Validation
pending**입니다. 일반적인 `Supported` 상태를 주장하지 않습니다.

### 포함

- 정해진 formal round 제한과 structured finding을 사용하는 Claude Code 또는
  Codex CLI review
- Review 공식 이력을 담는 비공개 Markdown 파일과 중단됐거나 결과가 불확실한
  reviewer 실행을 복구하기 위한 정보
- 모든 finding에 대한 driver 응답, owner 승인 기록과 owner만 실행할 수 있는
  `close` command
- 파일 하나, 선택한 여러 파일 또는 지정한 directory tree를 대상으로 하는
  revision 검증 file list
- Read-only 종료 준비 요약과 선택한 acRelay session data의 명시적인 cleanup
- Platform별 core test 근거와 검증된 macOS Apple Silicon 조합에 한정된 live
  reviewer 사용
- Developer ID signing이나 notarization을 하지 않은 `darwin/arm64` release
  archive, checksum-verifying installer, pinned `go install`과 기록된 build 출처

### 현재 제한

- 다음 platform 지원 대상은 Windows입니다. Windows core runtime lane은
  검증했으며, live Claude Code·Codex review 지원은 platform별 검증과 그 결과에
  따른 patch 이후에 결정합니다. Linux는 core CI를 유지하지만 artifact와
  live-review 지원 계획이 없습니다. Intel Mac도 이번 release에 artifact와
  검증된 reviewer 조합이 없습니다.
- Release는 Developer ID signing 또는 notarization을 하지 않습니다.
- Redacted export, hosted service, daemon, automatic merge 또는 불명확한 실행의
  자동 retry가 없습니다.
- 별도의 reviewer process와 기록된 발췌문은 독립성, 완전성, 정확성 또는 이해를
  증명하지 않습니다.
- Optional official acRelay Skill은 이 repository의 release source tree에
  포함하고 engine과 함께 versioning합니다. 같은 Experimental·Validation
  pending preview 경계를 유지합니다.
