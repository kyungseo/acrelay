# Changelog

**English** · [한국어](./CHANGELOG.ko.md)

This file records changes that matter to acRelay users. The project is in
Alpha, so commands and file formats may change between prereleases. Once a tag
is published, however, it is never reused or rewritten.

## v0.1.0-alpha.4 — First-Use Reliability And Bounded Research

- Replace Claude's opaque one-shot output with a structured event stream.
  Startup, idle, and hard-cap supervision now observe real activity and expose
  only bounded progress messages.
- Disable discovered standalone Codex reviewer Skills for the invocation, and
  return bounded evidence IDs and recommendations directly so the host does
  not need to probe help or reread the raw canonical.
- Add immutable `contained`, `contextual`, and `research` review profiles.
  Exact auxiliary context is revision-checked, research requires separate
  egress consent, and command execution remains read-only.
- Cut the canonical format to `store-md v0.10`; Alpha.3 canonicals require
  their matching binary or a fresh Alpha.4 objective.
- Stop broad reviews before dispatch when subject plus context exceeds 8
  members or 128 KiB, unless the user explicitly accepts the token, time, and
  context risk. Reviews consolidate to eight actionable findings by default
  without omitting critical/high findings.
- Classify the exact `ENOTFOUND` signature as an inferred DNS/network failure
  without automatic retry. Accept only the safe trailing-empty-line evidence
  boundary that caused a real false mismatch.
- Add atomic JSON driver responses: one invalid disposition leaves the whole
  batch unchanged.
- Bundle the exact official Skill in the checksum-verified release archive.
  The pinned installer can install it for Codex, Claude Code, or both, protects
  local differences, and supports adding the Skill after the same engine
  version is already present.

## v0.1.0-alpha.3 — Runtime Compatibility And Calmer Skill UX

- Admit Claude Code `2.1.217+` and Codex CLI `0.144.1+` on the verified
  `darwin/arm64` platform when their required restricted command options remain
  available.
- Check handle-store compatibility before reviewer execution. A fresh session
  preserves a v1 store as a private backup and starts v2; an unsafe v1 resume
  stops with explicit reset guidance.
- Accept an exact final line ending when a reviewer quotes a range that reaches
  the end of a text file, avoiding a false `excerpt-mismatch`.
- Make the official Skill quiet by default: infer safe defaults, consolidate
  owner questions, hide protocol diagnostics, and forbid improvised raw CLI
  recovery.

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
