# CLAUDE.md — VORTEX project context for Claude Code

> Read `README.md` first for the full product brief, architecture, and the
> complete M1–M20 build plan. This file is the working context for day-to-day
> development sessions: how to build, what the conventions are, and where we are.

## What VORTEX is (one paragraph)

VORTEX is a self-hosted autonomous platform that ships as a **single Go binary**
and runs 24/7 on any VPS. It owns every infrastructure layer natively — edge/TLS,
reverse proxy + tunnels, clustering (Raft + SWIM gossip), zero-trust security,
observability, a WASM plugin system, and a management dashboard — and adds an
**autonomous AI agent system**: a user-facing Coordinator decomposes tasks and
spawns sandboxed specialist agents that communicate over a typed message bus,
each limited to declared Tools, with human-in-the-loop approval for irreversible
actions. It exposes 9 modes (Forge, DevOps, Research, Data, Studio, Guard, Comms,
Commerce, Learn) controlled two-way over Telegram/WhatsApp/Slack.

## Non-negotiable architecture rules

1. **Single binary** — everything compiles into one binary; no required sidecars.
2. **Secrets never in config files** — config declares key *names* only; values
   live in the encrypted store and are injected at runtime.
3. **Config is always valid or rejected** — CUE validation at startup, never at
   first use. Wrong key = startup error with a line number.
4. **No agent talks to users directly** — only the Coordinator touches
   Telegram/WhatsApp/CLI.
5. **Every agent action is a declared Tool** — no tool, no action.
6. **Nothing ships from Forge until QA passes** — the QA gate is non-negotiable.
7. **Every action is audited** — HMAC-chained tamper-proof log.
8. **Human approval before irreversible agent actions.**
9. **Tests before merging** — `task test` must pass. No exceptions.
10. **Go standard library first** — only add a dependency if stdlib genuinely
    cannot do it. Profile before adding anything; keep the binary small.

## Tech stack

- **Go** (primary) — currently Go 1.26, single static binary.
- **Rust** — hot-path packet forwarding only (M2.7), via CGo FFI, only when
  benchmarks demand it.
- **CUE** — config format (NOT YAML/JSON). Typed, validatable, hot-reload on SIGHUP.
- **TypeScript + React + Vite + shadcn/ui** — dashboard, embedded in the binary.
- **Taskfile** (not Make), **GoReleaser**, **GitHub Actions**, **golangci-lint**.

## Repository layout

- `cmd/vortex/` — binary entrypoint (`main.go`).
- `internal/` — private packages: `config` (CUE), `cluster`, `proxy`, `tls`,
  `security`, `observability`, `agents`, `messaging`, `studio`, `api`.
- `pkg/` — reusable libraries: `logger` (slog), `lifecycle` (signals/shutdown),
  `crypto`.
- `plugins/` — WASM runtime, Go/Rust SDKs, built-in plugins.
- `agents/` — per-mode agent definitions (coordinator, forge, devops, …).
- `web/` — React dashboard + studio UI.
- `config/schema.cue` — master config schema.
- `scripts/` — install.sh, tune.sh.

## How to build and test

```sh
task build   # compile bin/vortex with version metadata stamped in
task test    # go test -race -count=1 ./...   (the merge gate)
task lint    # golangci-lint run ./...
task run -- --version   # run the binary with args
task vet     # fast go vet, no external tools
task clean   # remove bin/ and Go caches
```

`task` is installed via `go install github.com/go-task/task/v3/cmd/task@latest`.
`golangci-lint` is v1.64.8 (config in `.golangci.yml`).

## Conventions

- **Errors**: return them, wrap with `%w`, never panic in library code.
- **Logging**: use `pkg/logger` (slog). Thread a correlation ID through
  `context.Context` with `logger.WithCorrelationID`; the handler promotes it to
  a top-level `correlation_id` attribute automatically.
- **Lifecycle**: register cleanup with `Manager.OnShutdown` (runs in reverse
  registration order) and hot-reload work with `OnReload`. SIGTERM/SIGINT =
  shutdown, SIGHUP = reload (Unix); Windows wires Ctrl+C only.
- **Platform files**: use build tags (`//go:build !windows`) like
  `pkg/lifecycle/signals_*.go` rather than runtime OS checks where it keeps code
  clean. The binary must build on Windows for local dev; production targets Linux.
- **Tests**: every package ships with `_test.go`; every PR needs a test.

## Current status

- **M1.1 — Mono-repo scaffold: DONE.** Full directory tree, `go.mod`
  (`github.com/vortex-run/vortex`), `go.work`, `cmd/vortex/main.go` (boots,
  prints version, exits 0), `pkg/logger`, `pkg/lifecycle`, `Taskfile.yml`,
  `.github/workflows/ci.yml`, `.golangci.yml`. `task build`/`test`/`lint` clean.

### Next: M1.2 — CUE config engine
Load and validate `vortex.cue` against `config/schema.cue` at startup (fail fast
with line numbers), expose a typed config struct to the rest of the binary, and
hot-reload on SIGHUP via the lifecycle `OnReload` hook already wired in `main.go`.
Lives in `internal/config/`. After that: M1.3 (Cobra CLI), M1.4 (lifecycle
manager + systemd unit generator), M1.5 (logging polish), M1.6 (CI/GoReleaser),
M1.7 (installer).
