# Changelog

[English](./CHANGELOG.md) · **한국어**

사용자에게 영향을 주는 acRelay 변경 사항을 이 문서에 기록합니다. 현재 Alpha
단계이므로 prerelease 사이에 command와 file format이 달라질 수 있습니다.
하지만 한 번 게시한 tag는 재사용하거나 rewrite하지 않습니다.

## v0.1.0-alpha.1 — 첫 Public Validation Preview

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

- Linux와 Windows core runtime lane은 검증했지만, live Claude Code·Codex
  review 지원은 계획된 platform별 검증과 그 결과에 따른 patch 이후에
  결정합니다. Intel Mac은 이번 release에 artifact와 검증된 reviewer 조합이
  없습니다.
- Release는 Developer ID signing 또는 notarization을 하지 않습니다.
- Redacted export, hosted service, daemon, automatic merge 또는 불명확한 실행의
  자동 retry가 없습니다.
- 별도의 reviewer process와 기록된 발췌문은 독립성, 완전성, 정확성 또는 이해를
  증명하지 않습니다.
- Optional Skillstead package는 같은 preview의 일부로 Skillstead default
  branch에서 제공합니다. `v0.8.0` tag에는 포함되지 않습니다.
