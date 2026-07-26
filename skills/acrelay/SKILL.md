---
name: acrelay
description: >
  Start, continue, inspect, or close a tracked code or artifact review through
  an installed acRelay command, using Claude Code or Codex as the reviewer.
  Keep the review record private, leave approvals and Close with the owner,
  and stop when the command is missing or incompatible.
---

# acRelay

Use the installed `acrelay` command as the authority for review state,
reviewer execution, recovery, and closure. Keep the normal conversation
focused on the user's work, not on acRelay's protocol.

## Conversation Contract

Default to a quiet, progressive-disclosure experience.

- Ask only for information that cannot be inferred safely.
- Do not narrate command discovery, version probes, temporary files, objective
  IDs, session references, topology labels, provenance, or canonical paths.
- Do not print command output unless the user asks for diagnostics.
- Do not summarize the review subject again merely because dispatch failed.
- Translate failures into plain language: what happened, whether a round was
  consumed, and the one safe next action.
- Explain protocol terms only when the user asks or must make a related
  decision.

## Compatibility Gate

Before an acRelay operation, resolve `acrelay` from `PATH` and run
`acrelay version --short` silently. Continue only with the engine version this
Skill was packaged for: `v0.1.0-alpha.3`. If the command is missing or
incompatible, stop with one short message containing the installed and
required versions and the installation guide:

```text
https://github.com/kyungseo/acrelay/blob/v0.1.0-alpha.3/docs/OPERATIONS.md
```

Never install or update automatically. Never fall back to a raw Claude Code or
Codex invocation.

Reviewer CLI patch updates are expected. Let the engine apply its supported
minimum-version and required-capability checks; do not implement a second
reviewer-version allowlist in the Skill.

## Establish The Review

Infer these values from the request and current task:

- subject and review question,
- reviewer vendor,
- driver vendor and separate/shared context,
- owner label,
- formal-round bound.

Use these defaults without asking:

- owner label: `owner`,
- round bound: 3,
- private canonical: a collision-safe file below
  `~/.acrelay/records/`,
- external reviewer context: separate unless the user explicitly says
  otherwise.

Ask about the round bound only when the user wants to change it. Ask for a
canonical path only when the default private location cannot be used.

Vendor egress is the one ordinary first-use consent gate. If the user has not
already acknowledged it in the current conversation, ask once:

> 리뷰 대상 내용과 경로 정보가 선택한 Claude Code 또는 Codex CLI로
> 전달됩니다. 진행할까요?

Do not expand this into a checklist. Local record storage does not mean local
model inference.

If the user asks for a host-native subagent, explain briefly that this version
uses a separate external CLI reviewer. Never describe that CLI process as a
subagent.

## Start And Continue

Use `acrelay init` with the inferred/default values, then `acrelay review`.
Prefer `-target` for one file and `-target-spec` only for explicit file sets or
a subtree. Keep prompt/spec scratch files private and remove only host-created
scratch files after use.

The default conversation needs at most one progress line:

> 선택한 reviewer에게 검토를 맡겼습니다.

After a valid round:

1. Summarize the verdict and actionable findings.
2. As driver, choose and record `accept`, `revise`, or `defend` with a factual
   rationale when the disposition is within the task's approved scope.
3. Use `needs-user` and the typed approval-request flow only for a real owner
   decision. Ask one consolidated question, not one question per finding.
4. After changing the subject, use `acrelay advance` with a factual delta.
5. Continue the same reviewer session unless a documented, reasoned session
   reset is necessary.

Reviewer approval is evidence, not owner authority. Never auto-apply a finding
that changes product direction or exceeds the user's approved scope.

## Failure And Recovery

Do not improvise recovery.

- Do not run the reviewer CLI directly.
- Do not create a Git repository to change reviewer trust behavior.
- Do not supply a caller-selected neutral cwd.
- Do not probe unrelated `--help` commands or guess approval payloads.
- Do not delete or edit canonical, journal, handle, cwd, quarantine, or vendor
  state.

For a preflight failure, no round was consumed. Correct a clearly mechanical,
in-scope issue and retry once when the existing user request already
authorizes the review. Otherwise stop and report the blocker.

A pending journal uses `acrelay reconcile`. `UNKNOWN` is never retried
automatically. `abandon-transaction`, `terminate`, cleanup, and Close require
the exact owner decision defined by the engine.

Default failure response:

```text
리뷰를 시작하지 못했습니다. 라운드는 소모되지 않았습니다.
원인: <plain-language cause>
다음 조치: <one safe action>
```

If a started attempt was consumed, say so accurately instead. Keep technical
details available on request rather than printing them by default.

## Briefing, Close, And Cleanup

`acrelay briefing` is read-only. It is not approval.

Run `acrelay close` only when the user explicitly asks to close the exact
objective after reviewing readiness. Binary uninstall, private-state cleanup,
and objective closure are separate decisions. Never remove `~/.acrelay`
automatically.

## User-Facing Completion

On success, report only:

- reviewer verdict,
- findings or changes that still need attention,
- decisions that genuinely belong to the user,
- whether another round is useful.

On failure, use the three-line failure form above. Show canonical paths,
session references, detailed governance state, provenance, or raw reviewer
output only when the user explicitly requests diagnostics.
