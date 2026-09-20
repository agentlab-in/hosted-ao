#!/bin/sh
# install.sh - installs the ao and hao CLIs on this Linux machine and
# provisions it in pair mode.
#
# Usage (interim, until get.agentlab.in exists):
#   curl -fsSL https://raw.githubusercontent.com/agentlab-in/hosted-ao/develop/install.sh | sh
#
# This script contains no reference to its own fetch URL anywhere below, so
# pointing get.agentlab.in at this same file later (reverse-proxied or copied
# to static hosting) is a pure hosting/DNS change: zero script changes, just
# updating the one command shown in README.md. get.agentlab.in does not exist
# yet; GitHub releases remain the download source.
#
# Downloads both the ao and hao binaries for this machine's OS/arch, verifies
# each against its sha256 sidecar, installs them to /usr/local/bin, then execs
# `hao init --mode pair` so the hao machine-management CLI provisions (or
# reuses) this machine and prints the pairing string.
#
# ao is installed alongside hao because the hao-managed services (daemon and
# pair gateway) exec the ao binary, and because the legacy `ao pair` one-shot
# path remains a compatibility fallback for boxes already provisioned that
# way. hao owns installation, machine configuration, host services, and pair
# exposure; ao owns orchestration.
#
# Environment variables:
#   AO_INSTALL_REPO          - GitHub "owner/repo" to fetch release assets
#                               from. Default: agentlab-in/hosted-ao.
#   AO_INSTALL_BIN_DIR       - Where to install the ao and hao binaries.
#                               Default: /usr/local/bin.
#   AO_INSTALL_OS_OVERRIDE   - Overrides `uname -s` for OS detection. Honored
#                               in real runs, not just tests: forcing Linux
#                               on a non-Linux box still installs and execs a
#                               Linux ELF binary, which will not run there.
#   AO_INSTALL_ARCH_OVERRIDE - Overrides `uname -m` for arch detection. Same
#                               caveat as above: it feeds the same asset-name
#                               lookup real detection does, with no extra
#                               validation that the override matches reality.
#
# The pairing string hao init --mode pair prints is a credential. This script
# never redirects, tees, logs, or captures that command's stdout/stderr, and
# prints nothing of its own after it starts. The script's last action is
# exec, so hao init --mode pair fully replaces this process.
#
# POSIX sh only, no bashisms: sh is not guaranteed to be bash on a minimal
# image.

set -eu

AO_INSTALL_REPO="${AO_INSTALL_REPO:-agentlab-in/hosted-ao}"
AO_INSTALL_BIN_DIR="${AO_INSTALL_BIN_DIR:-/usr/local/bin}"

# The two CLIs this installer ships. hao is the provisioning entry point the
# final exec runs; ao is installed alongside it as the daemon/gateway binary
# and the legacy one-shot `ao pair` compatibility path. Script-local constant,
# not discovered, so install.sh does not depend on how either CLI is
# implemented.
INSTALL_BINARIES="ao hao"

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
  # Root-or-sudo is required later: copy_binary installs into a system bin
  # dir, and `hao init --mode pair` elevates its own privileged steps (systemd
  # service management) through sudo. Check it here so a sudo-less minimal
  # image fails with one accurate line up front instead of a late
  # "sudo: not found" once the download has already happened.
  if [ "$(id -u)" -ne 0 ]; then
    need_cmd sudo
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

# asset_name prints the release asset name for one binary on one os/arch. The
# release workflow publishes "<binary>-<os>-<arch>" plus a matching
# "<asset>.sha256" sidecar for ao and hao alike.
asset_name() {
  binary="$1"
  os="$2"
  arch="$3"
  echo "${binary}-${os}-${arch}"
}

download_url() {
  echo "https://github.com/${AO_INSTALL_REPO}/releases/latest/download/$1"
}

download_file() {
  url="$1"
  dest="$2"
  # -S makes curl emit its own one-line diagnostic on failure (DNS,
  # connection refused, TLS, timeout, a 404, ...); without it curl stays
  # fully silent even on error and there is no cause to report at all. That
  # diagnostic is redirected to "$dest.curl-err" (dest already lives under
  # main()'s $tmp_dir, so this needs no separate reference to that global
  # and is cleaned up the same way dest is) instead of straight to stderr,
  # then folded into err()'s single line below, so a failure here still
  # produces exactly one line of output but the line still carries curl's
  # reason: for an unattended curl|sh run on a box nobody is watching, that
  # reason is often the only signal there is, and this script's own generic
  # "download failed" should not throw it away.
  err_file="$dest.curl-err"
  curl -f -s -S -L -o "$dest" "$url" 2>"$err_file" || {
    cause=$(tr -s '\n' ' ' <"$err_file" | head -c 200)
    cause="${cause% }"
    if [ -n "$cause" ]; then
      err "download failed: $url ($cause)"
    else
      err "download failed: $url"
    fi
  }
}

# verify_sha256 checks file against its sidecar checksum file, which must
# contain "<hash>  <filename>" naming file's basename (the format the release
# workflow publishes). Returns non-zero on mismatch (or on a missing checksum
# tool) instead of exiting, so callers decide how to report it and tests can
# call it directly. Always called before chmod +x is applied to file, so a
# corrupted download is rejected while still non-executable. This catches
# truncation and corruption in transit; it is not a substitution defense
# (the sidecar travels over the same TLS connection, from the same repo, as
# the binary it checks), that defense is HTTPS to github.com itself.
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

  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT INT TERM

  # Download, verify, and install each CLI in turn. Each binary has its own
  # release asset and its own sha256 sidecar, named by asset_name.
  # shellcheck disable=SC2086 # intentional word splitting of the static list
  for binary in $INSTALL_BINARIES; do
    asset=$(asset_name "$binary" "$os" "$arch")
    bin_path="$tmp_dir/$asset"
    sha_path="$tmp_dir/$asset.sha256"

    download_file "$(download_url "$asset")" "$bin_path"
    download_file "$(download_url "$asset.sha256")" "$sha_path"

    verify_sha256 "$bin_path" "$sha_path" \
      || err "checksum verification failed for $asset; the download may be corrupted"
    chmod +x "$bin_path"

    install_bin="$AO_INSTALL_BIN_DIR/$binary"
    copy_binary "$bin_path" "$install_bin" \
      || err "could not install $binary to $install_bin; if sudo needs a password, re-run this installer from an interactive terminal (not piped through a non-interactive wrapper) so sudo can prompt"
  done

  # The temp dir is fully consumed once the verified binaries are installed.
  # Clean it up explicitly here rather than relying on the EXIT trap: exec
  # below replaces this process image, so a trap registered in this shell
  # never runs after it. Disarming the trap after the manual cleanup keeps
  # the two from racing or double-firing.
  rm -rf "$tmp_dir"
  trap - EXIT INT TERM

  # HAO-first provisioning. Unlike the legacy `ao pair` this installer used
  # to exec (which must run as root), `hao init --mode pair` runs as the
  # invoking user and elevates only its narrow privileged steps (systemd
  # service management) through sudo internally, so it is not wrapped in
  # sudo here. Running it as root would defeat its target-user detection for
  # the unprivileged services it supervises.
  exec "$AO_INSTALL_BIN_DIR/hao" init --mode pair
}

# AO_INSTALL_SOURCED lets tests source this file to exercise its functions
# without running main (and therefore without downloading or execing anything).
if [ "${AO_INSTALL_SOURCED:-0}" != "1" ]; then
  main "$@"
fi
