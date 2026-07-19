# acrelay (working name)

Portable agent collaboration relay — v1 reference implementation.

`acrelay` is a working identifier, not a public product name. Contract SSoT:
Toolstead `DR-811` (Agent Collab v1 Feasibility Contract And Session Continuity).

## Layout

- `internal/kernel` — collaboration/objective types, execution·governance dual
  state machines, R0..R2 round bound, attempt bound, confirmation cycle
- `internal/review` — review profile: canonical ReviewResult validation,
  dispatch outcome classification, fail-closed closure check
- `internal/store` — canonical Markdown artifact: pre-dispatch revision
  snapshot, content-derived fence / base64 raw blocks, unique-temp atomic append

- `internal/relay` — one-shot review flow wiring (preflight → snapshot →
  attempt commit → dispatch → validate → append), state as sequence-numbered
  blocks inside the canonical document
- `cmd/acrelay` — CLI: `init` / `review` / `confirm` / `disposition` /
  `advance` / `close` / `terminate` / `reconcile` / `status`

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

## Supported Platforms

- **macOS** — runtime-verified in this cycle: process-group termination of the
  reviewer child tree, owner-only permissions, and atomic file replace.
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
