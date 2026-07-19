# acrelay (working name)

Portable agent collaboration relay — v1 reference implementation.

`acrelay` is a working identifier, not a public product name. Contract SSoT:
Toolstead `DR-811` (Agent Collab v1 Feasibility Contract And Session Continuity),
`DR-812` (Declared Authority Model), and `DR-813` (Durable Dispatch Transaction).

## Layout

- `internal/kernel` — collaboration/objective types, execution·governance dual
  state machines, R0..R2 round bound, attempt bound, confirmation cycle
- `internal/review` — review profile: canonical ReviewResult validation,
  dispatch outcome classification, fail-closed closure check
- `internal/store` — canonical Markdown artifact: pre-dispatch revision
  snapshot, content-derived fence / base64 raw blocks, owner-only atomic replace
- `internal/relay` — prepared one-shot review flow (`Prepare` → snapshots →
  private dispatch journal → in-memory attempt admission → child start/capture →
  transaction-tagged canonical append), state as sequence-numbered blocks inside
  the canonical document; `store-md v0.3` / `dispatch-journal v0.1`
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

`store-md v0.3` is an exact-version cutover. Existing v0.2 canonicals are not
silently migrated; re-init or a linked follow-up objective is required.

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
go vet ./... && go test ./...       # deterministic suite (fake adapter)
```

Uninstall by deleting the binary and, if desired, `~/.acrelay/`.
