#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/acrelay-release-test.XXXXXX")
trap 'rm -rf "$test_root"' EXIT

version="v0.1.0-alpha.5"
release_number="${version#v}"
archive_root="acrelay_${release_number}_darwin_arm64"
dist="$test_root/dist"
bin_dir="$test_root/bin"
"$repo_root/scripts/package-release.sh" "$version" "$dist"

archive="$dist/${archive_root}.tar.gz"
provenance="$dist/acrelay_${release_number}_provenance.json"
checksums="$dist/acrelay_${release_number}_checksums.txt"
for path in "$archive" "$provenance" "$checksums"; do
  [[ -f "$path" ]] || { echo "missing release output: $path" >&2; exit 1; }
done

tar -tzf "$archive" | grep -Fx "$archive_root/acrelay" >/dev/null
tar -tzf "$archive" | grep -Fx "$archive_root/LICENSE" >/dev/null
tar -tzf "$archive" | grep -Fx "$archive_root/README.md" >/dev/null
tar -tzf "$archive" | grep -Fx "$archive_root/skills/acrelay/SKILL.md" >/dev/null
package_extract="$test_root/package"
mkdir -p "$package_extract"
tar -xzf "$archive" -C "$package_extract"
diff -qr "$repo_root/skills/acrelay" "$package_extract/$archive_root/skills/acrelay" >/dev/null

base_url="file://$dist"
test_home="$test_root/home"
mkdir -p "$test_home"
ACRELAY_INSTALL_TESTING=1 \
ACRELAY_RELEASE_BASE_URL="$base_url" \
HOME="$test_home" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir"
[[ "$("$bin_dir/acrelay" version --short)" == "$version" ]]

# A user may add both Skills after installing the same engine version.
ACRELAY_INSTALL_TESTING=1 \
ACRELAY_RELEASE_BASE_URL="$base_url" \
HOME="$test_home" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir" --skill-host both
[[ -f "$test_home/.agents/skills/acrelay/SKILL.md" ]]
[[ -f "$test_home/.claude/skills/acrelay/SKILL.md" ]]

# Same-version engine+Skill installation is idempotent.
ACRELAY_INSTALL_TESTING=1 \
ACRELAY_RELEASE_BASE_URL="$base_url" \
HOME="$test_home" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir" --skill-host both

# A locally modified Skill is never overwritten without explicit replacement.
printf '\nlocal edit\n' >>"$test_home/.agents/skills/acrelay/SKILL.md"
if ACRELAY_INSTALL_TESTING=1 ACRELAY_RELEASE_BASE_URL="$base_url" HOME="$test_home" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir" --skill-host codex 2>"$test_root/skill-version.err"; then
  echo "installer replaced a modified Skill without --replace" >&2
  exit 1
fi
grep -F "differs from the $version Skill" "$test_root/skill-version.err" >/dev/null
ACRELAY_INSTALL_TESTING=1 ACRELAY_RELEASE_BASE_URL="$base_url" HOME="$test_home" \
  "$repo_root/scripts/install.sh" --bin-dir "$bin_dir" --skill-host codex --replace
! grep -F "local edit" "$test_home/.agents/skills/acrelay/SKILL.md" >/dev/null

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
HOME="$test_home" \
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
