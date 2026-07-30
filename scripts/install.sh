#!/usr/bin/env bash
set -euo pipefail

# This installer is pinned to one release. Download the installer from the tag
# for the version you intend to install; it never resolves "latest".
VERSION="v0.1.0-alpha.5"
REPOSITORY="kyungseo/acrelay"
RELEASE_NUMBER="${VERSION#v}"
ASSET="acrelay_${RELEASE_NUMBER}_darwin_arm64.tar.gz"
CHECKSUMS="acrelay_${RELEASE_NUMBER}_checksums.txt"

bin_dir="${HOME}/.local/bin"
replace=false
skill_host=""

usage() {
  cat <<EOF
Install acRelay $VERSION for macOS Apple Silicon.

Usage: install.sh [--bin-dir PATH] [--skill-host codex|claude|both] [--replace]

  --bin-dir PATH                  Install into PATH (default: \$HOME/.local/bin)
  --skill-host codex|claude|both  Also install the exact-version official Skill
  --replace                       Replace a different engine or Skill explicitly

The installer never uses sudo and never removes ~/.acrelay state.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bin-dir)
      [[ $# -ge 2 ]] || { echo "error: --bin-dir requires a path" >&2; exit 2; }
      bin_dir=$2
      shift 2
      ;;
    --replace)
      replace=true
      shift
      ;;
    --skill-host)
      [[ $# -ge 2 ]] || { echo "error: --skill-host requires codex, claude, or both" >&2; exit 2; }
      skill_host=$2
      case "$skill_host" in
        codex|claude|both) ;;
        *) echo "error: --skill-host requires codex, claude, or both" >&2; exit 2 ;;
      esac
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown argument $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

uname_s=$(uname -s)
uname_m=$(uname -m)
if [[ "${ACRELAY_INSTALL_TESTING:-}" == "1" ]]; then
  uname_s=${ACRELAY_TEST_UNAME_S:-$uname_s}
  uname_m=${ACRELAY_TEST_UNAME_M:-$uname_m}
fi
if [[ "$uname_s" != "Darwin" || "$uname_m" != "arm64" ]]; then
  echo "error: acRelay $VERSION distributes a functional binary only for macOS Apple Silicon (darwin/arm64); detected ${uname_s}/${uname_m}" >&2
  exit 1
fi

for command_name in curl diff grep mktemp rmdir tar shasum; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "error: required command not found: $command_name" >&2
    exit 1
  }
done

target="$bin_dir/acrelay"
engine_current=false
if [[ -e "$target" ]]; then
  if [[ ! -x "$target" ]]; then
    echo "error: $target exists but is not executable; move it aside or choose another --bin-dir" >&2
    exit 1
  fi
  installed=$("$target" version --short 2>/dev/null || true)
  if [[ "$installed" == "$VERSION" ]]; then
    engine_current=true
    if [[ -z "$skill_host" ]]; then
      echo "acRelay $VERSION is already installed at $target"
      exit 0
    fi
  fi
  if [[ "$engine_current" != "true" && "$replace" != "true" ]]; then
    echo "error: $target reports '${installed:-unknown}', target is $VERSION; rerun with --replace only if replacement is intended" >&2
    exit 1
  fi
fi

base_url="https://github.com/$REPOSITORY/releases/download/$VERSION"
if [[ "${ACRELAY_INSTALL_TESTING:-}" == "1" && -n "${ACRELAY_RELEASE_BASE_URL:-}" ]]; then
  base_url=$ACRELAY_RELEASE_BASE_URL
fi

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/acrelay-install.XXXXXX")
install_tmp=""
skill_tmp=""
skill_backup=""
cleanup() {
  rm -rf "$work_dir"
  [[ -z "$install_tmp" ]] || rm -f "$install_tmp"
  [[ -z "$skill_tmp" ]] || rm -rf "$skill_tmp"
  [[ -z "$skill_backup" ]] || rm -rf "$skill_backup"
}
trap cleanup EXIT

curl -fL --retry 2 --retry-delay 1 -o "$work_dir/$ASSET" "$base_url/$ASSET"
curl -fL --retry 2 --retry-delay 1 -o "$work_dir/$CHECKSUMS" "$base_url/$CHECKSUMS"

checksum_line=$(grep -E "^[0-9a-fA-F]{64}  ${ASSET}$" "$work_dir/$CHECKSUMS" || true)
if [[ -z "$checksum_line" ]]; then
  echo "error: checksum manifest has no exact entry for $ASSET" >&2
  exit 1
fi
printf '%s\n' "$checksum_line" >"$work_dir/archive.sha256"
(
  cd "$work_dir"
  shasum -a 256 -c archive.sha256
)

tar -xzf "$work_dir/$ASSET" -C "$work_dir"
extracted="$work_dir/acrelay_${RELEASE_NUMBER}_darwin_arm64/acrelay"
extracted_skill="$work_dir/acrelay_${RELEASE_NUMBER}_darwin_arm64/skills/acrelay"
if [[ ! -f "$extracted" ]]; then
  echo "error: verified archive does not contain the expected acrelay binary" >&2
  exit 1
fi
if [[ -n "$skill_host" && ! -f "$extracted_skill/SKILL.md" ]]; then
  echo "error: verified archive does not contain the expected official Skill" >&2
  exit 1
fi
chmod 0755 "$extracted"
reported=$("$extracted" version --short)
if [[ "$reported" != "$VERSION" ]]; then
  echo "error: verified archive reports $reported, expected $VERSION" >&2
  exit 1
fi

destination_error() {
  echo "error: cannot install acRelay into $bin_dir; choose a writable --bin-dir or fix the directory permissions" >&2
}

skill_parent() {
  case "$1" in
    codex) printf '%s\n' "${HOME}/.agents/skills" ;;
    claude) printf '%s\n' "${HOME}/.claude/skills" ;;
    *) echo "error: internal unsupported Skill host $1" >&2; return 1 ;;
  esac
}

preflight_skill() {
  local host=$1
  local parent
  parent=$(skill_parent "$host")
  local destination="$parent/acrelay"
  if [[ -L "$destination" || ( -e "$destination" && ! -d "$destination" ) ]]; then
    echo "error: $destination exists but is not a plain directory; move it aside first" >&2
    return 1
  fi
  if [[ -d "$destination" ]] &&
    ! diff -qr "$extracted_skill" "$destination" >/dev/null 2>&1 &&
    [[ "$replace" != "true" ]]; then
    echo "error: $destination differs from the $VERSION Skill; rerun with --replace only if replacement is intended" >&2
    return 1
  fi
}

# Protect local Skill edits before changing the engine or either requested host.
case "$skill_host" in
  codex|claude) preflight_skill "$skill_host" ;;
  both)
    preflight_skill codex
    preflight_skill claude
    ;;
esac

if [[ "$engine_current" != "true" ]]; then
  if ! mkdir -p "$bin_dir"; then
    destination_error
    exit 1
  fi
  install_tmp="$bin_dir/.acrelay-install.$$"
  if ! cp "$extracted" "$install_tmp"; then
    destination_error
    exit 1
  fi
  if ! chmod 0755 "$install_tmp"; then
    destination_error
    exit 1
  fi
  if ! mv "$install_tmp" "$target"; then
    destination_error
    exit 1
  fi
  install_tmp=""
  echo "installed acRelay $VERSION at $target"
else
  echo "acRelay $VERSION is already installed at $target"
fi

install_skill() {
  local host=$1
  local parent
  parent=$(skill_parent "$host")
  local destination="$parent/acrelay"
  if ! mkdir -p "$parent"; then
    echo "error: cannot create Skill directory $parent" >&2
    return 1
  fi
  if [[ -L "$destination" || ( -e "$destination" && ! -d "$destination" ) ]]; then
    echo "error: $destination exists but is not a plain directory; move it aside first" >&2
    return 1
  fi
  if [[ -d "$destination" ]]; then
    if diff -qr "$extracted_skill" "$destination" >/dev/null 2>&1; then
      echo "official acRelay Skill $VERSION is already installed for $host"
      return 0
    fi
    if [[ "$replace" != "true" ]]; then
      echo "error: $destination differs from the $VERSION Skill; rerun with --replace only if replacement is intended" >&2
      return 1
    fi
  fi
  skill_tmp=$(mktemp -d "$parent/.acrelay-skill-install.XXXXXX")
  if ! cp -R "$extracted_skill/." "$skill_tmp/"; then
    echo "error: cannot stage the acRelay Skill for $host" >&2
    return 1
  fi
  if [[ -d "$destination" ]]; then
    skill_backup=$(mktemp -d "$parent/.acrelay-skill-backup.XXXXXX")
    rmdir "$skill_backup"
    if ! mv "$destination" "$skill_backup"; then
      echo "error: cannot move the existing acRelay Skill aside for $host" >&2
      return 1
    fi
  fi
  if ! mv "$skill_tmp" "$destination"; then
    if [[ -n "$skill_backup" && -d "$skill_backup" ]]; then
      mv "$skill_backup" "$destination" || true
    fi
    echo "error: cannot install the acRelay Skill for $host" >&2
    return 1
  fi
  skill_tmp=""
  if [[ -n "$skill_backup" ]]; then
    rm -rf "$skill_backup"
    skill_backup=""
  fi
  echo "installed official acRelay Skill $VERSION for $host at $destination"
}

case "$skill_host" in
  codex|claude) install_skill "$skill_host" ;;
  both)
    install_skill codex
    install_skill claude
    ;;
esac

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "note: add $bin_dir to PATH before running acrelay" ;;
esac
echo "state under ~/.acrelay is retained across updates and binary removal"
