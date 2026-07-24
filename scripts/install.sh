#!/usr/bin/env bash
set -euo pipefail

# This installer is pinned to one release. Download the installer from the tag
# for the version you intend to install; it never resolves "latest".
VERSION="v0.1.0-alpha.1"
REPOSITORY="kyungseo/acrelay"
RELEASE_NUMBER="${VERSION#v}"
ASSET="acrelay_${RELEASE_NUMBER}_darwin_arm64.tar.gz"
CHECKSUMS="acrelay_${RELEASE_NUMBER}_checksums.txt"

bin_dir="${HOME}/.local/bin"
replace=false

usage() {
  cat <<EOF
Install acRelay $VERSION for macOS Apple Silicon.

Usage: install.sh [--bin-dir PATH] [--replace]

  --bin-dir PATH  Install into PATH (default: \$HOME/.local/bin)
  --replace       Replace a different installed version explicitly

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

for command_name in curl grep mktemp tar shasum; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "error: required command not found: $command_name" >&2
    exit 1
  }
done

target="$bin_dir/acrelay"
if [[ -e "$target" ]]; then
  if [[ ! -x "$target" ]]; then
    echo "error: $target exists but is not executable; move it aside or choose another --bin-dir" >&2
    exit 1
  fi
  installed=$("$target" version --short 2>/dev/null || true)
  if [[ "$installed" == "$VERSION" ]]; then
    echo "acRelay $VERSION is already installed at $target"
    exit 0
  fi
  if [[ "$replace" != "true" ]]; then
    echo "error: $target reports '${installed:-unknown}', target is $VERSION; rerun with --replace only if replacement is intended" >&2
    exit 1
  fi
fi

base_url="https://github.com/$REPOSITORY/releases/download/$VERSION"
if [[ "${ACRELAY_INSTALL_TESTING:-}" == "1" && -n "${ACRELAY_RELEASE_BASE_URL:-}" ]]; then
  base_url=$ACRELAY_RELEASE_BASE_URL
fi

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/acrelay-install.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT

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
if [[ ! -f "$extracted" ]]; then
  echo "error: verified archive does not contain the expected acrelay binary" >&2
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

if ! mkdir -p "$bin_dir"; then
  destination_error
  exit 1
fi
install_tmp="$bin_dir/.acrelay-install.$$"
trap 'rm -rf "$work_dir"; rm -f "$install_tmp"' EXIT
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
trap 'rm -rf "$work_dir"' EXIT

echo "installed acRelay $VERSION at $target"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "note: add $bin_dir to PATH before running acrelay" ;;
esac
echo "state under ~/.acrelay is retained across updates and binary removal"
