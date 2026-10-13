# Release signing key setup (audit H4)

The public key is pinned in `internal/update/signature.go`
(`ReleaseSigningPublicKey`). You must store the **private** half as a GitHub
Actions secret named `VORTEX_SIGNING_KEY` or tagged releases cannot produce
`.sig` files that match the binary.

## If you already have `.secrets/` from this sprint

```powershell
# From a machine that has the private key file (never commit it):
gh secret set VORTEX_SIGNING_KEY --repo vortex-run/vortex --body (Get-Content -Raw .secrets\VORTEX_SIGNING_KEY.txt)
```

## Rotate later

```bash
./scripts/sign.sh keygen
# pin PUBLIC_KEY → ReleaseSigningPublicKey
# gh secret set VORTEX_SIGNING_KEY …
# cut a new release so every binary carries the new public key
```

Until the secret is set, GoReleaser uses `--skip=sign`. Binaries with a pinned
public key will refuse unsigned updates (`ErrNoSignature`).
