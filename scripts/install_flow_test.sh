#!/bin/sh
# Install-flow tests for scripts/install.sh against a local release directory
# (e.g. a `goreleaser release --snapshot` dist/), with downloads redirected to
# it. Covers a fresh host, a host installed before the VORTEX → Gubern rename,
# and a release that only has pre-rename vortex_* archives.
#
#   sh scripts/install_flow_test.sh dist
set -eu

rel="$(cd "${1:?usage: install_flow_test.sh <release-dir>}" && pwd)"
here="$(cd "$(dirname "$0")" && pwd)"
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
fail=0

# run_install <release-dir> <install-dir> <config-dir>: the real installer
# main, with downloads served from <release-dir>.
run_install() {
  GUBERN_INSTALL_LIB=1 GUBERN_CONFIG_DIR="$3" REL="$1" sh -c '
    . "$0"
    download_file() { cp "$REL/${1##*/}" "$2"; }
    download_quiet() { [ -f "$REL/${1##*/}" ] && cp "$REL/${1##*/}" "$2"; }
    latest_version() { echo v0.0.0-test; }
    main --install-dir "$1" --no-service
  ' "$here/install.sh" "$2" > "$work/out.txt" 2>&1
}

check() { # check <name> <condition...>
  name="$1"; shift
  if "$@" >/dev/null 2>&1; then echo "PASS $name"; else echo "FAIL $name"; sed 's/^/    /' "$work/out.txt"; fail=1; fi
}

# 1. Fresh host: gubern installed, gubern.cue seeded, no vortex link created.
inst="$work/fresh/bin"; cfg="$work/fresh/etc"
run_install "$rel" "$inst" "$cfg" || true
check "fresh: gubern installed and runs" "$inst/gubern" version
check "fresh: gubern.cue seeded" test -f "$cfg/gubern.cue"
check "fresh: no vortex link" test ! -e "$inst/vortex"

# 2. Pre-rename host: existing vortex binary and vortex.cue.
inst="$work/legacy/bin"; cfg="$work/legacy/etc"
mkdir -p "$inst" "$cfg"
printf 'old binary\n' > "$inst/vortex"; chmod +x "$inst/vortex"
printf 'cluster: { name: "kept" }\n' > "$cfg/vortex.cue"
run_install "$rel" "$inst" "$cfg" || true
check "legacy: gubern installed" "$inst/gubern" version
check "legacy: vortex now links to gubern" test -L "$inst/vortex"
check "legacy: vortex runs the new binary" "$inst/vortex" version
check "legacy: vortex.cue kept, no gubern.cue added" sh -c "grep -q kept '$cfg/vortex.cue' && test ! -f '$cfg/gubern.cue'"

# 3. A release from before the rename (vortex_* archives only).
old="$work/oldrel"; mkdir -p "$old"
cp "$rel/checksums.txt" "$old/"
cp "$rel/vortex_${os}_${arch}.tar.gz" "$old/"
inst="$work/old/bin"; cfg="$work/old/etc"
run_install "$old" "$inst" "$cfg" || true
check "old release: falls back to vortex_ archive" grep -q "no gubern archive" "$work/out.txt"
check "old release: installed as gubern" "$inst/gubern" version

exit "$fail"
