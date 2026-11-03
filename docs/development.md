# Development

**Cold start / AI agents:** read [HANDOFF.md](HANDOFF.md) first (product map,
package catalog, pitfalls, vs OpenClaw/Hermes).

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
task test:race          # force the race detector (needs cgo; 25m timeout)
task test:race:critical # race smoke: orchestration, agents, a2a, api (10m)
task test:integration   # integration suite (starts a real vortex process)
task lint               # golangci-lint over the module
task vet                # go vet
```

Run a single package's tests directly:

```bash
go test -count=1 ./internal/agents/...
```

### Windows Firewall and `*.test.exe` prompts

`go test` builds ephemeral binaries under `%TEMP%\go-build*\*\<pkg>.test.exe`
(e.g. `cmd.test.exe`, `api.test.exe`). Each new cache path looks like a
**different application** to Windows Defender Firewall, so prompts can repeat
even when tests only bind **127.0.0.1**.

| Cause | Evidence | Inbound needed? |
|-------|----------|-----------------|
| `internal/cmd` `runStart` default listen `:9090` (all interfaces) | `api.DefaultAddr`; fixed in tests via `VORTEX_API_LISTEN=127.0.0.1:0` | No for unit tests |
| Package tests that call `net.Listen` / `Server.Start` / `httptest.NewServer` | Many `*_test.go` files use `127.0.0.1:0` | No (loopback) |
| New temp binary path after cache churn | `%TEMP%\go-build…\*.test.exe` | No |
| Production `vortex start` on `:9090` | Intentional management API | Only if you need LAN access |

Do **not** broadly allow every `*.test.exe`. Prefer Deny, or allow one
**stable** binary if you must:

```bat
go test -c -o %TEMP%\vortex-cmd.test.exe ./internal/cmd
```

Override the management listen address for local smoke tests with
`VORTEX_API_LISTEN=127.0.0.1:0` (empty = default `:9090`).

## Project layout

```
cmd/vortex/        entrypoint
internal/
  api/             management HTTP server
  agents/          AI coordinator, tools, LSP, SQLite memory
  auth/            API keys (bcrypt), RBAC, OIDC
  audit/           HMAC hash-chained log, compliance reports
  cluster/         raft + gossip membership (experimental; state is node-local)
  config/          CUE config loading + hot reload
  gateway/         AI provider routing (chat completions / failover)
  keyring/         master-key derivation (HKDF) for at-rest keys
  messaging/       Telegram/WhatsApp/Slack + AI gateway wiring
  orchestration/   multi-agent task DAG
  proxy/           HTTP/TCP/UDP/QUIC reverse proxy
  proxy/gateway/   edge backend gateway (package proxygateway) — not the AI gateway
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
web/dashboard/     React UI (embedded dist/)
docs/openapi.yaml  machine-readable management API sketch
```

Top-level `agents/`, `pipeline/`, `research/`, and `plugins/` are **operator
drop-in scaffolding** (often empty `.gitkeep` trees). Implementation code lives
under `internal/` — do not put new product logic in those empty roots.

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

`.github/workflows/ci.yml` runs build, a bounded race-detector pass on
orchestration/agents/a2a/api, then race-enabled unit tests for the full module
(`task test:race`, 25m cap), lint (including the `integration` build tag), and
the integration suite (`-race`, 900s timeout) on every push and pull request.
All of that runs on **ubuntu-latest** (Linux + C toolchain for cgo). Local
Windows dev boxes often lack cgo — use WSL/Linux CI or install gcc/clang for
`-race`. `.github/workflows/security.yml` runs `govulncheck` and dependency
review.

## Releasing

Releases are cut by pushing a `v*` tag, which triggers
`.github/workflows/release.yml` (GoReleaser → multi-platform binaries +
`checksums.txt` + SBOMs + Ed25519 signature). See
[security.md](security.md#release-signing) for the signing key setup.

Every archive and OS package ships `THIRD_PARTY_LICENSES`, the license and
NOTICE texts of each Go module and production dashboard npm package in the
binary. GoReleaser generates it with `go run ./scripts/licenses`, which fails
the release if a dependency ships no license file. If that happens, check the
package and either add it to `knownLicenseless` in `scripts/licenses/main.go`
with where its license is declared, or replace the dependency. To check
locally (needs `npm ci` in `web/dashboard`):

```bash
go run ./scripts/licenses -o THIRD_PARTY_LICENSES
goreleaser release --snapshot --clean --skip=publish,sign,sbom
```

```bash
task tag VERSION=v0.3.0
```
