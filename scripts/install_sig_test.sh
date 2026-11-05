#!/bin/sh
# Tests install.sh's release-signature check against a throwaway key made
# with scripts/sign.sh. Needs OpenSSL 3. Run: sh scripts/install_sig_test.sh
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

keys="$(bash "$here/sign.sh" keygen)"
priv="$(printf '%s\n' "$keys" | sed -n 's/^PRIVATE_KEY=//p')"
pub="$(printf '%s\n' "$keys" | sed -n 's/^PUBLIC_KEY=//p')"
other="$(bash "$here/sign.sh" keygen | sed -n 's/^PUBLIC_KEY=//p')"

printf 'abc123  vortex_linux_amd64.tar.gz\n' > "$work/checksums.txt"
bash "$here/sign.sh" sign "$priv" "$work/checksums.txt" >/dev/null

fail=0
# run <name> <want: ok|fail> <pubkey> <sums> <sig> [env assignments...]
run() {
  name="$1" want="$2" key="$3" sums="$4" sig="$5"
  shift 5
  # shellcheck disable=SC2016 # the inner script expands its own $1..$4
  if env "$@" VORTEX_INSTALL_LIB=1 sh -c '. "$1"; RELEASE_PUBKEY="$2"; verify_signature "$3" "$4"' _ \
      "$here/install.sh" "$key" "$sums" "$sig" >"$work/out" 2>&1; then
    got=ok
  else
    got=fail
  fi
  if [ "$got" = "$want" ]; then
    echo "PASS $name"
  else
    echo "FAIL $name (want $want, got $got):"
    sed 's/^/    /' "$work/out"
    fail=1
  fi
}

run "valid signature" ok "$pub" "$work/checksums.txt" "$work/checksums.txt.sig"
grep -q 'signature verified' "$work/out" || { echo "FAIL valid signature: no 'verified' message"; fail=1; }

cp "$work/checksums.txt" "$work/tampered.txt"
printf 'evil999  vortex_linux_amd64.tar.gz\n' > "$work/tampered.txt"
run "tampered checksums" fail "$pub" "$work/tampered.txt" "$work/checksums.txt.sig"

run "wrong key" fail "$other" "$work/checksums.txt" "$work/checksums.txt.sig"

run "unsigned release warns" ok "$pub" "$work/checksums.txt" "$work/missing.sig"
grep -q 'NOT' "$work/out" || { echo "FAIL unsigned release: no warning"; fail=1; }
run "unsigned release, required" fail "$pub" "$work/checksums.txt" "$work/missing.sig" VORTEX_REQUIRE_SIGNATURE=1

# No usable openssl on PATH: warn, or refuse when required.
mkdir "$work/bin"
for t in sh dirname tr cat printf; do
  p="$(command -v "$t" || true)"
  [ -n "$p" ] && ln -s "$p" "$work/bin/$t"
done
run "no openssl warns" ok "$pub" "$work/checksums.txt" "$work/checksums.txt.sig" PATH="$work/bin"
run "no openssl, required" fail "$pub" "$work/checksums.txt" "$work/checksums.txt.sig" PATH="$work/bin" VORTEX_REQUIRE_SIGNATURE=1
run "unsigned release, required (new name)" fail "$pub" "$work/checksums.txt" "$work/missing.sig" GUBERN_REQUIRE_SIGNATURE=1

exit "$fail"
