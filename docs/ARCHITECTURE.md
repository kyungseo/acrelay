# acRelay Architecture And Trust Boundaries

**English** · [한국어](./ARCHITECTURE.ko.md)

acRelay is a local command-line tool that sends a defined review subject to a
reviewer CLI and records the result. It does not take over the whole review.
The driver still writes the change and decides how to respond to each finding.
The human owner still decides approval requests and when to close the review.

![acRelay architecture and trust zones](./assets/acrelay-architecture-trust@2x.png)

The editable source is
[`acrelay-architecture-trust.svg`](./assets/acrelay-architecture-trust.svg).

## Spatial Model

```text
files under review ──read/check──> acRelay ──approved data──> reviewer CLI
                                      │
                                      ├── private review record
                                      ├── recovery journal/quarantine
                                      └── session IDs + private work folder

driver ──finding response──> review record <──approval/close── owner
```

The diagram separates three boundaries:

1. **Files under review.** acRelay records the exact file list and checks it
   again before important actions. The reviewer does not receive a general role
   to change the repository.
2. **Private local records.** The canonical review record, recovery journal,
   quarantine data, session identifiers, and private working directory are
   operational data. They are not public reports.
3. **Data sent to the reviewer.** The selected reviewer service may process the
   file content, resolved paths, and metadata. Owner acknowledgment allows that
   transfer; it does not make the reviewer local or isolated.

## Component Responsibilities

| Component | What it does | What it does not decide |
| --- | --- | --- |
| Kernel | Tracks review and governance state, including round and attempt limits | Git hosting, merge, or owner judgment |
| Subject | Normalizes the selected paths and records the file list and revision | A filesystem-wide atomic snapshot |
| Review | Stores examined excerpts, findings, driver responses, and approval records | Proof of understanding or correctness |
| Relay | Prepares, sends, captures, and reconciles a reviewer run | Automatic retry when the result is uncertain |
| Store | Reads and atomically replaces the private canonical record | Authentication or tamper-proof storage |
| Adapter | Calls a supported reviewer version and resumes its session | Reviewer independence |
| Briefing | Summarizes whether the review appears ready to close | Approval or a state change |
| Cleanup | Removes only the specifically authorized acRelay session artifacts | Deleting the raw canonical or vendor-owned state |

## Review Sequence

The diagram shows where data lives. The review itself proceeds in this order:

1. `init` records the files under review, transfer policy, declared reviewer
   setup, owner, and starting revision.
2. `review` checks that the requested reviewer can run. A failed check does not
   use an attempt.
3. acRelay locks the canonical record, checks the files again, fixes the formal
   round limit if this is the first round, and writes a recovery journal.
4. The reviewer process starts. acRelay captures its structured result and
   appends the round to the canonical record.
5. The driver records a response and reason for every finding.
6. The owner answers any approval request.
7. `briefing` summarizes closeout readiness without changing the review.
8. `close` checks the files and closing conditions again, then records the
   owner’s close action.

If the reviewer starts but acRelay cannot tell how it ended, recovery records
`UNKNOWN`. acRelay never retries that run automatically.

## Topology Is Not Assurance

acRelay records which reviewer ran and what the operator declared about the
driver, execution environment, and whether their contexts were shared or
separate. It may summarize those facts with a label such as
`cross-vendor-external`, but the label never means “independence verified.”

Evidence is also limited. A content-matched excerpt confirms that the quoted
text matches the captured file bytes. It does not show that the reviewer
understood the whole change.

## Platform Boundary And Expansion

The initial downloadable binary is for `darwin/arm64` only. Linux and Windows
CI jobs already test core behavior, but those lanes are not yet live Claude
Code or Codex review evidence. Platform-specific reviewer validation is a
planned support-expansion step, followed by any patches that evidence requires.
Until a combination is verified, acRelay stops before using a review round.
Intel Mac has no distributed binary or verified live-review combination in
this release.

See the [behavioral reference](./REFERENCE.md) for exact platform,
restriction, journal, evidence, and cleanup contracts.
