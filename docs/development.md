# Development

## Prerequisites

- **Go 1.26+**
- **[Task](https://taskfile.dev)** — the build runner (the project uses a
  `Taskfile.yml`, not a Makefile)
- **golangci-lint** — for `task lint`
- A C compiler is optional; it enables the race detector (`task test:race`).

## Build & test

```bash
task build              # → ./bin/vortex (version-stamped via -ldflags)
task test               # unit tests (race detector when a C compiler exists)
task test:race          # force the race detector (needs cgo)
task test:integration   # integration suite (starts a real vortex process)
task lint               # golangci-lint over the module
task vet                # go vet
```

Run a single package's tests directly:

```bash
go test -count=1 ./internal/agents/...
```

## Project layout

```
cmd/vortex/        entrypoint
internal/
  api/             management HTTP server
  agents/          AI coordinator, tools, LSP, SQLite memory
  auth/            API keys (bcrypt), RBAC, OIDC
  audit/           HMAC hash-chained log, compliance reports
  cluster/         raft + gossip membership
  config/          CUE config loading + hot reload
  keyring/         master-key derivation (HKDF) for at-rest keys
  messaging/       AI gateway (9 providers) + Telegram/WhatsApp/Slack
  orchestration/   multi-agent task DAG
  proxy/           HTTP/TCP/UDP/QUIC reverse proxy
  secrets/         encrypted store + rotation
  security/        rate limiting, blocklists, edge
  tls/             cert manager, mTLS mesh
  tui/             terminal dashboard (+ vim editor)
  update/          self-update + release signature verification
pkg/
  atomicfile/      atomic temp-file+rename writes
  safedial/        SSRF-safe HTTP client (DNS pinning)
  lifecycle/       graceful start/stop/reload
  logger/          structured logging
```

## Conventions

- **Stdlib first.** External dependencies are added sparingly and deliberately.
  The binary is a single static artifact (`CGO_ENABLED=0`).
- **Tests alongside code.** Every package has `_test.go` coverage; CI runs the
  race detector.
- **Atomic, durable writes.** Persisted state (keys, secrets, sessions) is
  written via `pkg/atomicfile` (temp + fsync + rename).
- **No secrets in config.** `vortex.cue` selects backends; credentials come
  from the environment.

## CI

`.github/workflows/ci.yml` runs build, race-enabled unit tests, lint
(including the `integration` build tag), and the integration suite on every
push and pull request. `.github/workflows/security.yml` runs `govulncheck` and
dependency review.

## Releasing

Releases are cut by pushing a `v*` tag, which triggers
`.github/workflows/release.yml` (GoReleaser → multi-platform binaries +
`checksums.txt` + SBOMs + Ed25519 signature). See
[security.md](security.md#release-signing) for the signing key setup.

```bash
task tag VERSION=v0.3.0
```
