#!/usr/bin/env bash
#
# VORTEX one-line installer (build plan M1.4 / M1.7).
#
#   curl -fsSL https://get.vortex.run | sh
#
# Detects OS and architecture, downloads the matching release from GitHub,
# verifies its SHA-256 checksum against checksums.txt, installs the binary, and
# seeds /etc/vortex/vortex.cue. The script is idempotent — re-running it upgrades
# the binary, skips when already at the target version, and never clobbers an
# existing config.
set -euo pipefail

REPO="vortex-run/vortex"
INSTALL_DIR="${VORTEX_INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${VORTEX_CONFIG_DIR:-/etc/vortex}"
BIN_NAME="vortex"
DOCS_URL="https://docs.vortex.run/install"

# Flags (resolved in parse_args).
FLAG_VERSION=""
NO_SERVICE=0

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
# err prints a human-readable reason plus the docs URL, then exits 1.
err()  {
  printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2
  printf 'See: %s\n' "$DOCS_URL" >&2
  exit 1
}

usage() {
  cat <<EOF
Usage: install.sh [--version vX.Y.Z] [--install-dir DIR] [--no-service] [--help]

Options:
  --version vX.Y.Z   install a specific version (overrides VORTEX_VERSION)
  --install-dir DIR  install the binary to DIR instead of /usr/local/bin
  --no-service       skip the service-installation preview step
  --help             print this help and exit

Environment:
  VORTEX_VERSION     overrides the version to install (flag takes precedence)
  VORTEX_INSTALL_DIR overrides the install directory
  VORTEX_CONFIG_DIR  overrides the config directory (default /etc/vortex)
EOF
}

# --- argument parsing ------------------------------------------------------
parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --version)
        [ "$#" -ge 2 ] || err "--version requires a value (e.g. --version v0.1.0)"
        FLAG_VERSION="$2"
        shift 2
        ;;
      --install-dir)
        [ "$#" -ge 2 ] || err "--install-dir requires a directory argument"
        INSTALL_DIR="$2"
        shift 2
        ;;
      --no-service)
        NO_SERVICE=1
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        usage >&2
        err "unknown argument: $1"
        ;;
    esac
  done
}

# --- detect OS -------------------------------------------------------------
detect_os() {
  local os
  os="$(uname -s 2>/dev/null || echo unknown)"
  case "$os" in
    Linux)   echo "linux" ;;
    Darwin)  echo "darwin" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *)       err "unsupported OS: $os" ;;
  esac
}

# --- detect ARCH -----------------------------------------------------------
detect_arch() {
  local arch
  arch="$(uname -m 2>/dev/null || echo unknown)"
  case "$arch" in
    x86_64|amd64)  echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *)             err "unsupported architecture: $arch" ;;
  esac
}

# --- downloaders -----------------------------------------------------------
# download_quiet fetches silently — used for the version API call.
download_quiet() {
  local url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
  else
    err "neither curl nor wget is available to download files"
  fi
}

# download_file fetches with a visible progress bar — used for the binary.
download_file() {
  local url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    # -f: fail on HTTP error, -S: show errors, -L: follow redirects,
    # --progress-bar: show progress instead of silence.
    curl -fSL --progress-bar "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget --show-progress -qO "$dest" "$url"
  else
    err "neither curl nor wget is available to download files"
  fi
}

# --- resolve latest version ------------------------------------------------
latest_version() {
  local api="https://api.github.com/repos/${REPO}/releases/latest" tmp tag
  tmp="$(mktemp)"
  download_quiet "$api" "$tmp" || err "could not reach the GitHub releases API"
  tag="$(grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' "$tmp" | head -n1 | sed -E 's/.*"([^"]*)"[[:space:]]*$/\1/')"
  rm -f "$tmp"
  [ -n "$tag" ] || err "could not determine the latest version from the GitHub API"
  echo "$tag"
}

# --- verify checksum against checksums.txt ---------------------------------
verify_sha256() {
  # verify_sha256 <file> <checksums.txt> <archive-basename>
  local file="$1" sumfile="$2" base="$3" expected actual line
  line="$(grep " ${base}\$" "$sumfile" || grep "  ${base}\$" "$sumfile" || true)"
  [ -n "$line" ] || err "no checksum entry for ${base} in checksums.txt"
  expected="$(printf '%s' "$line" | awk '{print $1}')"

  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    warn "no sha256 tool found; skipping checksum verification"
    return 0
  fi
  [ "$expected" = "$actual" ] || err "checksum mismatch for ${base}: expected ${expected}, got ${actual}"
  log "checksum verified"
}

main() {
  parse_args "$@"

  local os arch version
  os="$(detect_os)"
  arch="$(detect_arch)"

  # Version resolution order: --version flag, then VORTEX_VERSION, then latest.
  if [ -n "$FLAG_VERSION" ]; then
    version="$FLAG_VERSION"
  elif [ -n "${VORTEX_VERSION:-}" ]; then
    version="$VORTEX_VERSION"
  else
    version="$(latest_version)"
  fi

  if [ "$os" = "windows" ]; then
    cat <<EOF
VORTEX on Windows is best installed manually:
  1. Download vortex_windows_${arch}.zip from
     https://github.com/${REPO}/releases/download/${version}/vortex_windows_${arch}.zip
  2. Extract vortex.exe to "C:\\Program Files\\vortex\\vortex.exe"
  3. Install as a service:  vortex service install
EOF
    exit 0
  fi

  # Idempotency: if the target binary is already at the requested version, stop.
  local target_bin="${INSTALL_DIR}/${BIN_NAME}"
  if [ -x "$target_bin" ]; then
    local installed
    installed="$("$target_bin" version --short 2>/dev/null || echo "")"
    if [ -n "$installed" ] && [ "$installed" = "$version" ]; then
      log "VORTEX ${version} is already installed. Nothing to do."
      exit 0
    fi
  fi

  log "installing VORTEX ${version} for ${os}/${arch}"

  local base url sumurl workdir tarball
  base="vortex_${os}_${arch}.tar.gz"
  url="https://github.com/${REPO}/releases/download/${version}/${base}"
  sumurl="https://github.com/${REPO}/releases/download/${version}/checksums.txt"
  workdir="$(mktemp -d)"
  trap 'rm -rf "$workdir"' EXIT
  tarball="${workdir}/${base}"

  log "downloading ${url}"
  download_file "$url" "$tarball" || err "failed to download ${base}"
  download_quiet "$sumurl" "${workdir}/checksums.txt" || err "failed to download checksums.txt"
  verify_sha256 "$tarball" "${workdir}/checksums.txt" "$base"

  log "extracting"
  tar -xzf "$tarball" -C "$workdir" || err "failed to extract ${base}"
  [ -f "${workdir}/${BIN_NAME}" ] || err "binary ${BIN_NAME} not found in archive"

  # Use sudo for privileged paths when not already root.
  local SUDO=""
  if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
      SUDO="sudo"
    else
      err "must run as root (or have sudo) to install into ${INSTALL_DIR}"
    fi
  fi

  log "installing binary to ${target_bin}"
  $SUDO install -m 0755 "${workdir}/${BIN_NAME}" "$target_bin" || err "failed to install binary to ${target_bin}"

  # Post-install verification: a download that passed SHA-256 can still be the
  # wrong arch or corrupt in a way the checksum was computed over; running it is
  # the definitive check.
  if ! "$target_bin" version >/dev/null 2>&1; then
    $SUDO rm -f "$target_bin"
    err "installed binary failed to run; removed it (corrupt or wrong platform?)"
  fi

  log "ensuring config directory ${CONFIG_DIR}"
  $SUDO mkdir -p "$CONFIG_DIR"
  if [ ! -f "${CONFIG_DIR}/vortex.cue" ]; then
    if [ -f "${workdir}/vortex.cue" ]; then
      $SUDO cp "${workdir}/vortex.cue" "${CONFIG_DIR}/vortex.cue"
      log "seeded example config at ${CONFIG_DIR}/vortex.cue"
    else
      warn "no example vortex.cue in archive; create ${CONFIG_DIR}/vortex.cue manually"
    fi
  else
    log "existing config preserved at ${CONFIG_DIR}/vortex.cue"
  fi

  if [ "$NO_SERVICE" -eq 0 ]; then
    log "previewing service installation (dry-run)"
    "$target_bin" service install \
      --config-path "${CONFIG_DIR}/vortex.cue" --dry-run || true
  fi

  cat <<EOF

VORTEX ${version} installed to ${target_bin}
Config: ${CONFIG_DIR}/vortex.cue — edit this file before starting
Start:  sudo systemctl start vortex   (or appropriate for your init system)
Docs:   https://docs.vortex.run
EOF
}

main "$@"
