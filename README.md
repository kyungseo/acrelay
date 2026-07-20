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
- `internal/subject` — normalized local `file` / explicit `files` / declared
  `subtree` selectors, resolved member manifests, domain-separated aggregate
- `internal/relay` — prepared one-shot review flow (`Prepare` → snapshots →
  objective-bound append → private dispatch journal → in-memory attempt
  admission → child start/capture → transaction-tagged canonical append), state
  as sequence-numbered blocks inside the canonical document; `store-md v0.6` /
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
(`~/.acrelay/handles.json`) is 0600 and never leaves the machine. Handle store
v2 binds vendor, native handle, trust-profile identity, and the reviewer cwd;
v1 stores and profile-less/cwd-less entries fail closed. A neutral cwd stays
owner-only and is reused for that handle because vendor resume lookup can be
cwd-scoped; deleting the handle removes the acrelay-owned neutral root. Every
canonical and handle-store mutation is
serialized across processes by an advisory `flock` on a sidecar lock file, so
concurrent invocations cannot lose each other's updates.

Every reviewer child start is preceded by a 0600 private dispatch journal.
While a journal is pending, the same canonical rejects every mutator except
`reconcile` and declared owner/arbiter `abandon-transaction`; `status` remains
available. A crash with ambiguous execution reconciles to `UNKNOWN` and never
retries automatically. Corrupt journals are recorded in the canonical before
being moved to owner-only quarantine.

`store-md v0.6` and handle store v2 are exact-version cutovers. Existing v0.5
canonicals and handle store v1 files are not silently migrated. Finish an old
objective with its prior binary or initialize a new v0.6 canonical and new
reviewer session.

## Review Subject

The existing single-file form remains the shortest path:

```sh
acrelay init \
  -canonical review.md \
  -question "Is this ready?" \
  -target ./artifact.md \
  -approval-actor owner@example \
  -ack-vendor-egress
```

For `-target`, the declared root is the target file's parent directory. A
symlink that resolves outside that parent fails closed.

For an explicit file set or a local subtree, pass a JSON descriptor with
`-target-spec` (mutually exclusive with `-target`). Relative roots are resolved
against the descriptor directory:

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

`file` requires one `members` entry; `files` requires one or more explicit
entries; `subtree` derives membership at every authoritative checkpoint.
Logical paths are normalized root-relative UTF-8 paths and duplicates fail
closed. `include` and `exclude` use path-prefix semantics, with `exclude`
taking precedence. There are no hidden default exclusions: `.git`, generated
files, and other names are included unless the descriptor excludes them
explicitly.

The canonical record and its `.lock` must not be selected as subject members.
A subtree selector must also not admit acrelay's dynamically created
`.dispatch-*` and `.quarantine-*` namespaces. `init` rejects such a
self-conflicting selector before creating the canonical. Keep `-canonical`
outside the subject, use a narrow subtree `include`, or exclude the canonical's
containing directory. Excluding only the canonical filename is insufficient
for a broad subtree because dispatch and quarantine filenames are generated
dynamically.

Every member must resolve to a regular file inside the declared resolved root.
Broken links, root escapes, unsupported member types, unreadable members, and
zero-member selections fail closed. Symlink identity and resolution facts are
part of the aggregate, so retargeting a link to equal bytes is still a change.

The revision is SHA-256 over a canonical byte stream beginning with
`acrelay-subject-v1\0`, followed by length-prefixed selector metadata,
resolved-root identity, member count, and sorted typed member records. A
single-file subject uses this same aggregate; it intentionally does not equal
the file's raw content digest.

Review, confirmation, advance, and close re-resolve the full selector and
re-hash every member at their authoritative checkpoints. This detects changes
across those checkpoints but is not an atomic filesystem snapshot claim. A
mutation that is fully restored between checkpoints may not be detected. Each
checkpoint performs an O(N) full enumeration and re-hash; v1 has no subject
cache, index, or filesystem watcher. Git staged patches, commits, ranges,
branches, and other change-set selectors are not supported in this version.

## Review Input Trust Boundary

Every objective stores an immutable trust policy before any vendor dispatch.
`-ack-vendor-egress` and a declared `-approval-actor` are mandatory because
the selected vendor/model may process subject content, absolute and resolved
member paths, and metadata. The acknowledgment is a dispatch gate, not data
isolation. Additional owner approvals are typed records, so later approval
gates can use the same durable flow without being conflated with Close
authority.

The default reviewer cwd is a fresh owner-only temporary directory outside the
subject. Acrelay removes it after the prepared invocation closes. Claude runs
with safe mode, an explicit subject `--add-dir`, no project MCP/config hooks,
and only `Read,Glob,Grep`. Codex runs with user config and rules ignored,
strict config parsing, and a read-only sandbox. Initial review, resume, and
confirmation use the same objective trust profile; native handles are resumed
only when their stored profile identity matches, and resume reuses the exact
handle-bound cwd instead of creating a different neutral root.

In-target cwd is an explicitly unsafe objective mode. It must be approved at
`init` with `-allow-in-target-workdir`; the approval text records repository
code-execution, read, and egress risk. Under this mode, omitted `-workdir`
resolves to the subject root and an explicitly supplied cwd must remain inside
that root. A neutral objective rejects every caller-supplied cwd and always
uses an acrelay-owned temporary root.

The assurance levels are deliberately different:

| Axis | State | Boundary |
| --- | --- | --- |
| Governance authority | hard | Reviewer output remains evidence and cannot become owner approval or Close authority. |
| Repo customization | hard by default | Acrelay-owned neutral cwd plus adapter customization restrictions; failure blocks dispatch. |
| Mutation | hard when version-verified | Claude exposes no write-capable tools; Codex uses a read-only sandbox; complete-subject aggregate checks remain defense in depth. |
| Instruction hierarchy | labeled mitigation | Trust preamble and Claude system prompt are observed mitigations, not a guarantee against prompt injection. |
| Read scope | convention-only | Neither adapter provides target-only read isolation. Absence of observed leakage is not proof. |
| User/admin customization | labeled residual | Admin-managed or vendor-external policy may still apply. |
| Vendor egress | hard acknowledgment gate | Explicit immutable approval and per-dispatch provenance; no network or processing isolation is claimed. |

Driver prompts should reference logical member paths instead of quoting target
content into the trusted request. This is a documented convention, not runtime
enforcement. Working directory, read-only, safe-mode, and tool names must not
be interpreted as network isolation, target-only reads, DLP, or protection
from a fully privileged host administrator.

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
in provenance. General transport compatibility remains capability-first, but
the security-critical restriction profile is bound to positive behavioral
evidence for an exact CLI version. The current verified references are Claude
Code `2.1.215` and Codex CLI `0.144.1`. A different or unobservable version
fails before child start and requires a new owner-reviewed capability spike;
there is no unrestricted fallback.

Required help tokens are probed on every invocation as an advisory diagnostic.
Missing or reformatted help text remains diagnostic-only; help presence alone
cannot prove that a restriction works. Exact version-bound behavioral evidence,
the actual command, terminal envelope or JSONL events, session identity, and
structured output form the compatibility boundary. Command/transport/session failures
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
then re-reviews it through the stored reviewer session. It fails unless the
final status reaches `CLOSABLE`; a captured failed round cannot produce a
successful script exit. It uses the selected
CLI's current user auth, external network, and model/API budget while the
restricted profile suppresses project/user customization as described above.
Acrelay does not edit vendor configuration, but the vendor CLI may maintain its
own runtime/session state. The target, canonical record, session-handle store,
prompt, and built binary are synthetic temporary files removed when the script
exits. Run it only by explicit owner choice; deterministic test success does
not imply live-smoke evidence.

Uninstall by deleting the binary and, if desired, `~/.acrelay/`.
