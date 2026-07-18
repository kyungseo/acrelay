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

## Verify

```
go vet ./... && go test ./...
```
