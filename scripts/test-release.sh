#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/acrelay-release-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT

version="v0.1.0-alpha.3"
dist="$test_root/dist"
bin_dir="$test_root/bin"
"$repo_root/scripts/package-release.sh" "$version" "$dist"

archive="$dist/acrelay_0.1.0-alpha.3_darwin_arm64.tar.gz"
provenance="$dist/acrelay_0.1.0-alpha.3_provenance.json"
checksums="$dist/acrelay_0.1.0-alpha.3_checksums.txt"
for path in "$archive" "$provenance" "$checksums"; do
  [[ -f "$path" ]] || { echo "missing release output: $path" >&2; exit 1; }
done

tar -tzf "$archive" | grep -Fx "acrelay_0.1.0-alpha.3_darwin_arm64/acrelay" >/dev/null
tar -tzf "$archive" | grep -Fx "acrelay_0.1.0-alpha.3_darwin_arm64/LICENSE" >/dev/null
tar -tzf "$archive" | grep -Fx "acrelay_0.1.0-alpha.3_darwin_arm64/README.md" >/dev/null

base_url="file://$dist"
ACRELAY_INSTALL_TESTING=1 \
ACRELAY_RELEASE_BASE_URL="$base_url" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir"
[[ "$("$bin_dir/acrelay" version --short)" == "$version" ]]

# Same-version installation is idempotent.
ACRELAY_INSTALL_TESTING=1 \
ACRELAY_RELEASE_BASE_URL="$base_url" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir"

# A different executable is not replaced without explicit owner intent.
cat >"$bin_dir/acrelay" <<'EOF'
#!/usr/bin/env sh
if [ "$1" = "version" ]; then echo "v9.9.9"; exit 0; fi
exit 1
EOF
chmod 0755 "$bin_dir/acrelay"
if ACRELAY_INSTALL_TESTING=1 ACRELAY_RELEASE_BASE_URL="$base_url" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir" 2>"$test_root/version.err"; then
  echo "installer replaced a different version without --replace" >&2
  exit 1
fi
grep -F "reports 'v9.9.9', target is $version" "$test_root/version.err" >/dev/null

ACRELAY_INSTALL_TESTING=1 \
ACRELAY_RELEASE_BASE_URL="$base_url" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir" --replace
[[ "$("$bin_dir/acrelay" version --short)" == "$version" ]]

# A checksum mismatch fails before archive extraction or binary execution.
bad_dist="$test_root/bad-dist"
mkdir -p "$bad_dist"
cp "$archive" "$checksums" "$bad_dist/"
printf 'tampered' >>"$bad_dist/$(basename "$archive")"
if ACRELAY_INSTALL_TESTING=1 ACRELAY_RELEASE_BASE_URL="file://$bad_dist" \
  "$repo_root/scripts/install.sh" --bin-dir "$test_root/bad-bin" >"$test_root/checksum.log" 2>&1; then
  echo "installer accepted a checksum mismatch" >&2
  exit 1
fi
grep -F "FAILED" "$test_root/checksum.log" >/dev/null
[[ ! -e "$test_root/bad-bin/acrelay" ]]

if ACRELAY_INSTALL_TESTING=1 ACRELAY_TEST_UNAME_S=Linux ACRELAY_TEST_UNAME_M=x86_64 \
  "$repo_root/scripts/install.sh" --bin-dir "$test_root/unsupported" 2>"$test_root/platform.err"; then
  echo "installer accepted an unsupported platform" >&2
  exit 1
fi
grep -F "only for macOS Apple Silicon" "$test_root/platform.err" >/dev/null

# An unwritable destination fails closed with a stable next action.
readonly_bin="$test_root/readonly-bin"
mkdir -p "$readonly_bin"
chmod 0555 "$readonly_bin"
if ACRELAY_INSTALL_TESTING=1 ACRELAY_RELEASE_BASE_URL="$base_url" \
  "$repo_root/scripts/install.sh" --bin-dir "$readonly_bin" >"$test_root/permission.log" 2>&1; then
  chmod 0755 "$readonly_bin"
  echo "installer wrote to an unwritable destination" >&2
  exit 1
fi
chmod 0755 "$readonly_bin"
grep -F "choose a writable --bin-dir or fix the directory permissions" "$test_root/permission.log" >/dev/null
[[ ! -e "$readonly_bin/acrelay" ]]

echo "release packaging and installer tests passed"
