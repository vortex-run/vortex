# Security Model

VORTEX is designed to run an internet-facing edge and an autonomous agent on
the same box. This document explains the controls that make that safe.

## Key management

All at-rest encryption keys — the secret store, the TLS cert store, the mTLS
cluster CA, and the audit-log HMAC — are derived from a single random **master
key** via HKDF-SHA256 with a per-purpose label. The master key comes from:

1. `VORTEX_MASTER_KEY` (hex/base64, 32 bytes), or
2. a `0600` key file at `<config>/vortex/master.key`, generated with
   `crypto/rand` on first run.

The cluster name (which appears in `/health` and the dashboard) is **never**
used to derive a key. On upgrade, a one-time migration re-keys any legacy
cluster-name-keyed secret store and audit log onto the master key.

> **Rotate the master key** by setting `VORTEX_MASTER_KEY` to a new value and
> re-encrypting stores (a migration runs automatically when the old data is
> still decryptable). Back up the key file — losing it makes encrypted secrets
> unrecoverable.

## Secrets

Secrets are encrypted at rest with XChaCha20-Poly1305 (24-byte nonce). Only
set/unset state is ever exposed via the API; values are returned only to the
explicit `vortex secret get --reveal`. Secrets support expiry and rotation
metadata, with startup alerts (and Telegram notifications) for expired or
rotation-due secrets:

```bash
vortex secret set jwt_secret '...' --expires-in 90d --rotate-every 30d
```

## API keys & RBAC

API keys are bcrypt-hashed (with a SHA-256 prehash to fit bcrypt's 72-byte
limit); plaintext is shown once and never stored. The store is written
atomically on every issue/revoke. RBAC roles (`admin`, `operator`, `viewer`,
`readonly`) gate endpoints; admin-only routes require the admin role.

## Audit log

A tamper-proof, append-only log HMAC-chains every security-relevant event: each
entry's hash covers the previous entry's hash, so deleting, reordering, or
modifying any entry breaks the chain. `vortex audit verify` detects tampering;
`vortex audit report` produces compliance reports (markdown/JSON/CSV); the log
auto-archives to gzip at 100 MB and prunes archives after two years.

## Transport security

- **TLS** is hardened: forward-secret ECDHE-only cipher suites (no `TLS_RSA_*`),
  24h session-ticket-key rotation, configurable min version.
- **mTLS** mesh: routes with `mtls: true` require a client cert issued by the
  cluster CA, rotated automatically.
- **Security headers** on every response: HSTS (over TLS),
  `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, a strict CSP, and
  `Server`/`X-Powered-By` stripped.

## Edge & rate limiting

- Per-IP, per-API-key (1000 req/min default; admin unlimited), and global
  (10000 req/min) rate limits.
- **Burst protection**: >100 requests/second from one IP triggers a 5-minute
  auto-ban, logged and alerted.
- IP allow/blocklists with optional Tor-exit blocking.

## Control-plane trust

`/internal/reload` and `/internal/shutdown` (the SIGHUP/SIGTERM equivalents
used by `vortex reload`/`stop`) are allowed from loopback without a key by
default. **Behind a same-host reverse proxy, set `VORTEX_TRUST_LOOPBACK=false`**
so every request presenting a loopback address must still carry a credential.

## Agent sandbox

- Sandbox filesystem tools confine writes to a sandbox dir (lexical + symlink
  escape checks). Local FS/terminal tools are confined to the working dir.
- The **approval gate** is the primary control for machine-touching actions —
  keep it enabled for unattended runs.
- `http_get` and the research fetcher use an **SSRF-safe dialer** that resolves
  once, validates every candidate IP (blocking loopback/RFC1918/link-local/
  metadata), dials the pinned IP, and re-validates every redirect hop —
  defeating DNS rebinding.
- The WASM plugin runtime is sandboxed with no host imports and memory/CPU
  caps.

## Supply chain

- Releases ship `checksums.txt` plus an **Ed25519 signature**; `vortex
  self-update` verifies the signature against a public key pinned in the binary
  before swapping — a compromised release that forges both the binary and its
  checksum still cannot be installed.
- SBOMs (SPDX) are generated per release archive and as a whole-repo document.
- CI runs `govulncheck` and dependency review.

### Release signing

Generate a signing keypair, pin the public key, and store the private key as a
CI secret:

```bash
./scripts/sign.sh keygen
# → pin PUBLIC_KEY in internal/update/signature.go (ReleaseSigningPublicKey)
# → set PRIVATE_KEY as the CI secret VORTEX_SIGNING_KEY
```

When `VORTEX_SIGNING_KEY` is unset, releases are integrity-only (SHA-256) and
self-update falls back gracefully.

## Reporting vulnerabilities

Please report security issues privately to the maintainers rather than opening
a public issue.
