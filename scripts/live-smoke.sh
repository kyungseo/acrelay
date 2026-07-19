#!/bin/sh

set -eu

usage() {
  printf '%s\n' 'usage: ACRELAY_LIVE_SMOKE=1 ./scripts/live-smoke.sh <claude|codex>' >&2
}

if [ "${ACRELAY_LIVE_SMOKE:-}" != "1" ]; then
  printf '%s\n' 'refusing live smoke: set ACRELAY_LIVE_SMOKE=1 to authorize installed CLI, user auth/config, network, and model/API cost' >&2
  exit 2
fi

vendor=${1:-}
case "$vendor" in
  claude|codex) ;;
  *) usage; exit 2 ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/acrelay-live-smoke.XXXXXX")
cleanup() {
  rm -rf "$smoke_root"
}
trap cleanup EXIT HUP INT TERM

binary="$smoke_root/acrelay"
target="$smoke_root/synthetic-target.txt"
canonical="$smoke_root/canonical.md"
handles="$smoke_root/handles.json"

printf '%s\n' 'Synthetic review target. Verify that this text contains no actionable defect.' > "$target"

(
  cd "$repo_root"
  go build -o "$binary" ./cmd/acrelay
)

"$binary" init \
  -canonical "$canonical" \
  -question 'Does this synthetic target contain a material defect?' \
  -target "$target"

"$binary" review \
  -canonical "$canonical" \
  -reviewer "$vendor" \
  -handles "$handles" \
  -workdir "$smoke_root" \
  -prompt "Review only the synthetic target at $target. If it contains no material defect, approve it. Return the required structured result without reading unrelated files."

printf '%s\n' 'Synthetic review target, revised. Verify that this text contains no actionable defect.' > "$target"

"$binary" advance \
  -canonical "$canonical" \
  -note 'synthetic target revised to exercise reviewer-session resume'

"$binary" review \
  -canonical "$canonical" \
  -reviewer "$vendor" \
  -handles "$handles" \
  -workdir "$smoke_root" \
  -prompt "Re-review only the revised synthetic target at $target in the existing reviewer session. If it contains no material defect, approve it. Return the required structured result without reading unrelated files."

"$binary" status -canonical "$canonical"
printf '%s\n' "live review/resume smoke completed for $vendor; temporary private evidence is removed on exit"
