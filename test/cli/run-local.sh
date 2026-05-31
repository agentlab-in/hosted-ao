#!/usr/bin/env bash
#
# Convenience wrapper: build `ao` from source and run the CLI smoke test against
# it natively, using an isolated temp state dir and a free port. Touches nothing
# in your real AO installation.
#
#   test/cli/run-local.sh            # PASS/FAIL per assertion
#   test/cli/run-local.sh -v         # also print every command and its full output
#
# To run the same suite the way a brand-new user would install it (clean Linux
# container, binary on PATH), use Docker instead:
#
#   docker build -f test/cli/Dockerfile -t ao-cli-smoke . && docker run --rm --init ao-cli-smoke

set -euo pipefail

# Pass through -v/--verbose (anything else is forwarded to smoke.sh too).
smoke_args=()
for arg in "$@"; do
  case "$arg" in
    -v|--verbose) smoke_args+=("--verbose") ;;
    -h|--help) echo "usage: run-local.sh [-v|--verbose]"; exit 0 ;;
    *) smoke_args+=("$arg") ;;
  esac
done

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
bindir="$(mktemp -d)"
trap 'rm -rf "$bindir"' EXIT

echo "building ao ..."
( cd "$repo_root/backend" && CGO_ENABLED=0 go build -trimpath -o "$bindir/ao" ./cmd/ao )

echo "running smoke test ..."
AO_BIN="$bindir/ao" bash "$repo_root/test/cli/smoke.sh" "${smoke_args[@]+"${smoke_args[@]}"}"
