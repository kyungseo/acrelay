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

## Operating Sequence

Follow the existing sections in this order:

1. Run the [Compatibility Gate](#compatibility-gate).
2. [Establish The Review](#establish-the-review), including any required
   vendor-egress consent.
3. [Run The Review](#run-the-review) with `acrelay init`, then
   `acrelay review`.
4. [After A Valid Round](#after-a-valid-round), summarize the result and
   record one atomic `acrelay driver-response` when findings exist.
5. After changing the subject, use `acrelay advance` and continue the same
   reviewer session when another round is useful.
6. Use `acrelay briefing` or `acrelay close` only under the
   [Briefing, Close, And Cleanup](#briefing-close-and-cleanup) rules.

The host running this Skill is the **driver**. The separate external CLI is
the **reviewer**, and the **owner** retains approvals and Close. An objective
is one bounded review tracked in a private canonical record. The private
handle store preserves reviewer session handles; it is not disposable scratch
state.

## Conversation Contract

Default to a quiet, progressive-disclosure experience.

Render every user-facing template in the conversation language. Keep commands,
identifiers, versions, status values, decision values, and URLs literal.

- Never narrate Skill loading or say that you will probe, set up, initialize,
  or record the review. The first ordinary progress line, if needed, is the
  single reviewer-dispatch line below.
- Run the compatibility check silently. Do not say that the version matches or
  narrate another preflight success.
- Read consent from the original current user turn, not only from the synthetic
  Skill `ARGUMENTS`. Skill invocation may shorten those arguments. An explicit
  acknowledgment, approval, or instruction to proceed with vendor egress in
  the original turn is already consent; do not ask for it again.
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
Skill was packaged for: `v0.1.0-alpha.4`. If the command is missing or
incompatible, do not announce the Skill or probe first. Stop with exactly
these three user-facing lines, translated to the conversation language:

```text
acRelay engine version mismatch. No review round was started.
Installed: <observed-or-missing> / Required: v0.1.0-alpha.4
Update guide: https://github.com/kyungseo/acrelay/blob/v0.1.0-alpha.4/docs/OPERATIONS.md
```

Do not add protocol analysis, an upgrade decision essay, raw paths, command
output, caveats about dirty builds, or an offer to show diagnostics.
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
- finding appetite: 8,
- private canonical: a collision-safe file below
  `~/.acrelay/records/`,
- private handle store: `~/.acrelay/handles.json`,
- external reviewer context: separate unless the user explicitly says
  otherwise.

Ask about the round bound only when the user wants to change it before the
first successful review preflight. That preflight fixes the bound for the
objective. Later reviews must omit `-round-bound` or assert the same value; a
different bound requires a new objective. Ask for a canonical path only when
the default private location cannot be used.

Choose the narrowest review access profile that can answer the question:

- `contained`: the declared subject is sufficient. This is the default for
  code, plans, and artifacts that do not require current external facts.
- `contextual`: exact auxiliary local files are needed to interpret the
  subject. Use `-review-profile contextual` with `-context-spec <file>`; do not
  turn the whole repository into context.
- `research`: current factual or source verification is material. This enables
  only the reviewer vendor's bounded web search/fetch surface. Use
  `-review-profile research` with `-ack-research-egress`; `-context-spec` is
  optional. Command network and write-capable tools remain unavailable.

Do not use `research` merely because network access exists. Do not use
`contained` when the user explicitly asks to verify current external facts.
Subject files are authoritative review evidence. Auxiliary local context and
external sources may inform findings but do not become subject evidence.

Vendor egress is the ordinary first-use consent gate. If the user has not
already acknowledged it in the current conversation, ask once:

> Review content, resolved paths, and metadata will be sent to the selected
> Claude Code or Codex CLI. Proceed?

Do not expand this into a checklist. Local record storage does not mean local
model inference.

For `research`, include search-query and external-URL egress in the same
consolidated consent question and pass `-ack-research-egress` in addition to
`-ack-vendor-egress`. Do not ask twice.

If the user asks for a host-native subagent, explain briefly that this version
uses a separate external CLI reviewer. Never describe that CLI process as a
subagent.

## Run The Review

### Prepare Inputs

Use `acrelay init` with the inferred/default values, then `acrelay review`.
Prefer `-target` for one file and `-target-spec` only for explicit file sets or
a deliberately bounded subtree. Use `-context-spec` only for exact auxiliary
files under `contextual` or `research`. Keep prompt/spec scratch files private
and remove only host-created scratch files after use.

Do not run any `acrelay ... --help` command during normal operation. Do not
pre-read the subject merely to construct the command; the engine snapshots and
validates it. A named relative target such as `target.md` is already resolved
against the current working directory; do not locate it with `find`, `ls`, or
another discovery command.

Inside Claude Code use `-driver-vendor claude`; inside Codex use
`-driver-vendor codex`. Use the other vendor only as the `-reviewer` value
requested by the user.

Choose a literal collision-safe record suffix in reasoning, such as a UUID-like
hex token. Do not use shell command substitution, environment expansion, or a
second command to generate it.

The engine reports the combined subject-and-context member count and byte size.
If it refuses a broad scope, do not add `-ack-broad-scope` silently. Narrow the
selector when the question permits; otherwise ask one user question that
includes the member/byte summary and expected cost/latency risk.

### Command Forms

Use these exact forms and add only the flags required by the chosen profile:

```text
acrelay init -canonical <record> -question <question> -target <file> -review-profile contained -approval-actor owner -ack-vendor-egress -driver-vendor <claude|codex> -context-relation <separate|shared> -execution-surface external-cli
acrelay review -canonical <record> -reviewer <claude|codex> -round-bound <1..5> -finding-appetite <1..20> -handles <private-handle-store> -prompt-file <private-prompt-file>
acrelay driver-response -canonical <record> -response-file <strict-json-file>
```

The `init` form above shows the default `contained` profile. For
`contextual`, replace it with `-review-profile contextual` and add
`-context-spec <file>`. For `research`, replace it with
`-review-profile research`, add `-ack-research-egress`, and add
`-context-spec <file>` only when exact auxiliary local context is needed.

`review` uses these engine defaults when their flags are omitted:

- `-finding-appetite`: 8,
- `-startup-timeout`: 2 minutes,
- `-idle-timeout`: 5 minutes,
- `-hard-cap`: 30 minutes,
- `-handles`: `~/.acrelay/handles.json`.

A review requires either `-prompt` or `-prompt-file`; neither has a default.
Use `-prompt` only for the literal review request itself. A filesystem path
must always use `-prompt-file`; never pass a prompt-file path as the value of
`-prompt`.

### Dispatch And Progress

The completed `review` output includes each finding's severity, blocking state,
evidence IDs, summary, and bounded recommendation. Use that output to prepare
the driver response. Do not read the raw canonical unless the user explicitly
requests diagnostics or a recovery operation requires it.

The default conversation needs at most one progress line, rendered in the
conversation language:

> The selected reviewer is assessing the subject.

The engine may emit a bounded `activity-observed` progress signal. Do not turn
each signal into narration; use it only to distinguish a live review from
silence.

## After A Valid Round

Reviewer approval is evidence, not owner authority. Never auto-apply a finding
that changes product direction or exceeds the user's approved scope.

After a valid round:

1. Summarize the verdict and actionable findings.
2. As driver, choose `accept`, `revise`, or `defend` with a factual rationale
   when the disposition is within the task's approved scope. Put every
   disposition into one strict response file and record it once with
   `acrelay driver-response`; do not guess fields.

   The exact driver-response shape is:

   ```json
   {
     "dispositions": [
       {
         "finding_id": "R0-F1",
         "decision": "revise",
         "rationale": "The finding is supported by the declared evidence.",
         "follow_up": "Update the subject before the next round.",
         "approval_request_id": ""
       }
     ]
   }
   ```

   Every object and field shown above is required. `decision` is
   `accept|revise|defend|needs-user`. Use an empty string for fields that do not
   apply. `accept` and `revise` require a concrete `follow_up` or `no-action`;
   `needs-user` requires the exact existing approval request ID. The batch is
   atomic: one invalid item records nothing.

3. Use `needs-user` and the typed approval-request flow only for a real owner
   decision. Ask one consolidated question, not one question per finding.
4. After changing the subject, use `acrelay advance` with a factual delta.
5. Continue the same reviewer session unless a documented, reasoned session
   reset is necessary.

Do not run `briefing` immediately after a valid review. Run it only when the
user explicitly asks about Close readiness or asks to close. An approve result
with no findings needs no driver-response mutation and no extra status read.

## Failure And Recovery

Do not improvise recovery.

- Do not run the reviewer CLI directly.
- Do not create a Git repository to change reviewer trust behavior.
- Do not supply a caller-selected neutral cwd.
- Do not guess approval payloads.
- Do not delete or edit canonical, journal, handle, cwd, quarantine, or vendor
  state.

For a preflight failure, no round was consumed. Correct a clearly mechanical,
in-scope issue and retry once when the existing user request already
authorizes the review. Otherwise stop and report the blocker.

For a started attempt, startup/idle timeout and inferred network failures are
recorded failures and are never retried automatically. `ENOTFOUND` means DNS
resolution failed; increasing the model timeout is not a remedy. Ask the user
to restore connectivity, then use the engine's recorded state to choose a new
objective or other explicit recovery path.

If the absolute `-hard-cap` expires after the reviewer child starts, the result
is `UNKNOWN`: the engine cannot assert whether vendor-side execution
completed. `UNKNOWN` is never retried automatically.

Use `acrelay reconcile` for a pending journal. `abandon-transaction`,
`terminate`, cleanup, and Close require the exact owner decision defined by
the engine.

Default failure response:

```text
The review could not start. No round was consumed.
Cause: <plain-language cause>
Next action: <one safe action>
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

Keep the completion to one verdict line, the actionable findings, and one
consolidated owner-decision paragraph. Do not repeat scope, protocol, round
accounting, or follow-up choices unless they materially change the decision.
Do not report member counts, byte counts, evidence-anchor mechanics,
governance labels, version-check success, or consumed-round bookkeeping on a
normal success. Do not offer Close, cleanup, edits, or a re-review unless the
user requested that next action.
If the request explicitly forbids mutation, do not end by asking whether to
perform that mutation. State that remediation remains with the owner and stop;
the completion must not contain an invitation such as “tell me whether to edit”
or “I can draft the changes.”

On failure, use the three-line failure form above. Show canonical paths,
session references, detailed governance state, provenance, or raw reviewer
output only when the user explicitly requests diagnostics.
