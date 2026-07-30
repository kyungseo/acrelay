# acRelay Skill

**English** · [한국어](./README.ko.md)

Reviewing a plan or implementation with another coding agent can improve its
direction, reveal defects that the driver missed, and make the final result
more complete. This Skill makes those reviews easy to start in natural
language.

In this guide, the agent doing the work is the **driver**, the separate CLI
that challenges the work is the **reviewer**, and the person who makes the
final decision is the **owner**.

This official Skill is distributed with the standalone
[acRelay engine](https://github.com/kyungseo/acrelay). The engine is one
executable and does not use a dedicated daemon, server, or database. Install
the engine and the complete Skill package, then ask for a review of a plan,
document, file, selected implementation files, or a declared directory tree.
The Skill uses the engine's provided capabilities as-is; it does not
reimplement or bypass them.

The two parts have different jobs:

- **Skill:** passes your natural-language request to acRelay.
- **Engine:** checks the files, starts a separate Claude Code or Codex CLI
  reviewer, and records the result.

> [!IMPORTANT]
> Install both the Skill and the engine. The Skill cannot run a review by
> itself.

[![A user asks Codex to bring Claude into a bounded acRelay review; Claude exits after the round, and the user decides what changes](./assets/acrelay-review-flow@2x.png)](./assets/acrelay-review-flow.svg)

## Start A First Review

1. Confirm that you are using Codex App, Codex CLI, or Claude Code and that the
   reviewer CLI you want is already installed and signed in.
2. [Install the engine and complete Skill package](#install-the-engine-and-skill).
3. Start a fresh agent session so the newly installed Skill can be discovered.
4. Ask for a review, naming the file or bounded file set and either Claude Code
   or Codex as reviewer.
5. When asked, confirm that acRelay may send the review content, resolved
   paths, and metadata to the selected reviewer service.
6. Read the verdict and findings. The reviewer advises; you remain the owner
   who decides what changes and whether to close the review.

## What It Replaces

For every manual round, someone normally copies the request to the reviewer and
copies the result back to the driver. Three rounds can require six
copy-and-paste handoffs.

| Before | With the acRelay Skill |
| --- | --- |
| Move each request and result between agents | Start in natural language; the Skill calls the engine |
| Remember the reviewed revision and open findings | Keep the revision, findings, and responses in one record on your computer |
| Let the conversation drift until someone agrees | Use 1–5 review rounds (default 3), then return the decision to the owner |

Each invocation starts one bounded review objective, which may use 1–5 review
rounds (default 3). acRelay runs only when invoked and never continues as a
background service.

## Publication Status

This Skill is part of the **Public Validation Preview**. It is
**Experimental**, broader validation is still **Validation pending**, and
neither Claude Code nor Codex is presented as generally `Supported`. The
author completed live reviews through both reviewer paths, but an invited
non-author still needs to validate the experience before the project presents
it as generally `Supported`.

`Validation pending` does not mean the package is missing. It means this
documented preview is available to evaluate, but the project does not yet
claim general runtime support.

## Before You Start

You should already be using Codex App, Codex CLI, or Claude Code. A review
runs through Claude Code CLI or Codex CLI, so at least one of those reviewer
CLIs must already be installed, signed in, and working. Codex App can drive the
work, but the reviewer still runs through a CLI.

Here, **App** means the desktop interface and **CLI** means a command that runs
in Terminal. The Skill and engine do not install or sign in to reviewer tools.

## Install The Engine And Skill

The exact `v0.1.0-alpha.4` `acrelay` command must be available from Terminal
(on `PATH`). The current prebuilt binary is for macOS Apple Silicon
(`darwin/arm64`). In Terminal, run `uname -m` and continue with this installer
only when the result is `arm64`. The installer never substitutes `latest`.

```sh
curl -fsSL https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.4/scripts/install.sh |
  bash -s -- --skill-host codex
```

Use `claude` instead of `codex`, or `both`, with `--skill-host`. The installer
is pinned to the release and checks the archive containing both engine and
Skill against its published checksum. It preserves a locally different Skill
unless you explicitly add `--replace`. If you prefer to inspect the installer
before running it, or want the pinned `go install` alternative, follow the
[acRelay installation guide](https://github.com/kyungseo/acrelay/blob/v0.1.0-alpha.4/docs/OPERATIONS.md).

Windows is the next platform-support target. Its core runtime lane is already
verified; platform-specific Claude Code and Codex review validation comes next,
followed by any patches that evidence requires. Linux core runtime CI remains
in the source test matrix, but this preview provides no Linux artifact or
live-review support. Until a combination is verified, the engine stops before
sending files.

## Manual Or Project-Local Skill Install

Install from the exact `v0.1.0-alpha.4` acRelay tag and copy the complete
`skills/acrelay` folder; do not copy `SKILL.md` by itself.

```sh
git clone --depth 1 --branch v0.1.0-alpha.4 https://github.com/kyungseo/acrelay.git /tmp/acrelay-v0.1.0-alpha.4
```

### Claude Code

```sh
mkdir -p "$HOME/.claude/skills"
cp -R /tmp/acrelay-v0.1.0-alpha.4/skills/acrelay "$HOME/.claude/skills/"
```

### Codex

```sh
mkdir -p "$HOME/.agents/skills"
cp -R /tmp/acrelay-v0.1.0-alpha.4/skills/acrelay "$HOME/.agents/skills/"
```

### Windows PowerShell

On Windows, clone the same exact tag into a temporary folder:

```powershell
$source = Join-Path ([System.IO.Path]::GetTempPath()) "acrelay-v0.1.0-alpha.4"
git clone --depth 1 --branch v0.1.0-alpha.4 https://github.com/kyungseo/acrelay.git $source
```

For Claude Code:

```powershell
$claudeSkill = Join-Path $HOME ".claude/skills/acrelay"
New-Item -ItemType Directory -Force (Split-Path $claudeSkill) | Out-Null
Remove-Item -LiteralPath $claudeSkill -Recurse -Force -ErrorAction SilentlyContinue
Copy-Item -Recurse (Join-Path $source "skills/acrelay") $claudeSkill
```

For Codex:

```powershell
$codexSkill = Join-Path $HOME ".agents/skills/acrelay"
New-Item -ItemType Directory -Force (Split-Path $codexSkill) | Out-Null
Remove-Item -LiteralPath $codexSkill -Recurse -Force -ErrorAction SilentlyContinue
Copy-Item -Recurse (Join-Path $source "skills/acrelay") $codexSkill
```

These commands install only the Skill. They do not claim a Windows engine
artifact or live-review support.

For a project-local install, copy the folder to `.claude/skills/acrelay` or
`.agents/skills/acrelay`. After any new or updated Skill installation, start a
fresh agent session so discovery does not depend on the current session's
cache. Update by replacing the complete folder from another exact tag;
uninstall by deleting only the installed `acrelay` folder.

The Skill itself never installs or upgrades the engine while it is running. A
missing or different engine version stops with an explanation; it never
bypasses acRelay by calling the reviewer directly.

## Example Requests

### Challenge a plan

```text
Use acRelay to have Claude challenge this plan. Summarize the decisions I need to make at the end.
```

If you do not specify a limit, acRelay allows up to three review rounds. You
may request any limit from one to five.

### Review one file

```text
Use acRelay to have Codex review this file. Summarize the evidence it checked and the problems it found.
```

### Review an implementation

```text
Use acRelay to check whether the current implementation matches the approved plan and summarize any gaps.
```

acRelay v0.1.0-alpha.4 accepts a file, explicit files, or a declared subtree.
It does not yet accept a PR URL, staged patch, commit range, or branch
comparison as a first-class selector. Check out the intended revision and name
the files or subtree instead.

### Choose review access

```text
Use acRelay's research profile to have Claude verify the current external facts in this plan. Keep local context to these named files.
```

`contained` is the default, `contextual` adds an exact non-authoritative local
context manifest, and `research` additionally permits bounded web search/fetch
with separate egress consent. Research is for current factual verification,
not a default merely because internet access exists. If subject plus context
exceeds 8 files or 128 KiB, the Skill narrows the scope or asks once for
explicit broad-scope consent instead of silently dispatching.

### Use only Claude Code or only Codex

```text
I am working in Claude Code. Use a separate Claude Code CLI reviewer to review this work.
```

A separate CLI session of the same tool can act as reviewer. This is useful
when you use only Claude Code or only Codex, although the driver and reviewer
may share blind spots. This preview reviews through a CLI session; it does not
take a result from the driver tool's built-in subagent directly.

### Choose a driver and reviewer

| How you work | Driver | Reviewer |
| --- | --- | --- |
| Drive from Codex App | Codex App | Claude Code CLI or Codex CLI |
| Drive from Claude Code | Claude Code CLI | Codex CLI or a separate Claude Code CLI session |
| Use only Claude Code | Claude Code CLI | A separate Claude Code CLI session |
| Use only Codex | Codex CLI | A separate Codex CLI session |

- Claude Code CLI and Codex CLI can be the driver or reviewer in the
  combinations listed for this preview.
- Codex App can be the driver, but the reviewer must be a CLI.
- Claude App has no direct acRelay path.
- Antigravity is not part of this preview.

A reviewer from another tool can challenge assumptions that the driver may
not notice. A same-tool reviewer is still useful, but acRelay records that the
two contexts may share blind spots.

### Check the current status

```text
Summarize the current state of this acRelay review and the decisions I need to make.
```

## How The Skill Prepares A Review

The Skill infers what it safely can from your request and the app or CLI
running it. Unless you request otherwise, it uses `owner` as the owner label,
stores the private record below `~/.acrelay/records/`, keeps the reviewer
context separate, and allows up to three rounds.

Before dispatch, it determines:

- the exact file, files, or subtree to review,
- the review question,
- Claude Code or Codex as reviewer,
- whether the declared subject is enough or exact auxiliary context or current
  external research is needed,
- and how the driver and reviewer contexts are related.

It asks only for information that cannot be inferred safely. The ordinary
first-use question is whether review content, resolved paths, and metadata may
be sent to the selected reviewer service. Research adds search-query and
external-URL egress to that same consent question.

The reviewer CLI may use its provider’s network and model tokens. A local
review record does not mean local model inference.

## Boundaries

- The binary—not the Skill—owns review state, evidence, recovery, cleanup, and
  Close.
- acRelay automates the reviewer run, not the driver’s response or the owner’s
  decision.
- A separate context or reviewer vendor does not prove independent judgment.
- Recorded excerpts do not prove understanding, completeness, or correctness.
- `briefing` only summarizes the current record; it is never approval.
- A reviewer run recorded as `UNKNOWN` is never retried automatically.
- Removing the binary or Skill does not remove `~/.acrelay`, private review
  records, or reviewer-vendor data.

For exact commands, formats, platform evidence, and recovery rules, use the
[acRelay engine documentation](https://github.com/kyungseo/acrelay).
