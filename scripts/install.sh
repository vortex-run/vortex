#!/usr/bin/env bash
#
# VORTEX one-line installer (build plan M1.4 / M1.7).
#
#   curl -fsSL https://get.vortex.run | sh
#
# Detects OS and architecture, downloads the matching release from GitHub,
# verifies its SHA-256 checksum, installs the binary to /usr/local/bin, and
# seeds /etc/vortex/vortex.cue. The script is idempotent — re-running it
# upgrades the binary and never clobbers an existing config.
set -euo pipefail

REPO="vortex-run/vortex"
INSTALL_DIR="${VORTEX_INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${VORTEX_CONFIG_DIR:-/etc/vortex}"
BIN_NAME="vortex"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

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

# --- pick a downloader -----------------------------------------------------
download() {
  # download <url> <dest>
  local url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
  else
    err "neither curl nor wget is available"
  fi
}

# --- resolve latest version ------------------------------------------------
latest_version() {
  local api="https://api.github.com/repos/${REPO}/releases/latest" tmp
  tmp="$(mktemp)"
  download "$api" "$tmp"
  # Parse "tag_name": "vX.Y.Z" without jq.
  local tag
  tag="$(grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' "$tmp" | head -n1 | sed -E 's/.*"([^"]*)"[[:space:]]*$/\1/')"
  rm -f "$tmp"
  [ -n "$tag" ] || err "could not determine latest version from GitHub API"
  echo "$tag"
}

# --- verify checksum -------------------------------------------------------
verify_sha256() {
  # verify_sha256 <file> <checksum_file>
  local file="$1" sumfile="$2" expected actual
  expected="$(awk '{print $1}' "$sumfile")"
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    warn "no sha256 tool found; skipping checksum verification"
    return 0
  fi
  [ "$expected" = "$actual" ] || err "checksum mismatch: expected $expected, got $actual"
  log "checksum verified"
}

main() {
  local os arch version
  os="$(detect_os)"
  arch="$(detect_arch)"

  if [ "$os" = "windows" ]; then
    cat <<EOF
VORTEX on Windows is best installed manually:
  1. Download vortex_windows_${arch}.zip from
     https://github.com/${REPO}/releases/latest
  2. Extract vortex.exe to "C:\\Program Files\\vortex\\vortex.exe"
  3. Install as a service:  vortex service install
EOF
    exit 0
  fi

  version="${VORTEX_VERSION:-$(latest_version)}"
  log "installing VORTEX ${version} for ${os}/${arch}"

  local base tarball url sumurl workdir
  base="vortex_${os}_${arch}.tar.gz"
  url="https://github.com/${REPO}/releases/download/${version}/${base}"
  sumurl="${url}.sha256"
  workdir="$(mktemp -d)"
  trap 'rm -rf "$workdir"' EXIT
  tarball="${workdir}/${base}"

  log "downloading ${url}"
  download "$url" "$tarball"
  download "$sumurl" "${tarball}.sha256"
  verify_sha256 "$tarball" "${tarball}.sha256"

  log "extracting"
  tar -xzf "$tarball" -C "$workdir"
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

  log "installing binary to ${INSTALL_DIR}/${BIN_NAME}"
  $SUDO install -m 0755 "${workdir}/${BIN_NAME}" "${INSTALL_DIR}/${BIN_NAME}"

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

  log "previewing service installation (dry-run)"
  "${INSTALL_DIR}/${BIN_NAME}" service install \
    --config-path "${CONFIG_DIR}/vortex.cue" --dry-run || true

  cat <<EOF

VORTEX ${version} installed to ${INSTALL_DIR}/${BIN_NAME}
Config: ${CONFIG_DIR}/vortex.cue — edit this file before starting
Start:  sudo systemctl start vortex   (or appropriate for your init system)
Docs:   https://docs.vortex.run
EOF
}

main "$@"
