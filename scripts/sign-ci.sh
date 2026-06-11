#!/usr/bin/env bash
#
# GoReleaser sign wrapper (production audit H4). Invoked by the `signs:` block
# in .goreleaser.yaml with the checksum artifact path as $1. Signs it with the
# Ed25519 key in VORTEX_SIGNING_KEY (a CI secret), writing <artifact>.sig.
#
# When VORTEX_SIGNING_KEY is unset (snapshot/local builds) it creates no .sig
# and exits 0 so the build still succeeds — `vortex self-update` then falls
# back to integrity-only verification.
set -euo pipefail

artifact="${1:?artifact path required}"

if [ -z "${VORTEX_SIGNING_KEY:-}" ]; then
  echo "VORTEX_SIGNING_KEY not set; skipping release signing (integrity-only)" >&2
  exit 0
fi

dir="$(cd "$(dirname "$0")" && pwd)"
bash "$dir/sign.sh" sign "$VORTEX_SIGNING_KEY" "$artifact"
