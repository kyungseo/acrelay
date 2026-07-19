# acrelay (working name)

Portable agent collaboration relay — v1 reference implementation.

`acrelay` is a working identifier, not a public product name. Contract SSoT:
Toolstead `DR-811` (Agent Collab v1 Feasibility Contract And Session Continuity),
`DR-812` (Declared Authority Model), and `DR-813` (Durable Dispatch Transaction).

## Layout

- `internal/kernel` — collaboration/objective types, execution·governance dual
  state machines, objective-level formal round bound, attempt bound,
  confirmation cycle
- `internal/review` — review profile: canonical ReviewResult validation,
  dispatch outcome classification, fail-closed closure check
- `internal/store` — canonical Markdown artifact: pre-dispatch revision
  snapshot, content-derived fence / base64 raw blocks, owner-only atomic replace
- `internal/relay` — prepared one-shot review flow (`Prepare` → snapshots →
  objective-bound append → private dispatch journal → in-memory attempt
  admission → child start/capture → transaction-tagged canonical append), state
  as sequence-numbered blocks inside the canonical document; `store-md v0.4` /
  `dispatch-journal v0.1`
- `cmd/acrelay` — CLI: `init` / `review` / `confirm` / `disposition` /
  `advance` / `close` / `terminate` / `reconcile` / `abandon-transaction` /
  `status`

## Canonical Record Is Private

The canonical Markdown record preserves raw reviewer output including
provenance. Keep it in private local storage outside any shared, synced, or
published boundary. There is no share/export path in v1 — copying the raw
canonical into a shareable artifact requires explicit opt-in per DR-811, and
a redacted-export tool is a release-gate decision. The session handle store
(`~/.acrelay/handles.json`) is 0600 and never leaves the machine; every
canonical and handle-store mutation is serialized across processes by an
advisory `flock` on a sidecar lock file, so concurrent invocations cannot
lose each other's updates.

Every reviewer child start is preceded by a 0600 private dispatch journal.
While a journal is pending, the same canonical rejects every mutator except
`reconcile` and declared owner/arbiter `abandon-transaction`; `status` remains
available. A crash with ambiguous execution reconciles to `UNKNOWN` and never
retries automatically. Corrupt journals are recorded in the canonical before
being moved to owner-only quarantine.

`store-md v0.4` is an exact-version cutover. Existing v0.3 canonicals are not
silently migrated; re-init or a linked follow-up objective is required.

## Formal Round Bound

Each objective selects a total formal review bound from `1..5` on its first
successful `review` preflight. Omission resolves to `3`:

```
acrelay review -canonical review.md -reviewer claude -prompt-file packet.md -round-bound 5
```

The resolved value is appended to the canonical before the reviewer child is
started and cannot be changed. Later reviews may omit the flag or assert the
same value; a different value fails before adapter preparation. A pure
preflight failure does not bind the objective, while a failure after the
canonical bind leaves the policy immutable. An explicit child-start failure
consumes no formal round; ambiguous execution follows the existing durable
`UNKNOWN` and no-retry contract.

The bound covers the objective's entire review→revise→re-review loop, including
reviews after `advance`. Bound `1` therefore means one `R0` assessment with no
re-review round. A related objective keeps the reviewer session but selects a
new bound independently. Attempt and confirmation limits are unchanged.

## CLI Compatibility And Provenance

Claude Code and Codex CLI versions are observed on every invocation and stored
in provenance, but version-string equality is not an admission gate. The
adapter's known-good version is regression evidence only. An unobservable
version banner still fails before child start because provenance would be
incomplete.

Required help tokens are probed on every invocation as an advisory diagnostic.
Missing or reformatted help text does not block dispatch. The actual command,
terminal envelope or JSONL events, session identity, and structured output are
the authoritative compatibility boundary. Command/transport/session failures
and absent or malformed structured output are recorded as `FAILED` with no
automatic retry. A captured ReviewResult whose content violates the canonical
schema remains `needs-input`.

Platform-default model selection sends no model override flag. Claude records
the terminal envelope's `modelUsage` as verified. Codex uses the bounded
`codex doctor --json` `config.load` model/provider fields as an attested
effective-config observation; every other doctor field and the raw output are
discarded. Doctor failure, timeout, or malformed output records an empty,
`unverified` model observation and never blocks dispatch.

## Supported Platforms

- **macOS** — runtime-verified in this cycle: process-group termination of the
  reviewer child tree, owner-only permissions, and atomic file replace. File
  and parent-directory sync are best-effort power-loss hardening; no
  `F_FULLFSYNC` or power-loss durability claim is made.
- **Linux** — builds and shares the POSIX process-group termination path, but
  its runtime (termination, permissions, atomic replace, timeout grace) is not
  yet verified in this cycle; treat as unverified until it is.
- **Windows** — unsupported. The child-process lifecycle boundary uses POSIX
  process groups; the Windows Job Object equivalent is a follow-up port and the
  package does not build for `GOOS=windows`.

## Install / Verify

```
go build -o acrelay ./cmd/acrelay   # single binary, no runtime dependencies
go vet ./...
go test ./... -race -count=1        # deterministic suite only
```

The default Go suite isolates `HOME`, Claude/Codex config directories, XDG
state, Git global/system config, and the vendor CLI lookup path inside each
risky test package. Installed `claude` and `codex` commands are fail-fast
sentinels in this mode; fake fixtures must explicitly shadow them. A
post-suite barrier also rejects changes to the actual default
`~/.acrelay/handles.json` footprint. The suite does not use user auth,
external network access, or model/API cost.

Installed-CLI smoke is a separate script-to-binary path and is never compiled
into the default Go test suite. It requires both the explicit command and an
environment opt-in:

```
ACRELAY_LIVE_SMOKE=1 ./scripts/live-smoke.sh claude
ACRELAY_LIVE_SMOKE=1 ./scripts/live-smoke.sh codex
```

The live smoke performs an initial review, revises the synthetic target, and
then re-reviews it through the stored reviewer session. It uses the selected
CLI's current user auth/config, external network, and model/API budget. Acrelay
does not edit vendor configuration, but the vendor CLI may maintain its own
runtime/session state. The target, canonical record, session-handle store,
prompt, and built binary are synthetic temporary files removed when the script
exits. Run it only by explicit owner choice; deterministic test success does
not imply live-smoke evidence.

Uninstall by deleting the binary and, if desired, `~/.acrelay/`.
