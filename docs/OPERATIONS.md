# acRelay Installation And Operations

**English** · [한국어](./OPERATIONS.ko.md)

This guide explains how to install, update, remove, and recover the initial
unsigned `v0.1.0-alpha.1` release. Its only prebuilt binary is for macOS Apple
Silicon (`darwin/arm64`). That is the first release boundary, not the intended
end of platform support: Linux and Windows core runtime lanes are already
verified, and platform-specific Claude Code/Codex review validation is planned
as the next support-expansion step. Required patches will be published only
after that evidence is reviewed. Intel Mac has no prebuilt binary or verified
live-review combination in this release.

The commands below work only after the exact release tag and files have been
published. If they are unavailable, stop. Do not substitute an unpinned branch
or a `latest` download.

## Choose An Install Path

| Option | When to use it | Important limit |
| --- | --- | --- |
| One-command installer | Fastest path to the published binary | The binary archive is checksum-verified, but the installer is not inspected before execution |
| Tagged installer | Install the published binary in your user account | Unsigned; the checksum detects changed bytes but does not prove who published them |
| Download, read, then run | Inspect the installer before executing it | Installs the same binary and uses the same checksum |
| Pinned `go install` | Build from source with an existing Go toolchain | Go may download the required toolchain and module data, depending on local settings |

The installer does not install the optional
[Skillstead acRelay Skill](https://github.com/kyungseo/skillstead/tree/main/skills/acrelay).
That natural-language front door is an unpublished Alpha preview until its
separate validation is complete.

## One-Command Installer

```sh
curl -fsSL https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.1/scripts/install.sh | bash
```

The script is pinned to `v0.1.0-alpha.1`, and the downloaded binary archive is
checked against the release checksum before execution. Piping the script to
`bash` does not let you inspect the installer itself. Use the tagged,
review-first path below when that distinction matters.

## Tagged, Review-First Installer

Download the script from the exact tag, inspect it, and run it:

```sh
curl -fLO https://raw.githubusercontent.com/kyungseo/acrelay/v0.1.0-alpha.1/scripts/install.sh
less install.sh
bash install.sh
```

The script is pinned internally to `v0.1.0-alpha.1`. It does not call a
`latest` endpoint or accept an arbitrary version override. It downloads:

```text
acrelay_0.1.0-alpha.1_darwin_arm64.tar.gz
acrelay_0.1.0-alpha.1_checksums.txt
```

The installer checks that the archive’s SHA-256 value exactly matches the
published checksum entry before it extracts or runs the binary. The archive
contains one top-level directory:

```text
acrelay_0.1.0-alpha.1_darwin_arm64/
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
go install github.com/kyungseo/acrelay/cmd/acrelay@v0.1.0-alpha.1
```

The module’s `go` directive is the required toolchain contract. With Go’s
automatic toolchain selection enabled, the `go` command may download the
declared toolchain. Review `go env GOTOOLCHAIN` if that network behavior is
not acceptable in the current environment.

This path verifies module content through the user’s configured Go module
proxy/checksum policy. It does not use the GitHub Release archive.

Building successfully on another platform does not make its Claude Code or
Codex reviewer path supported. acRelay still requires recorded
`vendor + version + GOOS + GOARCH` evidence before dispatch.

## Verify

```sh
acrelay version
acrelay version --short
```

The short output for this release must be:

```text
v0.1.0-alpha.1
```

The release archive also publishes:

```text
acrelay_0.1.0-alpha.1_provenance.json
```

This file records the source commit, build environment, Go version, target
`GOOS/GOARCH`, CGO setting, and GitHub run identity when applicable. Only the
manual GitHub Actions workflow, run against an already-approved exact tag,
produces release files intended for publication. Files created by the local
packaging script are test fixtures, not publication sources.

## Unsigned macOS Boundary

The initial release is not signed or notarized. Browser/Finder downloads may
show Gatekeeper warnings or refuse execution. Do not generalize one successful
terminal installation into a warning-free guarantee.

The supported claim is limited to the exact path observed during release
validation. If execution is blocked, stop and report the observed Gatekeeper
message and file attributes; do not recommend disabling Gatekeeper globally.

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

1. Requires the exact approved tag `v0.1.0-alpha.1`.
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
