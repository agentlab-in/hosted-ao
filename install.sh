#!/bin/sh
# install.sh - installs the ao CLI on this Linux machine and provisions it.
#
# Usage (interim, until get.agentlab.in exists):
#   curl -fsSL https://raw.githubusercontent.com/agentlab-in/hosted-ao/develop/install.sh | sh
#
# This script contains no reference to its own fetch URL anywhere below, so
# pointing get.agentlab.in at this same file later (reverse-proxied or copied
# to static hosting) is a pure hosting/DNS change: zero script changes, just
# updating the one command shown in README.md.
#
# Downloads the ao binary for this machine's OS/arch, verifies its sha256
# sidecar, installs it to /usr/local/bin/ao, then execs `ao pair` so it can
# provision (or reuse) this machine and print the pairing string.
#
# The pairing string ao pair prints is a credential. This script never
# redirects, tees, logs, or captures that command's stdout/stderr, and prints
# nothing of its own after it starts. The script's last action is exec, so
# ao pair fully replaces this process.
#
# POSIX sh only, no bashisms: sh is not guaranteed to be bash on a minimal
# image.

set -eu

AO_INSTALL_REPO="${AO_INSTALL_REPO:-agentlab-in/hosted-ao}"
AO_INSTALL_BIN_DIR="${AO_INSTALL_BIN_DIR:-/usr/local/bin}"

# The command ao pair delegates to for provisioning-or-reuse (setup-vm pair
# provisioning under the hood). This is a script-local constant, not
# discovered, per the design decision that install.sh should not have to know
# how ao pair is implemented.
AO_PROVISION_VERB="pair"

err() {
  printf 'install.sh: %s\n' "$1" >&2
  exit 1
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    err "'$1' is required but was not found on PATH"
  fi
}

check_requirements() {
  need_cmd curl
  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    err "'sha256sum' (or 'shasum' as a fallback) is required but neither was found on PATH"
  fi
}

# detect_os prints "linux" or fails with a clear "not supported yet" message.
# AO_INSTALL_OS_OVERRIDE is a test seam; real callers get uname -s.
detect_os() {
  os="${AO_INSTALL_OS_OVERRIDE:-$(uname -s)}"
  case "$os" in
    Linux) echo linux ;;
    *) err "'$os' is not supported yet; this installer only supports Linux" ;;
  esac
}

# detect_arch maps uname -m to the arch suffix published in release assets.
# AO_INSTALL_ARCH_OVERRIDE is a test seam; real callers get uname -m.
detect_arch() {
  arch="${AO_INSTALL_ARCH_OVERRIDE:-$(uname -m)}"
  case "$arch" in
    x86_64) echo x64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) err "unsupported architecture '$arch'; only 64-bit x86 (x86_64) and ARM (aarch64/arm64) Linux builds are published" ;;
  esac
}

asset_name() {
  os="$1"
  arch="$2"
  echo "ao-${os}-${arch}"
}

download_url() {
  echo "https://github.com/${AO_INSTALL_REPO}/releases/latest/download/$1"
}

download_file() {
  url="$1"
  dest="$2"
  # curl's own -S diagnostic is discarded: err() below is the single line of
  # output this failure surfaces, so a 404 (or any other curl failure) reads
  # as one clear message instead of curl's line plus ours.
  curl -fsL -o "$dest" "$url" 2>/dev/null || err "download failed: $url"
}

# verify_sha256 checks file against its sidecar checksum file, which must
# contain "<hash>  <filename>" naming file's basename (the format the release
# workflow publishes). Returns non-zero on mismatch (or on a missing checksum
# tool) instead of exiting, so callers decide how to report it and tests can
# call it directly. Always called before chmod +x is applied to file, so a
# corrupted or tampered download is rejected while still non-executable.
verify_sha256() {
  file="$1"
  sha_file="$2"
  dir=$(dirname "$file")
  sha_base=$(basename "$sha_file")
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$dir" && sha256sum -c "$sha_base") >/dev/null 2>&1
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$dir" && shasum -a 256 -c "$sha_base") >/dev/null 2>&1
  else
    return 1
  fi
}

# copy_binary installs src to dest as an executable, using sudo when this
# process is not already root. install -m 0755 sets the exec bit atomically
# with the copy in one privileged invocation, so a failure partway through
# (sudo ticket timeout, disk error, signal) cannot leave a copied-but-not-
# executable dest behind for a re-run to silently miss.
copy_binary() {
  src="$1"
  dest="$2"
  if [ "$(id -u)" -eq 0 ]; then
    install -m 0755 "$src" "$dest"
  else
    sudo install -m 0755 "$src" "$dest"
  fi
}

main() {
  check_requirements

  os=$(detect_os)
  arch=$(detect_arch)
  asset=$(asset_name "$os" "$arch")

  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT INT TERM

  bin_path="$tmp_dir/$asset"
  sha_path="$tmp_dir/$asset.sha256"

  download_file "$(download_url "$asset")" "$bin_path"
  download_file "$(download_url "$asset.sha256")" "$sha_path"

  verify_sha256 "$bin_path" "$sha_path" \
    || err "checksum verification failed for $asset; the download may be corrupted"
  chmod +x "$bin_path"

  install_bin="$AO_INSTALL_BIN_DIR/ao"
  copy_binary "$bin_path" "$install_bin" \
    || err "could not install ao to $install_bin; if sudo needs a password, re-run this installer from an interactive terminal (not piped through a non-interactive wrapper) so sudo can prompt"

  # The temp dir is fully consumed once the verified binary is installed.
  # Clean it up explicitly here rather than relying on the EXIT trap: exec
  # below replaces this process image, so a trap registered in this shell
  # never runs after it. Disarming the trap after the manual cleanup keeps
  # the two from racing or double-firing.
  rm -rf "$tmp_dir"
  trap - EXIT INT TERM

  if [ "$(id -u)" -eq 0 ]; then
    exec "$install_bin" "$AO_PROVISION_VERB"
  else
    exec sudo "$install_bin" "$AO_PROVISION_VERB"
  fi
}

# AO_INSTALL_SOURCED lets tests source this file to exercise its functions
# without running main (and therefore without downloading or execing anything).
if [ "${AO_INSTALL_SOURCED:-0}" != "1" ]; then
  main "$@"
fi
