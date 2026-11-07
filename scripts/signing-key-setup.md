# Release signing key setup (audit H4)

The public key is pinned in `internal/update/signature.go`
(`ReleaseSigningPublicKey`). You must store the **private** half as a GitHub
Actions secret named `VORTEX_SIGNING_KEY` **in the protected `release`
environment** or tagged releases cannot produce `.sig` files that match the
binary.

The key makes a release trusted by every `gubern self-update`, so it is guarded
twice (audit NV-03):

- the `release` environment requires the maintainer's approval before the
  release job runs, and only `v*` tags may deploy to it;
- the `release-tags` ruleset lets only repo admins create, move or delete
  `v*` tags.

Keep it an *environment* secret, not a repository secret: a repository secret
is readable by any workflow on any branch. After pushing a tag, approve the
pending **Release** deployment in the Actions tab.

## If you already have `.secrets/` from this sprint

```powershell
# From a machine that has the private key file (never commit it):
gh secret set VORTEX_SIGNING_KEY --repo sandeepbollavaram/vortex --env release --body (Get-Content -Raw .secrets\VORTEX_SIGNING_KEY.txt)
# then remove the old repository-level copy:
gh secret delete VORTEX_SIGNING_KEY --repo sandeepbollavaram/vortex
```

## Rotate later

```bash
./scripts/sign.sh keygen
# pin PUBLIC_KEY → ReleaseSigningPublicKey
# gh secret set VORTEX_SIGNING_KEY --env release …
# cut a new release so every binary carries the new public key
```

Until the secret is set, GoReleaser uses `--skip=sign`. Binaries with a pinned
public key will refuse unsigned updates (`ErrNoSignature`).
