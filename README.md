# acRelay

**English** · [한국어](./README.ko.md)

acRelay starts a one-shot red-team review when a plan, document, or
implementation deserves a second opinion. The coding agent doing the work asks
a separate Claude Code or Codex CLI to challenge a file or a defined set of
files. acRelay keeps the reviewed revision, findings, responses, and final
owner decision together in a private local record.

The agent making the change remains the **driver**. A person remains the
**owner** and decides approvals and when the review is finished. acRelay never
merges code, retries a review whose result is uncertain, or marks a review
finished on its own.

## Why acRelay

acRelay grew out of repeatedly running red-team rounds between Claude Code and
Codex while developing real plans and implementations. A second agent often
surfaced gaps and made the result stronger, but moving every request and result
by hand became the part nobody wanted to keep doing.

Cross-agent review is useful, but a manual relay gets repetitive. For every
round, someone copies the request to the reviewer and copies the result back to
the driver. Three rounds can mean six copy-and-paste handoffs, while the user
also has to remember which revision was reviewed and which findings remain
open.

| Manual relay | With acRelay |
| --- | --- |
| Copy each request and result between agents | Start the relay through the local binary or acRelay Skill |
| Track rounds, revisions, and findings by hand | Keep them together in one private review record |
| Decide informally when the agents are done | Show a closeout summary and leave the final decision to the owner |

Here, **one-shot** means the owner deliberately starts one bounded review
objective. It does not mean “one prompt” or “one round.” An objective may use
1–5 formal rounds; the default is 3. The limit prevents an open-ended argument
and helps the user control reviewer token and model costs. Long review loops
also accumulate fatigue, repeated prompts, and context drift that can push a
reviewer toward approval without adding useful scrutiny. At five rounds,
the current objective cannot add another formal review round; the owner decides
whether to close it or deliberately start a new objective.

The surface is intentionally simple. Underneath, the engine keeps the reviewed
revision, findings, responses, recovery state, round limit, and owner authority
separate rather than treating a reviewer’s “looks good” as completion.

## One Executable, No acRelay Daemon

acRelay is distributed as one executable. It does not run its own daemon,
server, database, queue, or background network service. When asked to review,
it starts a separately installed and authenticated Claude Code or Codex CLI,
checks the returned structure, and records the result.

The reviewer CLI may use its provider’s network and consume model tokens.
“Local record” means acRelay keeps its review history locally; it does not mean
the reviewer model runs locally.

The typical setup is a Codex App, Claude Code CLI, or Codex CLI driver calling
the acRelay Skill and binary, with Claude Code CLI or Codex CLI as the reviewer.
A user who mainly works with one agent ecosystem can use a supported
same-vendor separate CLI session; acRelay records the shared-blind-spot
caution. Host-native subagent result ingestion is not supported in this
release and never silently falls back to another path.

[![How acRelay moves a review between the driver, private record, owner, and reviewer service](./docs/assets/acrelay-architecture-trust@2x.png)](./docs/assets/acrelay-architecture-trust.svg)

## Current Alpha And Platform Expansion

The first Alpha has a deliberately narrow download and live-review evidence
scope:

- downloadable binary: **macOS Apple Silicon (`darwin/arm64`)** only
- reviewers: Claude Code CLI and Codex CLI, but only for combinations of
  reviewer version and operating system that were tested explicitly
- release: unsigned and not notarized, version `v0.1.0-alpha.1`
- review model: one reviewer for each review, a fixed round limit, a recorded
  driver response to every finding, and a final decision by the owner

Linux and Windows are the next platform-support targets. The core runtime
already passes recorded test lanes on both platforms. Platform-specific
Claude Code and Codex review validation is the next support-expansion step,
and any required patches will be released after that evidence is reviewed.
Until then,
`v0.1.0-alpha.1` stops before sending a review from an unverified platform and
reviewer combination. Intel Mac does not have a downloadable artifact or
verified live-review combination in this release.

A separate process or vendor does not by itself prove independent judgment.

## Install

These commands require the published `v0.1.0-alpha.1` tag and release assets.
If either is unavailable, stop rather than substituting an unpinned branch or
`latest` download.

### One-command binary install

The installer is pinned to the exact tag and verifies the downloaded binary
archive against the release checksum before executing it:

```sh
curl -fsSL https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.1/scripts/install.sh | bash
```

Piping a script to `bash` is convenient but does not let you inspect the
installer first. Use the review-first path below when you want to read it
before execution.

### Review-first binary install

The installer is pinned to one release and never resolves `latest`. Review it
before running:

```sh
curl -fLO https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.1/scripts/install.sh
less install.sh
bash install.sh
```

It installs to `~/.local/bin` by default, does not use `sudo`, verifies the
release archive before executing its binary, and refuses to replace a different
installed version without `--replace`.

### Go install

```sh
go install github.com/kyungseo/acrelay/cmd/acrelay@v0.1.0-alpha.1
```

This source-install path requires the Go toolchain declared in
[`go.mod`](./go.mod). Go may download that toolchain according to the user’s
`GOTOOLCHAIN` configuration.

Verify either installation:

```sh
acrelay version
```

See [Installation and operations](./docs/OPERATIONS.md) for PATH setup,
updates, binary removal, unsigned-download behavior, and recovery.

## Prefer Natural Language? Add The acRelay Skill

The engine is complete on its own, but most users should not need to remember
its low-level commands. The optional
[acRelay Skill in Skillstead](https://github.com/kyungseo/skillstead/tree/main/skills/acrelay)
turns requests such as “have Claude red-team this plan” into the same
binary-enforced workflow.

The Skill is an Alpha preview and is still completing its separate
Claude Code and Codex validation. It never installs, upgrades, or replaces the
engine. After installing the engine, preview the Skill by copying its complete
folder:

### Claude Code

```sh
git clone --depth 1 https://github.com/kyungseo/skillstead.git /tmp/skillstead
mkdir -p "$HOME/.claude/skills"
cp -R /tmp/skillstead/skills/acrelay "$HOME/.claude/skills/"
```

### Codex

```sh
git clone --depth 1 https://github.com/kyungseo/skillstead.git /tmp/skillstead
mkdir -p "$HOME/.agents/skills"
cp -R /tmp/skillstead/skills/acrelay "$HOME/.agents/skills/"
```

Use the default branch only while intentionally evaluating the unpublished
preview. Once the Skill has a verified release tag, pin that tag instead.
See the [Skill guide](https://github.com/kyungseo/skillstead/blob/main/skills/acrelay/README.md)
and [Skillstead installation guide](https://github.com/kyungseo/skillstead/blob/main/docs/INSTALL.md)
for project-local paths, updates, removal, and current validation status.

Example request:

> Use acRelay to have Claude red-team this plan. Keep the review record
> private, use at most three rounds, and show me the owner decisions at the
> end.

## Direct CLI: First Review

Create a private directory outside shared, synced, or repository paths. acRelay
calls the Markdown file that holds the official review history the
**canonical record**:

```sh
mkdir -p "$HOME/.acrelay/reviews"
```

Start one review:

```sh
acrelay init \
  -canonical "$HOME/.acrelay/reviews/example.md" \
  -question "Is this change ready to ship?" \
  -target ./README.md \
  -approval-actor owner \
  -ack-vendor-egress \
  -execution-surface external-cli \
  -driver-vendor codex \
  -context-relation separate
```

The `-ack-vendor-egress` flag confirms that the selected reviewer service may
receive the files being reviewed, resolved file paths, and related metadata.
The review record stays local, but Claude Code or Codex may still send data to
its provider.

Run the first reviewer round:

```sh
acrelay review \
  -canonical "$HOME/.acrelay/reviews/example.md" \
  -reviewer claude \
  -prompt "Review the target against the objective. Return examined evidence and structured findings."
```

The driver then records whether each finding was accepted, revised, defended,
or needs the owner, together with a reason. The owner answers any approval
request and runs the final `close` command. To see whether the review is ready
to close without changing anything, run:

```sh
acrelay briefing \
  -canonical "$HOME/.acrelay/reviews/example.md"
```

`briefing` only summarizes the current record. It is not approval and never
changes the review.

## What acRelay Records

- The exact files selected for review and a revision identifier that is checked
  again before important actions
- The excerpts the reviewer says it examined and its structured findings
- A recovery journal for interrupted reviewer runs; uncertain results are
  recorded as `UNKNOWN` and are not retried automatically
- The selected reviewer and the formal review-round limit
- The driver’s response and reason for every finding
- Owner approval requests and responses
- A read-only closeout summary that remains separate from the owner’s `close`
  command

These records do not prove that the reviewer understood everything or that its
review was complete, correct, or independent. They show only what acRelay
observed and what the driver, reviewer, or owner declared.

## Private State

The canonical record can contain reviewer output, prompts, file paths, and
execution details. Keep it private. This release cannot create a sanitized
copy for sharing.

`~/.acrelay` stores reviewer session identifiers and private working
directories used to continue a review. Removing the binary does **not** remove
them. Use `acrelay cleanup` with the exact canonical record and session
reference before removing retained acRelay state. acRelay cannot delete or
confirm deletion of session or configuration data owned by Claude Code, Codex,
or their providers.

## Documentation

- [Architecture and trust boundaries](./docs/ARCHITECTURE.md)
- [Behavioral and CLI reference](./docs/REFERENCE.md)
- [Installation, update, removal, and release recovery](./docs/OPERATIONS.md)
- [Release history](./CHANGELOG.md)
- [Natural-language acRelay Skill](https://github.com/kyungseo/skillstead/tree/main/skills/acrelay)
- [Skillstead installation guide](https://github.com/kyungseo/skillstead/blob/main/docs/INSTALL.md)

The Skill is the easier front door; this repository remains the authority for
engine behavior, evidence, privacy, recovery, and platform support.

## License

[Apache-2.0](./LICENSE). The software is provided without warranties or
conditions of any kind; see the license for the complete terms.
