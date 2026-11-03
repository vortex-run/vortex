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
atomically on every issue/revoke. Keys carry RBAC roles (`admin`, `operator`,
`viewer`, `readonly`), but today only admin-only routes check a role:
`/api/keys`, and Studio (`/studio/`, enabled by `VORTEX_STUDIO_WORKSPACE`),
whose terminal is a shell that doesn't go through the approval gate. Every
other authenticated route accepts any valid key, so a `viewer` or
`readonly` key can still run agents, approve checkpoints, and write files
through them. Do not issue non-admin keys to anyone you would not trust with
those actions. This is tracked as N6 in `VORTEX_PRODUCTION_AUDIT.md`.

**Data-plane vs control-plane:** Agent runtime routes (`/api/agents/*`,
including `GET /api/agents/catalog`), Forge, orchestration (`POST /api/orchestrate`,
`GET /api/orchestration/*`), logs, and OpenAI-compatible `/v1/*` endpoints use
`requireAPIKey` — they need a valid key even from loopback when auth is
configured. Control-plane routes (`/internal/reload`, dashboard data under
`/api/status`, etc.) may allow loopback without a key unless
`VORTEX_TRUST_LOOPBACK=false`.

## Audit log

A tamper-evident, append-only log HMAC-chains every security-relevant event:
each entry's hash covers the previous entry's hash, so deleting, reordering,
or modifying any entry breaks the chain. `vortex audit verify` detects
tampering; `vortex audit report` produces compliance reports
(markdown/JSON/CSV); the log auto-archives to gzip at 100 MB and prunes
archives after two years.

Each entry's hash covers its sequence number, timestamp, actor, action,
resource and (for entries marked `"v":2`) its `detail`: tool outcomes and
errors, Studio SQL queries, issued key owners, config hashes. Entries written
before v2 have no `v` field; their `detail` isn't covered, though everything
else is, and they keep verifying alongside newer entries. The chain detects
changes by anyone without the HMAC key; it doesn't stop someone from deleting
the whole file, so ship the log off the host if you need that.

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

## Messaging webhooks

`/webhook/telegram`, `/webhook/whatsapp` and `/webhook/slack` are public (no
API key, per-IP rate limited) and pass messages to the agent runtime, so each
authenticates the platform itself and fails closed:

- **Telegram:** only chat IDs in `VORTEX_TELEGRAM_ALLOWED_IDS` are served;
  empty allows nobody.
- **WhatsApp:** requests must carry a valid `X-Hub-Signature-256` for
  `VORTEX_WA_APP_SECRET`. The route isn't mounted without the secret. Only
  senders in `VORTEX_WA_ALLOWED_NUMBERS` reach the agent (empty allows
  nobody), because a valid signature proves Meta delivered a message, not
  that its sender may drive your agent.
- **Slack:** requests must carry a valid, fresh (±5 min) signature for
  `VORTEX_SLACK_SIGNING_SECRET`. The route isn't mounted without the secret.
  Anyone in the Slack workspace that installed the app can use the slash
  command.

Command approval prompts go to the operator's configured Telegram chat, not
back to whoever sent the message, so a webhook sender can't approve their own
commands.

## Agent sandbox

- Sandbox filesystem tools confine writes to a sandbox dir (lexical + symlink
  escape checks).
- Local FS/terminal tools: set `agents.local_tools.trusted_root` in `vortex.cue`
  (or `VORTEX_AGENTS_LOCAL_TOOLS_TRUSTED_ROOT`) to confine paths to that
  workspace (lexical + symlink checks). When unset, absolute paths are allowed
  after approval (approval remains the primary control). `auto_mode` may waive
  repeated approval only for routine in-scope file mutations — never for shell,
  and never outside the trusted root. Defaults keep Auto Mode off globally.
  `agents.local_tools` is applied at process startup only; SIGHUP / `vortex reload`
  does not rebuild the local tool registry — restart after changing trust settings.
  Startup validation requires the coordinator working directory to be inside
  `trusted_root` (work dir ⊆ trusted_root) so the default shell cwd is not left
  outside the FS trust boundary. If `run_terminal` is invoked with an empty cwd
  and Root would escape `trusted_root`, the tool falls back to `trusted_root`
  (approved shell is still not a full OS jail).
- The **approval gate** is the primary control for machine-touching actions —
  keep it enabled for unattended runs.
- **Forge** (app builds you request) is the exception: it compiles the
  generated project without a per-command prompt. `go build` doesn't run
  project code; `flutter build apk` runs the project's Gradle scripts, which
  the AI wrote. Builds run in a temporary directory with the same scrubbed
  environment as agent tools plus toolchain settings (Go/Gradle/Android/Java/
  Flutter paths and proxies), with no provider keys, tokens or master key. They
  are not sandboxed.
- DevOps package names, domains and upstreams are validated before they are
  shown for approval or sent to a host (no shell or nginx injection, no path
  traversal).
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

The Ed25519 public key is pinned in `internal/update/signature.go`
(`ReleaseSigningPublicKey`). Releases signed with the matching private key
(GitHub Actions secret `VORTEX_SIGNING_KEY`) verify on `vortex self-update`.

To rotate:

```bash
./scripts/sign.sh keygen
# → replace ReleaseSigningPublicKey with the new PUBLIC_KEY
# → set the new PRIVATE_KEY as the CI secret VORTEX_SIGNING_KEY
# → cut a new release so every binary carries the rotated public key
```

When `VORTEX_SIGNING_KEY` is unset, GoReleaser skips signing (`--skip=sign`)
and ships integrity-only checksums — but a binary with a pinned public key
will refuse unsigned updates (`ErrNoSignature`). Keep the secret configured
for production releases.

## Host keys (DevOps SSH)

When a server entry sets `known_hosts`, VORTEX verifies the remote host key
against that OpenSSH known_hosts file. With no file configured, connections
use trust-on-first-use (TOFU) and log the fingerprint when a logger is set —
prefer an explicit known_hosts file for production fleets.

## Multi-node & containment (honest boundaries)

VORTEX is **production-ready for single-node** deployments. Cluster gossip/raft
exist for discovery experiments, but keys, audit, and quota state remain
node-local — do not treat multi-node as HA yet. Agent tools use allow-lists,
approval gates, and environment scrubbing; they are **not** container- or
namespace-isolated.

## Reporting vulnerabilities

See [SECURITY.md](../SECURITY.md) for private reporting.
