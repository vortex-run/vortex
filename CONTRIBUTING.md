# Contributing to VORTEX

Thanks for helping. VORTEX is a single-binary Go platform with an embedded
React dashboard — keep changes focused, tested, and free of secrets.

## Prerequisites

- **Go 1.26.9+** (module pins `toolchain go1.26.9`; `GOTOOLCHAIN=auto` downloads it)
- **Task** — https://taskfile.dev (`task --list`)
- **Node 24+** and npm — only for dashboard work under `web/dashboard/`

## Build & test

```bash
task build          # → bin/vortex
task test           # unit suite (merge gate)
task test:race      # race detector (CI)
task lint           # golangci-lint
go test -tags integration -timeout 900s ./integration/...
```

Dashboard:

```bash
cd web/dashboard
npm ci
npm run lint
npm run build       # refresh committed dist/ (embedded into the binary)
```

## Layout (do not mass-reorganize)

| Path | Role |
|------|------|
| `cmd/vortex` | Binary entrypoint |
| `internal/` | Product logic (API, agents, proxy, config, …) |
| `pkg/` | Small shared libraries |
| `config/` | Embedded CUE schema |
| `web/dashboard/` | React UI (assets embedded via `embed.go`) |
| `docs/` | User and developer guides |
| `integration/` | Process-level tests |

Empty top-level `agents/`, `pipeline/`, `research/`, and `plugins/` dirs are
**operator scaffolding** for runtime drop-ins — implementation lives under
`internal/`. See [docs/development.md](docs/development.md).

## Naming: two “gateways”

- `internal/gateway` — AI provider routing / chat completions
- `internal/proxy/gateway` (`package proxygateway`) — edge reverse-proxy for backends

Prefer the package path in reviews to avoid mixing them.

## Config & secrets

- Product config is CUE (`vortex.cue` + embedded schema). Env overrides use
  `VORTEX_<SECTION>_<FIELD>` for scalars only.
- Never commit API keys, master keys, or `VORTEX_SIGNING_KEY`. Local signing
  material belongs in `.secrets/` (gitignored).

## Pull requests

1. Branch from `main`; keep PRs reviewably small
2. Include tests for behaviour changes
3. Run `task test` (and dashboard lint/build if you touch `web/dashboard`)
4. Fill in the PR template
5. Do not bypass CI with `--no-verify`

Security-sensitive changes: follow [SECURITY.md](SECURITY.md).

## Code of conduct

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).
