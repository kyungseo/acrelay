# Changelog

**English** · [한국어](./CHANGELOG.ko.md)

This file records changes that matter to acRelay users. The project is in
Alpha, so commands and file formats may change between prereleases. Once a tag
is published, however, it is never reused or rewritten.

## v0.1.0-alpha.2 — First Public Validation Preview

This release is **Experimental** and broader validation is still
**Validation pending**.
It does not claim general `Supported` status.

### Included

- Claude Code or Codex CLI reviews with a fixed formal-round limit and
  structured findings
- A private Markdown file that serves as the official review history, plus
  recovery data for interrupted or uncertain reviewer runs
- A recorded driver response to every finding, recorded owner approvals, and
  an owner-only `close` command
- An exact, revision-checked file list for one file, selected files, or a
  declared directory tree
- A read-only closeout summary and explicit cleanup for selected acRelay
  session data
- Platform-specific core test evidence, with live reviewer use restricted to
  verified macOS Apple Silicon combinations
- A `darwin/arm64` release archive that is not Developer ID signed or notarized,
  plus a checksum-verifying installer, pinned `go install`, and recorded build
  provenance

### Known limitations

- Windows is the next platform-support target. Its core runtime lane is
  verified, while live Claude Code and Codex review support still awaits
  platform-specific validation and any resulting patches. Linux retains core
  CI coverage but has no artifact or live-review support plan. Intel Mac has
  no artifact or verified reviewer combination in this release.
- The release is not Developer ID signed or notarized.
- There is no redacted export, hosted service, daemon, automatic merge, or
  automatic retry after ambiguous execution.
- A separate reviewer process and recorded excerpts do not prove independence,
  completeness, correctness, or understanding.
- The optional official acRelay Skill is included in this repository's release
  source tree and versioned with the engine. It remains part of the same
  **Experimental**, **Validation pending** preview.
