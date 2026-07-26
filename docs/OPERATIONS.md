# acRelay Installation And Operations

**English** · [한국어](./OPERATIONS.ko.md)

This guide explains how to install, update, remove, and recover the
`v0.1.0-alpha.3` **Public Validation Preview**. It is **Experimental**, and
its broader validation status is **Validation pending**.

Its only prebuilt binary is for macOS Apple Silicon (`darwin/arm64`). Windows
is the next platform-support target: its core runtime lane is already verified,
and platform-specific Claude Code/Codex review validation comes next. Required
patches will be published only after that evidence is reviewed. Linux core
runtime CI remains in the source test matrix, but this preview provides no
Linux artifact or live-review support. Intel Mac likewise has no prebuilt
binary or verified live-review combination in this release.

## Choose An Install Path

| Option | When to use it | Important limit |
| --- | --- | --- |
| One-command installer | Fastest path to the published binary | The binary archive is checksum-verified, but the installer is not inspected before execution |
| Tagged installer | Install the published binary in your user account | Not Developer ID signed or notarized; the checksum detects changed bytes but does not prove who published them |
| Download, read, then run | Inspect the installer before executing it | Installs the same binary and uses the same checksum |
| Pinned `go install` | Build from source with an existing Go toolchain | Go may download the required toolchain and module data, depending on local settings |

The installer does not install the optional
[acRelay Skill](../skills/acrelay/README.md). This repository is the canonical
source for both the engine and that natural-language front door. The Skill is
also part of the Public Validation Preview: Experimental, validation pending,
and not a general `Supported` claim.

## One-Command Installer

```sh
curl -fsSL https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.3/scripts/install.sh | bash
```

The script is pinned to `v0.1.0-alpha.3`, and the downloaded binary archive is
checked against the release checksum before execution. Piping the script to
`bash` does not let you inspect the installer itself. Use the tagged,
review-first path below when that distinction matters.

## Tagged, Review-First Installer

Download the script from the exact tag, inspect it, and run it:

```sh
curl -fLO https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.3/scripts/install.sh
less install.sh
bash install.sh
```

The script is pinned internally to `v0.1.0-alpha.3`. It does not call a
`latest` endpoint or accept an arbitrary version override. It downloads:

```text
acrelay_0.1.0-alpha.3_darwin_arm64.tar.gz
acrelay_0.1.0-alpha.3_checksums.txt
```

The installer checks that the archive’s SHA-256 value exactly matches the
published checksum entry before it extracts or runs the binary. The archive
contains one top-level directory:

```text
acrelay_0.1.0-alpha.3_darwin_arm64/
├── acrelay
├── LICENSE
└── README.md
```

The default destination is `~/.local/bin/acrelay`. Choose another user-owned
directory with:

```sh
bash install.sh --bin-dir "$HOME/bin"
```

The installer does not use `sudo`. If the destination directory is not on
`PATH`, it reports the exact directory to add.

### Existing installations

- Same version: exits successfully without replacement.
- Different or unobservable version: stops without changing the installation
  and reports the installed and target versions.
- Explicit replacement: rerun with `--replace` after deciding that the
  replacement is intended.
- Existing non-executable path: fails without overwriting it.

`--replace` explicitly permits replacement, including a possible downgrade.
The installer does not try to decide which semantic version is newer.

## Pinned Go Install

```sh
go install github.com/kyungseo/acrelay/cmd/acrelay@v0.1.0-alpha.3
```

The module’s `go` directive is the required toolchain contract. With Go’s
automatic toolchain selection enabled, the `go` command may download the
declared toolchain. Review `go env GOTOOLCHAIN` if that network behavior is
not acceptable in the current environment.

This path verifies module content through the user’s configured Go module
proxy/checksum policy. It does not use the GitHub Release archive.

Building successfully on another platform does not enable its Claude Code or
Codex reviewer path. Platform evidence remains GOOS/GOARCH-bound. On a verified
platform, reviewer CLI versions at or above the documented minimum must still
expose every option required by the restricted adapter command.

## Verify

```sh
acrelay version
acrelay version --short
```

The short output for this release must be:

```text
v0.1.0-alpha.3
```

The release archive also publishes:

```text
acrelay_0.1.0-alpha.3_provenance.json
```

This file records the source commit, build environment, Go version, target
`GOOS/GOARCH`, CGO setting, and GitHub run identity when applicable. Only the
manual GitHub Actions workflow, run against an already-approved exact tag,
produces release files intended for publication. Files created by the local
packaging script are test fixtures, not publication sources.

## macOS Signing And Gatekeeper Boundary

The initial release is not Developer ID signed or notarized. Browser/Finder
downloads may show Gatekeeper warnings or refuse execution. Do not generalize
one successful terminal installation into a warning-free guarantee.

The current evidence is limited to the exact paths observed during release
validation; it is not a general `Supported` claim. If execution is blocked,
stop and report the observed Gatekeeper message and file attributes. Do not
recommend disabling Gatekeeper globally or assume the dialog's exact wording.

## Update

Each installer is release-pinned. To update, download the installer from the
new exact tag, inspect it, and run it with `--replace` after confirming the
source and target versions.

An update replaces only the binary. It does not move or delete `~/.acrelay`,
reviewer session identifiers, private working directories, canonical review
records, recovery journals, or quarantined recovery data.

## Remove The Binary

First locate the installed executable:

```sh
command -v acrelay
```

Then remove that exact binary path. For the default installer destination:

```sh
rm "$HOME/.local/bin/acrelay"
```

Binary removal intentionally leaves private state intact. Do **not** remove
`~/.acrelay` as part of uninstall.

If you also intend to retire a finished review’s acRelay session data, inspect
the cleanup plan while the binary is still installed. You must name the exact
canonical record and session reference:

```sh
acrelay cleanup -canonical /private/path/review.md -ref sref-... -mode list
acrelay cleanup -canonical /private/path/review.md -ref sref-... -mode dry-run
```

`-mode apply` is allowed only after the review has reached an eligible terminal
state and the owner records that no related review will resume the session.
The owner keeps the raw canonical record. acRelay does not delete or claim to
delete session or configuration data owned by the reviewer vendor.

## How The Release Bundle Is Built

For maintainers, the release workflow:

1. Requires the exact approved tag `v0.1.0-alpha.3`.
2. Confirms that the tag resolves to the checked-out commit.
3. Confirms a `darwin/arm64` builder and the exact Go toolchain.
4. Runs deterministic and race tests, vet, build, and module verification.
5. Calls `scripts/package-release.sh`.
6. Exercises the real package → checksum → installer → version chain through
   `scripts/test-release.sh`.
7. Uploads a short-lived Actions artifact bundle.

The workflow does not create or push a tag, change repository visibility or
settings, or publish a GitHub Release. The owner must approve each of those
actions separately.

The project does not claim bit-for-bit reproducible archives. It records
repeatable build steps and where the build came from, but archives created on
different runner images or at different times may have different hashes.

## Bad Release Recovery

Stop distribution first and classify what was published:

| Surface | Recovery action | Boundary |
| --- | --- | --- |
| GitHub Release asset | Remove the affected asset or release, then publish a corrected version and checksum notice | Prior downloads cannot be recalled |
| Go module version | Publish a newer module containing a `retract` directive | Published versions and checksum records may remain available |
| Broken Alpha version | Publish a patch/prerelease superseding it | Do not mutate an existing tag |
| Installer/checksum mismatch | Halt the install path, replace through a new version, and identify the exact affected files | Do not silently overwrite published version assets |

Never force-update or reuse a published tag. Returning a repository to private
cannot recall clones, caches, downloaded assets, module mirrors, or checksum
records.

## Build From Source

For development:

```sh
go build -o acrelay ./cmd/acrelay
go vet ./...
go test ./... -race -count=1
go mod verify
```

The deterministic suite does not use vendor authentication, model/API budget,
or live reviewer dispatch. Live smoke is a separate owner-approved action.
