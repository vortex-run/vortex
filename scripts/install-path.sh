#!/usr/bin/env bash
#
# VORTEX PATH installer (Linux / macOS).
#
# Appends the repo's bin/ directory to your shell rc so `vortex` (and
# `vortex code`) work from any directory. Run once:
#
#   ./scripts/install-path.sh
#
# Re-running is safe: it skips an rc that already has the entry.
set -euo pipefail

VORTEX_BIN="$(cd "$(dirname "$0")/.." && pwd)/bin"
LINE="export PATH=\"\$PATH:$VORTEX_BIN\""

added=0
for rc in "$HOME/.zshrc" "$HOME/.bashrc"; do
  [ -f "$rc" ] || continue
  if grep -qF "$VORTEX_BIN" "$rc"; then
    echo "✓ Already in PATH via $rc"
  else
    printf '\n# Added by VORTEX install-path.sh\n%s\n' "$LINE" >> "$rc"
    echo "✓ Added $VORTEX_BIN to PATH in $rc"
    added=1
  fi
done

# No login shell rc found — fall back to ~/.profile so the change still lands.
if [ ! -f "$HOME/.zshrc" ] && [ ! -f "$HOME/.bashrc" ]; then
  rc="$HOME/.profile"
  if [ -f "$rc" ] && grep -qF "$VORTEX_BIN" "$rc"; then
    echo "✓ Already in PATH via $rc"
  else
    printf '\n# Added by VORTEX install-path.sh\n%s\n' "$LINE" >> "$rc"
    echo "✓ Added $VORTEX_BIN to PATH in $rc"
    added=1
  fi
fi

if [ "$added" -eq 1 ]; then
  echo "  Restart your terminal, or source the rc above, to use 'vortex' anywhere."
fi
