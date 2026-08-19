#!/bin/sh
# install-detect-test.sh
#
# Unit-level assertions for install.sh's detection and verification functions,
# run without ever downloading anything or touching the real machine. Sources
# install.sh with AO_INSTALL_SOURCED=1 so the file's own main() at the bottom
# does not auto-run, then drives detect_os/detect_arch/asset_name via the
# AO_INSTALL_OS_OVERRIDE/AO_INSTALL_ARCH_OVERRIDE test seams, plus a direct
# check that a corrupted download is rejected by verify_sha256 before any
# chmod +x could make it executable.
#
# Usage: sh test/cli/install/install-detect-test.sh

set -u

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/../../.." && pwd)
install_sh="$repo_root/install.sh"

fail_count=0
pass_count=0

pass() {
  pass_count=$((pass_count + 1))
  printf 'PASS: %s\n' "$1"
}

fail() {
  fail_count=$((fail_count + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

assert_eq() {
  desc="$1"
  expected="$2"
  actual="$3"
  if [ "$expected" = "$actual" ]; then
    pass "$desc"
  else
    fail "$desc (expected '$expected', got '$actual')"
  fi
}

assert_contains() {
  desc="$1"
  haystack="$2"
  needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$desc" ;;
    *) fail "$desc (expected output to mention '$needle', got: $haystack)" ;;
  esac
}

if [ ! -f "$install_sh" ]; then
  echo "FAIL: $install_sh does not exist yet" >&2
  exit 1
fi

AO_INSTALL_SOURCED=1
export AO_INSTALL_SOURCED
# shellcheck source=/dev/null
. "$install_sh"

# --- detect_os / detect_arch / asset_name: x86_64 Linux -> ao-linux-x64 ---
# Exported: these are read by detect_os/detect_arch inside the sourced
# install.sh, not directly in this script.
AO_INSTALL_OS_OVERRIDE=Linux
AO_INSTALL_ARCH_OVERRIDE=x86_64
export AO_INSTALL_OS_OVERRIDE AO_INSTALL_ARCH_OVERRIDE
os=$(detect_os)
arch=$(detect_arch)
assert_eq "detect_os(Linux) -> linux" "linux" "$os"
assert_eq "detect_arch(x86_64) -> x64" "x64" "$arch"
assert_eq "asset_name(linux, x64) -> ao-linux-x64" "ao-linux-x64" "$(asset_name "$os" "$arch")"

# --- aarch64 -> ao-linux-arm64 ---
AO_INSTALL_ARCH_OVERRIDE=aarch64
arch=$(detect_arch)
assert_eq "detect_arch(aarch64) -> arm64" "arm64" "$arch"
assert_eq "asset_name(linux, arm64) -> ao-linux-arm64" "ao-linux-arm64" "$(asset_name "$os" "$arch")"

# --- armv7l -> named failure ---
AO_INSTALL_ARCH_OVERRIDE=armv7l
if err_out=$(detect_arch 2>&1); then
  fail "detect_arch(armv7l) should have failed, printed: $err_out"
else
  assert_contains "detect_arch(armv7l) names what was detected" "$err_out" "armv7l"
fi
unset AO_INSTALL_ARCH_OVERRIDE

# --- Darwin -> named "not supported yet" failure ---
AO_INSTALL_OS_OVERRIDE=Darwin
if err_out=$(detect_os 2>&1); then
  fail "detect_os(Darwin) should have failed, printed: $err_out"
else
  assert_contains "detect_os(Darwin) mentions Darwin" "$err_out" "Darwin"
  assert_contains "detect_os(Darwin) says not supported yet" "$err_out" "not supported yet"
fi
unset AO_INSTALL_OS_OVERRIDE

# --- corrupted download: mismatched sha256 rejected before chmod +x ---
fixture_dir=$(mktemp -d)
trap 'rm -rf "$fixture_dir"' EXIT INT TERM

bin_fixture="$fixture_dir/ao-linux-x64"
sha_fixture="$fixture_dir/ao-linux-x64.sha256"
printf 'not a real binary\n' >"$bin_fixture"
chmod 644 "$bin_fixture"
# A well-formed but wrong hash, in the "<hash>  <filename>" format the release
# workflow publishes.
echo "0000000000000000000000000000000000000000000000000000000000000000  ao-linux-x64" >"$sha_fixture"

if verify_sha256 "$bin_fixture" "$sha_fixture" 2>/dev/null; then
  fail "verify_sha256 accepted a mismatched checksum"
else
  pass "verify_sha256 rejects a mismatched checksum"
fi

if [ -x "$bin_fixture" ]; then
  fail "corrupted download became executable before verification rejected it"
else
  pass "corrupted download was never chmod +x'd"
fi

printf '\n%s passed, %s failed\n' "$pass_count" "$fail_count"
if [ "$fail_count" -gt 0 ]; then
  exit 1
fi
exit 0
