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
smoke_ok=0
cleanup() {
  if [ "$smoke_ok" != "1" ] && [ "${ACRELAY_LIVE_SMOKE_KEEP_ON_FAILURE:-}" = "1" ]; then
    printf '%s\n' "live smoke failed; private evidence retained for diagnosis at $smoke_root" >&2
    return
  fi
  rm -rf "$smoke_root"
}
trap cleanup EXIT HUP INT TERM

binary="$smoke_root/acrelay"
subject_dir="$smoke_root/subject"
runtime_dir="$smoke_root/runtime"
mkdir -p "$subject_dir" "$runtime_dir"
chmod 700 "$subject_dir" "$runtime_dir"
target="$subject_dir/synthetic-target.txt"
canonical="$runtime_dir/canonical.md"
handles="$runtime_dir/handles.json"

printf '%s\n' 'Synthetic review target. Verify that this text contains no actionable defect.' > "$target"

(
  cd "$repo_root"
  go build -o "$binary" ./cmd/acrelay
)

"$binary" init \
  -canonical "$canonical" \
  -question 'Does this synthetic target contain a material defect?' \
  -target "$target" \
  -approval-actor 'live-smoke-owner' \
  -ack-vendor-egress

TMPDIR="$runtime_dir" "$binary" review \
  -canonical "$canonical" \
  -reviewer "$vendor" \
  -handles "$handles" \
  -prompt "Review only the synthetic target at $target. If it contains no material defect, approve it. Return the required structured result without reading unrelated files."

printf '%s\n' 'Synthetic review target, revised. Verify that this text contains no actionable defect.' > "$target"

"$binary" advance \
  -canonical "$canonical" \
  -note 'synthetic target revised to exercise reviewer-session resume'

TMPDIR="$runtime_dir" "$binary" review \
  -canonical "$canonical" \
  -reviewer "$vendor" \
  -handles "$handles" \
  -prompt "Re-review only the revised synthetic target at $target in the existing reviewer session. If it contains no material defect, approve it. Return the required structured result without reading unrelated files."

status_output=$("$binary" status -canonical "$canonical")
printf '%s\n' "$status_output"
case "$status_output" in
  *'governance: CLOSABLE'*) ;;
  *)
    printf '%s\n' "live review/resume smoke did not reach CLOSABLE for $vendor" >&2
    exit 1
    ;;
esac
smoke_ok=1
printf '%s\n' "live review/resume smoke completed for $vendor; temporary private evidence is removed on exit"
