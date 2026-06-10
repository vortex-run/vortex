#!/usr/bin/env bash
#
# VORTEX release verifier (build plan M19).
#
#   ./scripts/verify-release.sh v0.2.0
#
# Downloads checksums.txt for the given release tag, then downloads every
# binary archive listed in it and verifies each SHA-256 matches. Prints one
# PASS/FAIL line per artifact and exits non-zero if any artifact fails.
set -euo pipefail

REPO="vortex-run/vortex"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
err()  { printf '\033[1;31mERROR:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: verify-release.sh <version-tag>

Example:
  ./scripts/verify-release.sh v0.2.0

Downloads checksums.txt from the GitHub release and verifies the SHA-256 of
every binary archive it lists. Prints PASS or FAIL per artifact.
EOF
}

# download <url> <dest> — quiet fetch with curl or wget.
download() {
  local url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
  else
    err "neither curl nor wget is available to download files"
  fi
}

# sha256 <file> — print the file's SHA-256 hex digest.
sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    err "no sha256 tool found (need sha256sum or shasum)"
  fi
}

main() {
  if [ "$#" -ne 1 ] || [ "$1" = "--help" ] || [ "$1" = "-h" ]; then
    usage
    [ "$#" -eq 1 ] && exit 0
    exit 1
  fi
  local version="$1"
  local base="https://github.com/${REPO}/releases/download/${version}"

  local workdir
  workdir="$(mktemp -d)"
  trap 'rm -rf "$workdir"' EXIT

  log "fetching checksums.txt for ${version}"
  download "${base}/checksums.txt" "${workdir}/checksums.txt" ||
    err "could not download checksums.txt for ${version} (does the release exist?)"

  local failures=0 total=0
  # checksums.txt lines: "<sha256-hex>  <filename>"
  while read -r expected name; do
    [ -n "${name:-}" ] || continue
    # Only verify binary archives; skip auxiliary assets like SBOM documents.
    case "$name" in
      *.tar.gz|*.zip) ;;
      *) continue ;;
    esac
    total=$((total + 1))

    if ! download "${base}/${name}" "${workdir}/${name}"; then
      printf 'FAIL  %s (download failed)\n' "$name"
      failures=$((failures + 1))
      continue
    fi

    local actual
    actual="$(sha256 "${workdir}/${name}")"
    if [ "$actual" = "$expected" ]; then
      printf 'PASS  %s\n' "$name"
    else
      printf 'FAIL  %s (expected %s, got %s)\n' "$name" "$expected" "$actual"
      failures=$((failures + 1))
    fi
    rm -f "${workdir}/${name}"
  done < "${workdir}/checksums.txt"

  [ "$total" -gt 0 ] || err "checksums.txt for ${version} lists no binary archives"

  if [ "$failures" -gt 0 ]; then
    err "${failures}/${total} artifacts failed verification"
  fi
  log "all ${total} artifacts verified for ${version}"
}

main "$@"
