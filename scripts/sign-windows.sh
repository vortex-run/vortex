#!/usr/bin/env bash
# Authenticode-sign a Windows binary so it ships with a real publisher identity.
#
# Windows shows "Publisher: Unknown" for unsigned executables, and the firewall
# and SmartScreen dialogs say so. That is not cosmetic on a server: an operator
# approving an inbound rule for an unsigned binary has no cryptographic evidence
# of who built it. Signing replaces that with the certificate subject (for this
# project, "vortex-run").
#
# This is deliberately a no-op when the certificate secrets are absent, mirroring
# scripts/sign-ci.sh: snapshot and fork builds must still succeed without access
# to release credentials. A release only becomes signed once a maintainer
# provisions a real code-signing certificate — which cannot be generated here by
# design, since its whole value is that a CA verified the holder's identity.
#
# To enable, set both in the release environment:
#   WINDOWS_CERT_BASE64  base64 of the .pfx/.p12 code-signing certificate
#   WINDOWS_CERT_PASS    its password
#
# A self-signed certificate is NOT a substitute: Windows would still warn, and
# a name it cannot verify is arguably worse than an honest "Unknown".
set -euo pipefail

artifact="${1:?usage: sign-windows.sh <artifact>}"

case "$artifact" in
*.exe) ;;
*)
  exit 0 # not a Windows binary
  ;;
esac

if [[ -z "${WINDOWS_CERT_BASE64:-}" || -z "${WINDOWS_CERT_PASS:-}" ]]; then
  echo "sign-windows: no code-signing certificate configured; shipping $artifact unsigned" >&2
  echo "sign-windows: Windows will show 'Publisher: Unknown' until WINDOWS_CERT_BASE64 is set" >&2
  exit 0
fi

if ! command -v osslsigncode >/dev/null 2>&1; then
  echo "sign-windows: osslsigncode not installed; cannot sign $artifact" >&2
  exit 1
fi

cert_file="$(mktemp)"
signed="$(mktemp)"
# Remove the decoded certificate on every exit path, including failure — it is
# the release identity and must not outlive the signing step on the runner.
trap 'rm -f "$cert_file" "$signed"' EXIT

printf '%s' "$WINDOWS_CERT_BASE64" | base64 -d >"$cert_file"

osslsigncode sign \
  -pkcs12 "$cert_file" \
  -pass "$WINDOWS_CERT_PASS" \
  -n "VORTEX" \
  -i "https://github.com/vortex-run/vortex" \
  -ts "http://timestamp.digicert.com" \
  -in "$artifact" \
  -out "$signed"

# Timestamping is what keeps already-released binaries valid after the
# certificate itself expires; without it every shipped artifact would start
# failing verification on the cert's expiry date.
mv "$signed" "$artifact"
echo "sign-windows: signed $artifact"
