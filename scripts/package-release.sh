#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: scripts/package-release.sh <vX.Y.Z[-prerelease]> <output-directory>" >&2
  exit 2
}

[[ $# -eq 2 ]] || usage

version=$1
output_dir=$2
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "error: version must be a v-prefixed semantic version" >&2
  exit 2
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
release_number=${version#v}
platform=darwin
architecture=arm64
archive_base="acrelay_${release_number}_${platform}_${architecture}"
archive_name="${archive_base}.tar.gz"
provenance_name="acrelay_${release_number}_provenance.json"
checksums_name="acrelay_${release_number}_checksums.txt"

mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd)
for name in "$archive_name" "$provenance_name" "$checksums_name"; do
  if [[ -e "$output_dir/$name" ]]; then
    echo "error: refusing to overwrite $output_dir/$name" >&2
    exit 1
  fi
done

stage=$(mktemp -d "${TMPDIR:-/tmp}/acrelay-package.XXXXXX")
trap 'rm -rf "$stage"' EXIT
package_root="$stage/$archive_base"
mkdir -p "$package_root"

source_commit=${ACRELAY_SOURCE_COMMIT:-$(git -C "$repo_root" rev-parse HEAD)}
go_version=$(cd "$repo_root" && go env GOVERSION)
builder="local"
if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
  builder="github-actions"
fi

(
  cd "$repo_root"
  CGO_ENABLED=0 GOOS="$platform" GOARCH="$architecture" \
    go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.releaseVersion=$version -X main.releaseCommit=$source_commit" \
    -o "$package_root/acrelay" ./cmd/acrelay
)
cp "$repo_root/LICENSE" "$package_root/LICENSE"
cp "$repo_root/README.md" "$package_root/README.md"
mkdir -p "$package_root/skills"
cp -R "$repo_root/skills/acrelay" "$package_root/skills/"
tar -C "$stage" -czf "$output_dir/$archive_name" "$archive_base"

cat >"$output_dir/$provenance_name" <<EOF
{
  "schema": "acrelay-release-provenance v0.1",
  "version": "$version",
  "source_commit": "$source_commit",
  "builder": "$builder",
  "go_version": "$go_version",
  "goos": "$platform",
  "goarch": "$architecture",
  "cgo_enabled": false,
  "buildvcs": false,
  "trimpath": true,
  "github_run_id": "${GITHUB_RUN_ID:-}",
  "github_run_attempt": "${GITHUB_RUN_ATTEMPT:-}"
}
EOF

checksum_file="$output_dir/$checksums_name"
(
  cd "$output_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$archive_name" "$provenance_name"
  else
    shasum -a 256 "$archive_name" "$provenance_name"
  fi
) >"$checksum_file"

printf 'created %s\ncreated %s\ncreated %s\n' \
  "$output_dir/$archive_name" \
  "$output_dir/$provenance_name" \
  "$checksum_file"
