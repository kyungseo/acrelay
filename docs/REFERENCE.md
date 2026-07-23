# acRelay Behavioral And CLI Reference

**English** · [한국어](./REFERENCE.ko.md)

This is the detailed reference for the acRelay `v0.1.0-alpha.1` behavioral
contract. Start with the [root README](../README.md) for installation and the
shortest review path.

## How To Read This Reference

This page is for operators and maintainers who need the exact behavior behind
the shorter README and operations guide. The following terms appear throughout:

- **review objective:** one review from `init` until the owner closes or
  terminates it
- **canonical record:** the private Markdown file that is the authoritative
  history for that review
- **session reference (`ref`):** the identifier acRelay uses to resume or clean
  up one specific reviewer session
- **formal round:** one reviewer pass that may produce findings
- **disposition:** the driver’s recorded response to a finding—accept, revise,
  defend, or ask the owner
- **fail closed:** stop without silently changing the review, consuming a round
  where prohibited, or falling back to a less restricted path
- **vendor egress:** review content, resolved paths, and metadata sent to the
  selected Claude Code or Codex reviewer

Command names, state values, and format identifiers remain in their exact
machine-readable form.

## Code Map For Maintainers

- `internal/kernel` — collaboration/objective types, execution·governance dual
  state machines, objective-level formal round bound, attempt bound,
  confirmation cycle
- `internal/review` — `review-profile v0.2`: examined evidence, structured
  findings, driver dispositions, review-time approval requests, and the
  fail-closed closure check
- `internal/store` — canonical Markdown artifact: pre-dispatch revision
  snapshot, content-derived fence / base64 raw blocks, owner-only atomic replace
- `internal/subject` — normalized local `file` / explicit `files` / declared
  `subtree` selectors, resolved member manifests, domain-separated aggregate
- `internal/relay` — prepared one-shot review flow (`Prepare` → snapshots →
  objective-bound append → private dispatch journal → in-memory attempt
  admission → child start/capture → transaction-tagged canonical append), state
  as sequence-numbered blocks inside the canonical document; `store-md v0.9` /
  `dispatch-journal v0.1`
- `cmd/acrelay` — CLI: `init` / `review` / `confirm` / `disposition` /
  `request-approval` / `respond-approval` / `withdraw-approval` / `advance` /
  `close` / `terminate` / `reconcile` / `abandon-transaction` / `status` /
  `cleanup` / `briefing`

## Keep The Canonical Record Private

The canonical Markdown record preserves raw reviewer output including
provenance. Keep it in private local storage outside any shared, synced, or
published boundary. `init` fails closed when it finds a `.git` ancestor or a
supported, verified sync-root signal. Detection is intentionally incomplete:
no warning is not proof that a location is safe. A one-shot override requires
`-allow-unsafe-location`, the existing `-approval-actor`, and a non-empty
`-unsafe-location-reason`; the actor, detected signal, and rationale are
written to that canonical and do not become a reusable policy.
The v1 sync signal set is limited to existing OneDrive roots declared by the
platform `OneDrive`, `OneDriveCommercial`, or `OneDriveConsumer` environment
variables and the existing macOS iCloud Drive root. Dropbox, Google Drive,
aliases, and other providers are not detected unless a later implementation
adds a verified signal.

There is no share/export path in v1. The raw canonical is owner-retained and
acrelay never deletes or automatically redacts it. Copying it into a shareable
artifact may expose private prompts, evidence, and provenance. Public examples
must use synthetic data; a redacted-export schema and leakage contract require
a separate future decision and implementation. The session handle store
(`~/.acrelay/handles.json`) is private storage — 0600 on POSIX, a
creation-time protected current-user-only DACL on Windows — and never leaves
the machine. Local storage does not mean local model inference: the selected
reviewer vendor may still process approved subject content, paths, and metadata,
and vendor-owned session/config/history retention is outside acrelay's cleanup
authority. Handle store v2 binds vendor, native handle, trust-profile identity,
and the reviewer cwd; v1 stores and profile-less/cwd-less entries fail closed.
A neutral cwd stays owner-only and is reused for that handle because vendor
resume lookup can be cwd-scoped. New neutral cwd directories live under the
handle store's private `runtime/` directory, not the OS temp directory. If that
paired root would fall inside the review subject, acrelay uses its platform
user-config fallback instead. Existing temp-bound handles are not moved or
copied; they remain diagnosable until an explicit session reset or authorized
cleanup. Every
canonical and handle-store mutation is
serialized across processes by an exclusive lock on a sidecar lock file —
POSIX uses an advisory `flock`; Windows uses a `LockFileEx` fixed byte range
(cooperative serialization, not flock equivalence) — so cooperating acrelay
invocations do not lose each other's updates. Either lock binds only
lock-taking processes and depends on local-filesystem support; a
non-cooperating writer is stopped by the store revision guard.

Every reviewer child start is preceded by a 0600 private dispatch journal.
While a journal is pending, the same canonical rejects every mutator except
`reconcile` and declared owner/arbiter `abandon-transaction`; `status` remains
available. A crash with ambiguous execution reconciles to `UNKNOWN` and never
retries automatically. Corrupt journals are recorded in the canonical before
being moved to owner-only quarantine.

## Private Artifact Lifecycle

| Artifact | Location and sensitivity | Retention / deletion authority |
| --- | --- | --- |
| Raw canonical | Owner-selected private path; raw review evidence | Owner-retained. Acrelay never deletes or applies a TTL. |
| Canonical `.lock` | Beside the canonical; private coordination state | Acrelay opens/reuses it. An orphan lock is not auto-deleted because inactivity is not provable. |
| `.dispatch-*` journal | Beside the canonical; private pending execution evidence | Removed after captured reconciliation, or moved to quarantine only after declared abandon is recorded canonical-first. |
| `.quarantine-*` sidecar | Beside the canonical; private anomaly evidence | Exact transaction/digest purge only after durable canonical disposition and owner reason. |
| Handle store and `.lock` | `~/.acrelay/` by default or explicit private path; native session secret | Exact ref cleanup only. No store-global liveness, TTL, or GC. |
| Neutral cwd | Paired private `runtime/` root (platform fallback when needed) | Retained for resume; exact ref cleanup removes only the verified acrelay-owned directory. |
| Vendor session/config/history | Vendor-owned location and retention | Outside acrelay authority; never reported as deleted by local cleanup. |

Cleanup is explicit, canonical/ref-scoped, and non-destructive by default:

```sh
acrelay cleanup -canonical review.md -ref sref-... -mode list
acrelay cleanup -canonical review.md -ref sref-... -mode dry-run
acrelay cleanup -canonical review.md -ref sref-... -mode apply \
  -actor owner \
  -continuity-abandon-reason "No related objective will resume this session."
```

`OPEN`, `DECISION_REQUIRED`, and `CLOSABLE` objectives cannot clean up their
session. A terminal objective still retains its handle and cwd until the owner
names the exact canonical/ref and records that related-objective continuity is
abandoned. Apply writes that declaration to the canonical before removing the
mapping and acrelay-owned neutral cwd. In-target cwd paths are never deleted.
Repeated cleanup converges to `already_clean`. Handle-store mutation and cwd
removal are not one filesystem transaction; a crash can leave a retained
mapping whose cwd is missing, and a retry diagnoses and completes that state.

Pending journals and `UNKNOWN` transactions block session cleanup until normal
`reconcile` or declared `abandon-transaction` resolves them. Quarantine purge
is a separate exact-path action and requires the abandoned transaction's full
digest to remain in the canonical:

```sh
acrelay cleanup -canonical review.md \
  -quarantine review.md.quarantine-tx-...-<digest12>.json \
  -mode apply -actor owner \
  -sidecar-reason "The canonical disposition is durable."
```

If a canonical was externally deleted, `-orphan-sidecar` can list, dry-run, or
remove one exact private dispatch/quarantine sidecar after an owner declaration.
It never scans a store or computes global reference liveness. A lone `.lock`
cannot prove that no process owns it, so automatic lock deletion stays blocked.
None of these operations claims secure erase, backup/cloud-copy recovery, or
deletion of vendor-owned data.

Run session cleanup before deleting the raw canonical, and retain the canonical
until cleanup reports `applied` or `already_clean`. If the canonical is deleted
first, acrelay can no longer verify its bound session ref or record continuity
abandonment. The handle mapping and durable cwd therefore remain owner-retained;
there is no ref-only deletion escape hatch or global orphan lookup.

The lifecycle gate follows the persisted state, not prose: `OPEN`,
`DECISION_REQUIRED`, and `CLOSABLE` preserve the session; `CLOSED`, `ABANDONED`,
and `SUPERSEDED` permit only owner-declared exact cleanup. A pending journal or
`UNKNOWN` transaction blocks it. Corrupt/version-mismatched stores and cwd paths
outside the durable or recognized legacy namespace fail closed. A missing cwd
is diagnosed and requires explicit reset for further resume; it is never
silently recreated or migrated.

`status -canonical review.md` uses the default handle store for bounded
lifecycle diagnostics; pass the same `-handles` path used for review when it
was explicit. Status reports durable, legacy, missing, or unavailable state
without printing the native handle or cwd path and without creating storage.

`store-md v0.9` and handle store v2 are exact-version cutovers. Existing v0.8
canonicals and handle store v1 files are not silently migrated. Finish an old
objective with its prior binary or initialize a new v0.9 canonical and new
reviewer session.

## Dispatch Reliability And Failure Causes

Dispatch termination follows a fixed precedence. A valid
terminal contract with structured output classifies through the normal
result path. A typed vendor failure event is `FAILED` with a cause. A child
that dies from a signal, a parent-signal cancellation, a hard-cap expiry, or
a crash reconcile is `UNKNOWN`: the runtime cannot assert whether vendor-side
execution completed, so re-dispatch stays forbidden. `UNKNOWN` is an
ambiguity marker, never proof that the reviewer stopped safely. A clean exit
without the terminal contract is `FAILED` with `transport.missing-terminal`;
startup/idle expiry stays `FAILED(timeout)`; only `Prepare` and explicit
child start failures are non-consuming.

Every FAILED or UNKNOWN dispatch carries exactly one `failure-cause v0.1`
record on the transaction ledger: a bounded registry code plus a source
state. Each code permits only specific sources — `observed` (runtime-verified
fact: signal, timeout, exit status), `vendor-declared` (a typed vendor event
field), or `inferred` (classified from vendor text, never a verified fact) —
and the load gate rejects a code/source pair the registry does not allow, so a
persisted pair cannot carry a source the registry disallows for that code (this is an allowlist gate, not tamper-proofing of the source field). Raw stdout/stderr
stays in the canonical as before; the typed cause never stores vendor text,
and `status` renders only the allowlisted phrase for the latest cause. Exit
codes alone never decide a semantic cause; unclassifiable failures record
`unknown`.

Owner-remediation categories (`vendor.quota`, `vendor.auth`,
`vendor.network`, `vendor.service-unavailable`, `vendor.tool-policy`) are
registered but assigned only from `vendor-declared` or version-bound
`inferred` evidence. Neither vendor CLI currently exposes a typed
error-category field, so in this version such failures record the mechanism
cause or `unknown` rather than a guessed remediation category; populating
these from live vendor evidence is a separately scoped follow-up.

Persisted native resume handles are validated against vendor-safe formats
before any child start; a malformed handle fails closed without consuming an
attempt and is never silently replaced by a new session. A vendor-side
resume rejection after child start remains a consuming `FAILED` with the
`resume-handle-invalid` cause (AR-1). On POSIX, the first parent
SIGINT/SIGTERM cancels the dispatch gracefully (group SIGTERM, then SIGKILL
after the grace window) and classifies `canceled.parent-signal`; a second
signal force-kills the tracked child groups and exits. On Windows only
console interrupts (`os.Interrupt`) are deliverable and cancellation is an
immediate job termination with no graceful stage. A parent killed with SIGKILL cannot run
cleanup: the child group may orphan and the durable journal remains the
authoritative recovery path. The POSIX pid-reuse window around group kill
and the deterministic suite's inability to observe real vendor error
phrasing are documented residual risks.

## Platform Support

Support is stated per surface and per recorded lane — never as a single
"Windows supported" claim, and lanes are never aggregated.

- macOS arm64: core runtime verified (local + hosted CI lane green);
  real-vendor dispatch verified on the recorded local darwin/arm64 vendor
  tuples (recorded restriction evidence — no vendor dispatch runs in CI).
  File and parent-directory sync remain best-effort power-loss hardening; no
  power-loss durability claim is made on any platform.
- Linux amd64: core runtime verified (GitHub-hosted CI lane green, race
  included). Real vendor dispatch stays unsupported pending a platform-
  specific restriction spike.
- Windows Server x64: core runtime verified (GitHub-hosted `windows-latest`
  lane green, race included). Real vendor dispatch stays unsupported pending
  a platform-specific restriction spike.
- Windows 11 ARM64: core runtime verified on an owner-operated UTM guest
  (guest-local NTFS, standard user, deterministic suite and Windows
  capability fixtures; no race claim on this lane). Real vendor dispatch
  stays unsupported pending a platform-specific restriction spike.

Real-vendor dispatch for Linux, Windows, and any vendor+version+GOOS+GOARCH
tuple without recorded restriction evidence is fail-closed; only the verified
darwin/arm64 tuples are supported. Lane evidence is never aggregated across
platforms.

This is a release-evidence boundary, not a decision to remain Apple Silicon
only. Linux and Windows reviewer validation is planned as the next
support-expansion step. Any platform patch and support claim follows reviewed
restriction evidence; no date or warning-free path is promised in advance.
Intel Mac currently has neither a distributed artifact nor verified
real-review evidence.

Platform behavior differences are recorded, not equalized: Windows
cancellation is an immediate Job Object termination (no SIGTERM-like graceful
stage); an external TerminateProcess is not distinguishable from an ordinary
nonzero exit, so the POSIX signal-death UNKNOWN classification has no Windows
runtime equivalent; Windows exclusive locking is cooperative serialization
over a fixed byte range (not flock equivalence); private storage on Windows
is a creation-time protected DACL whose only explicit allow ACE is the
current user SID — ordinary cross-user access denial is the whole privacy
claim, and privileged-administrator takeover is not defended against.

## Reviewer Topology And Independence Claims

acrelay automates only the reviewer leg, so it can never observe the driver
agent. `review-topology v0.1` therefore records the reviewer relation as
source-qualified facets instead of an independence claim: `reviewer_vendor`
is runtime-observed once a dispatch has recorded the vendor fact, and before
that projects as `none` with the `unknown-undeclared` source;
`reviewer_session_mode` projects as runtime-observed across its values —
`new`/`resumed`/`reset` after an observation, `carried`/`none` before one —
except the ambiguity state `unknown` (an unverifiable session outcome, e.g. a
prepared-journal UNKNOWN reconcile), which projects with the
`unknown-undeclared` source; `execution_surface`,
`driver_vendor`, and `context_relation` are operator-declared init policy
(`-execution-surface external-cli`, `-driver-vendor claude|codex|other`,
`-context-relation separate|shared`) — the CLI validates the surface value
but never presents it as a runtime observation; `vendor_relation` is derived
and marked `derived-not-verified`. An
omitted declaration records the explicit `undeclared` fact and never upgrades
to any topology claim. The declaration is objective-immutable: changing the
topology relation requires a new objective; reviewer vendor and session mode
are recorded at dispatch as facts, not policy changes.

The derived profile is a display label only: `cross-vendor-external`,
`same-vendor-external`, or `undeclared`. `independence verified` is never
asserted by any profile — a declared separation only ever projects
`driver_session_separation=declared-not-verified`. Briefing and `status`
render the facets with their sources, and the briefing adds cautions for
`same-vendor-review` (correlated blind spots survive a separate session),
`topology-undeclared`, and `reviewer-session-resumed` (a resumed or carried
session is not a fresh review context). A single-agent user can therefore run
a same-vendor separate reviewer without a second vendor account, with the
same-vendor relation, the declared-not-verified separation state, and the
correlated-blind-spot caution stated in the output rather than hidden.

`-execution-surface host-subagent` (a reviewer running as the driver's own
host-orchestrated subagent) is explicitly unsupported and fails closed with
an actionable diagnostic: this standalone CLI has no typed ingress for
host-produced review results, and there is no silent fallback to another
topology. That path is deferred until a stable host API and a typed
external-result ingress contract exist.

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
isolation. Immutable trust approvals remain separate from mutable review-time
approval requests. Both preserve declared accountability, but neither is
authentication, RBAC, or Close authority.

The default reviewer cwd is a fresh owner-only directory in acrelay's durable
private runtime root outside the subject. It is retained with the opaque session
reference for resume and removed only by explicitly authorized lifecycle
cleanup. Claude runs
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
uses an acrelay-owned durable root.

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

## Review Evidence Contract

Every result-valid review, including `approve` with no findings, must return at
least one `examined` anchor. A text anchor contains a logical member, a bounded
1-based inclusive line range (maximum 40 lines), a quoted excerpt (maximum
4096 bytes), and a non-empty claim. Acrelay compares the excerpt with captured
pre-dispatch member bytes and binds the normalized anchor to that member digest
and the objective aggregate. Digest or aggregate echo alone is not evidence.
Non-UTF-8 or NUL-bearing members use an explicit `opaque` anchor and remain
`reviewer-declared`; a text member cannot be downgraded to opaque. A zero-byte
member uses an explicit `empty-member` anchor and also remains
`reviewer-declared`, because there are no content bytes to match.

Exact byte comparison runs first. When the captured member contains CRLF and
exact comparison fails, acrelay may compare the same bounded excerpt after
canonical CRLF-to-LF normalization. A normalized match records both the
distinct `content-match-normalized` assurance and the `crlf-to-lf`
normalization fact; it never becomes exact `content-match`.

These assurance labels describe different facts:

| State | Boundary |
| --- | --- |
| `dispatch-valid` | The child/session/envelope and structured result were valid. |
| `content-match` | A bounded returned excerpt exactly matched authoritative captured bytes. It does not prove understanding. |
| `content-match-normalized` | The exact match failed, but the bounded excerpt matched after declared CRLF-to-LF normalization. It does not prove understanding. |
| `reviewer-declared` | The reviewer self-reported a claim, severity, opaque examination, or empty-member examination. |
| `synthetic-sampled` | A fresh-session synthetic fixture sample observed behavior. It is not a user-target correctness guarantee. |

Reviewer findings contain `summary`, `reviewer_severity`, evidence anchor IDs,
and `recommendation`. The relay mints finding IDs and owns blocking policy:
`critical`/`high` are blocking; `medium`/`low` are advisory. An `approve` result
with a runtime-blocking finding is preserved as a contradiction and keeps
governance `DECISION_REQUIRED`; it is not a transport failure. This is a
visibility and disposition gate, not a permanent terminal block: valid driver
dispositions and any required owner response can still satisfy the later
Close check.

Every driver disposition requires `-rationale`. `accept` and `revise` also
require `-follow-up` (use the explicit value `no-action` when appropriate):

```sh
acrelay disposition \
  -canonical review.md \
  -finding R0-F1 \
  -decision revise \
  -rationale "The evidence is valid." \
  -follow-up "Apply the fix and advance the target."
```

Review-time approval request types are open-ended namespaced strings rather
than a fixed category enum. A request JSON file contains `type`, exact `scope`,
`reason`, and one or more `{id, description}` options:

```sh
acrelay request-approval \
  -canonical review.md \
  -request-file approval-request.json \
  -role driver \
  -requester codex

acrelay respond-approval \
  -canonical review.md \
  -request AR-1 \
  -actor owner \
  -verbatim-file owner-response.txt \
  -responded-at 2026-07-20 \
  -decision accept-current \
  -scope "R0-F1 on the current target only" \
  -durable-anchor "canonical#owner-response-AR-1" \
  -unambiguous
```

The verbatim response is appended even when incomplete or ambiguous. A request
resolves only when actor, original text, date, durable anchor, explicit
unambiguous declaration, option ID, and exact decision scope are present and
match. Otherwise it stays open. Open requests block clean `Close` only:
`terminate`, `advance`, and confirmation remain available. `advance` carries
them forward and marks open requests stale; stale requests do not auto-resolve.
`status` displays open/stale requests and evidence assurance counts.

Confirmation output also requires non-empty examined evidence for every
confirmed/not-confirmed finding judgment. Quoted excerpts increase reviewer
output, vendor egress, and private canonical size; they remain within the
existing approved content-egress scope. Synthetic defect seeds exist only in
test fixtures, and every synthetic sample uses a fresh session. Acrelay never
inserts seeds into a user target.

## Closeout Briefing

`briefing` is a pure, read-only owner decision input. It does not acknowledge,
approve, close, dispatch, retry, consume a round/attempt, or mutate the
canonical record:

```sh
acrelay briefing -canonical review.md
acrelay briefing -canonical review.md -format json
acrelay briefing -canonical review.md -format json -check
```

The existing `status` command remains a short operational snapshot.
`briefing` owns the decision-oriented human render and the versioned
`briefing-output v0.2` machine schema. Both formats are rendered from the same
allowlisted typed projection; wrappers must consume JSON instead of reparsing
the canonical Markdown or human prose.

Readiness has four states:

| State | Meaning |
| --- | --- |
| `ready` | The current Close preconditions pass and there are no cautions. |
| `ready-with-cautions` | Close preconditions pass, but the owner must review advisory facts such as not-confirmed/escalated confirmation or contradiction history. |
| `blocked` | The shared Close-readiness gate currently fails. |
| `terminal` | The objective is already `CLOSED`, `SUPERSEDED`, or `ABANDONED`; it is neither ready nor blocked. |

Briefing and `close` share the same typed Close-readiness evaluation. `close`
re-runs it under the canonical lock, so a briefing is never an authorization
or a promise that a later Close will succeed. Confirmation status remains
advisory under the current contract: not-confirmed or escalated confirmation
does not silently become a new Close prerequisite. Making it one would be a
separate governance and persisted-contract decision.

Without `-check`, every successfully rendered business state exits 0. With
`-check`, the stable exit classification is `0=ready`,
`3=ready-with-cautions`, `4=blocked`, and `5=terminal`; parse, I/O, or canonical
integrity failures continue to use exit 1. This keeps render success distinct
from owner-facing business readiness.

The output includes the canonical snapshot revision, subject aggregate,
logical target members, final reviewed round, examined claims and assurance,
structured findings/dispositions, confirmation state, approval completeness,
typed blocking/caution reasons, material follow-up/advance facts, and next
actions. Confirmation `claimed_delta` prose is intentionally excluded because
it is an unverified driver claim; the renderer never reparses prose to recover
it. Evidence excerpts are also omitted by default. `content-match` still means
only that the reviewer returned matching bytes, not understanding or
completeness.

Briefing output is private local material, not a redacted export. The DTO does
not contain raw reviewer blocks, session/native handles, subject or resolved
root paths, resolved member paths, evidence excerpts, owner response verbatim,
durable-anchor values, or raw error strings. It may include structured review,
driver, owner-decision, vendor, and trust-policy facts needed for the owner's
decision. Public/redacted export remains outside v1.

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
evidence for an exact CLI version AND an exact GOOS/GOARCH (evidence is never
promoted across platforms). The current verified references are Claude Code
`2.1.217` and Codex CLI `0.144.1`, both on `darwin/arm64` only (2026-07-22
restriction spike). A different or unobservable version — or any platform
absent from the verified list — fails before child start and requires a new
owner-reviewed, platform-specific capability spike; there is no unrestricted
fallback.

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

## Build And Deterministic Verification

```sh
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

```sh
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

Remove the binary separately from private state. Binary removal never deletes
`~/.acrelay`, an explicit handle store, durable neutral working directories,
owner-selected raw canonicals, or vendor-owned data. Use the exact,
canonical-scoped `acrelay cleanup` contract before any state removal; acRelay
does not claim secure deletion.
