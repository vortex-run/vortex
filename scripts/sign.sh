#!/usr/bin/env bash
#
# VORTEX release signing helpers (build plan M20 / production audit H4).
#
# VORTEX signs each release's checksums.txt with an Ed25519 key whose public
# half is pinned into the binary (internal/update/signature.go,
# ReleaseSigningPublicKey). `vortex self-update` verifies the signature before
# installing, so a release that swaps both the binary AND its checksum still
# cannot be installed without the signing key's secret half.
#
# Subcommands:
#   keygen                 generate a new Ed25519 signing keypair
#   sign  <key> <file>     sign <file>, writing <file>.sig (base64 signature)
#   pubkey <key>           print the base64 public key to pin in the binary
#
# Keys are stored as base64. The PRIVATE key belongs in a CI secret
# (VORTEX_SIGNING_KEY); never commit it. This script uses `openssl` (Ed25519
# support requires OpenSSL 1.1.1+).
set -euo pipefail

err() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage:
  sign.sh keygen
      Generate an Ed25519 keypair. Prints PRIVATE_KEY and PUBLIC_KEY (base64).

  sign.sh sign <private-key-b64> <file>
      Sign <file> with the base64 private key, writing <file>.sig.

  sign.sh pubkey <private-key-b64>
      Derive and print the base64 public key (to pin in the binary).
EOF
}

command -v openssl >/dev/null 2>&1 || err "openssl is required"

keygen() {
  local tmp priv pub
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  openssl genpkey -algorithm ed25519 -out "$tmp/priv.pem" 2>/dev/null
  openssl pkey -in "$tmp/priv.pem" -pubout -out "$tmp/pub.pem" 2>/dev/null
  # Extract the raw 32-byte keys (DER tail) and base64-encode them.
  priv="$(openssl pkey -in "$tmp/priv.pem" -outform DER 2>/dev/null | tail -c 32 | base64)"
  pub="$(openssl pkey -in "$tmp/priv.pem" -pubout -outform DER 2>/dev/null | tail -c 32 | base64)"
  printf 'PRIVATE_KEY=%s\n' "$priv"
  printf 'PUBLIC_KEY=%s\n' "$pub"
  printf '\nPin PUBLIC_KEY in internal/update/signature.go (ReleaseSigningPublicKey).\n'
  printf 'Store PRIVATE_KEY as the CI secret VORTEX_SIGNING_KEY (never commit it).\n'
}

# privPEM reconstructs a PEM private key from a base64 raw 32-byte Ed25519 seed.
priv_pem() {
  local b64="$1"
  # PKCS#8 DER prefix for an Ed25519 private key, followed by the 32-byte seed.
  printf '302e020100300506032b657004220420' | xxd -r -p > /tmp/.vortex_ed_prefix
  {
    cat /tmp/.vortex_ed_prefix
    printf '%s' "$b64" | base64 -d
  } | openssl pkey -inform DER -out "$2" 2>/dev/null
  rm -f /tmp/.vortex_ed_prefix
}

sign_file() {
  local key="$1" file="$2" tmp
  [ -f "$file" ] || err "file not found: $file"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  priv_pem "$key" "$tmp/priv.pem"
  openssl pkeyutl -sign -inkey "$tmp/priv.pem" -rawin -in "$file" -out "$tmp/sig.bin" 2>/dev/null \
    || err "signing failed (check the private key)"
  base64 < "$tmp/sig.bin" | tr -d '\n' > "${file}.sig"
  printf '\n' >> "${file}.sig"
  printf 'wrote %s.sig\n' "$file"
}

pubkey() {
  local key="$1" tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  priv_pem "$key" "$tmp/priv.pem"
  openssl pkey -in "$tmp/priv.pem" -pubout -outform DER 2>/dev/null | tail -c 32 | base64
}

case "${1:-}" in
  keygen) keygen ;;
  sign)   [ "$#" -eq 3 ] || { usage; exit 1; }; sign_file "$2" "$3" ;;
  pubkey) [ "$#" -eq 2 ] || { usage; exit 1; }; pubkey "$2" ;;
  *)      usage; exit 1 ;;
esac
