# VORTEX

*An open source project by [Sandeep Kumar Bollavaram](https://github.com/sandeepbollavaram).*

> **One binary. Any server. AI agents that act only with your approval.**

VORTEX is a self-hosted infrastructure platform: a single static Go binary
that owns your edge (reverse proxy, TLS, QUIC), your security (mTLS, API keys,
OPA policy, a tamper-evident audit log), your observability (Prometheus,
OTel), and an AI agent runtime that can read, write, and run things on your
machine, build apps, run research, and manage your servers. Every shell
command the agent proposes waits for your approval (app builds you ask Forge
for are the exception; see the limitations below). Control it from a terminal
dashboard, a browser, or Telegram on your phone.

```
$ curl -fsSL https://vortex.run/install | sh
$ vortex setup        # pick an AI provider, optional Telegram
$ vortex start        # edge + security + agent runtime, one process
```

### Try the local demo (2 minutes)

```powershell
pwsh scripts/demo/demo.ps1
```

Opens a throwaway cluster with live backends, TLS routes, and the dashboard at
http://127.0.0.1:9090/dashboard/ — isolated from your real install. Details:
[scripts/demo/README.md](scripts/demo/README.md).

[![CI](https://github.com/sandeepbollavaram/vortex/actions/workflows/ci.yml/badge.svg)](https://github.com/sandeepbollavaram/vortex/actions/workflows/ci.yml)
[![Security Scan](https://github.com/sandeepbollavaram/vortex/actions/workflows/security.yml/badge.svg)](https://github.com/sandeepbollavaram/vortex/actions/workflows/security.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

---

## What is VORTEX?

VORTEX collapses the stack you normally assemble from a reverse proxy, a
secrets manager, a cert manager, an observability stack, and a pile of glue
scripts into **one binary with no external dependencies**. It runs your
internet-facing edge and an AI agent that operates the box for you — both in
the same process, both configured from one `vortex.cue` file.

It is **self-hosted and single-tenant by design**: you run it on your own VPS,
it stores its own state, and nothing phones home.

---

## Features

| | |
|---|---|
| 🤖 **AI Agent** | Claude, DeepSeek, OpenAI, Gemini, Groq, AWS Bedrock, Azure OpenAI, OpenRouter, Ollama |
| 🌐 **Reverse Proxy** | HTTP/HTTPS/TCP/UDP/QUIC with load balancing and health checks |
| 🔒 **Access Control** | mTLS, API keys with roles, OPA policy engine, edge rate limiting |
| ✋ **Approval Gate** | Agent shell commands wait for your approval; release builds can't turn it off |
| 📱 **Telegram Control** | Drive the agent, approve its commands, and get alerts from your phone |
| 🏗️ **App Builds** | VORTEX Forge builds apps (and Android APKs, with the Flutter SDK installed) from a prompt |
| 🔍 **Research Agent** | Web search + summarize into reports (SSRF-hardened) |
| 🖥️ **DevOps Agent** | SSH, Docker, Nginx management on your servers |
| 📊 **Data Pipelines** | CSV/JSON analysis + chart generation |
| 🏥 **Self-Healing** | Auto-detect failures and recover, with SLO tracking |
| 🤝 **Multi-Agent** | Orchestrate tasks across specialised agents; runs resume after a crash |
| 📟 **Terminal UI** | Full dashboard in your terminal (optional vim keybindings) |
| 🌐 **Web Dashboard** | Browser-based management at `/dashboard/` |
| 🔐 **Encrypted Secrets** | XChaCha20-Poly1305 at rest, keyed by a random master key |
| 📋 **Tamper-Evident Audit** | HMAC hash-chained, verified at startup, compliance-report exportable |
| 📦 **Release Signing** | Ed25519-signed checksums, verified before self-update (from the next release; v0.3.0 and earlier are unsigned) |

---

## Quick Start (30 seconds)

```bash
curl -fsSL https://vortex.run/install | sh
vortex setup
vortex start
```

Then open the dashboard at <http://localhost:9090/dashboard/> or the terminal
UI with `vortex ui`.

`vortex ai probe` checks that your provider can drive model-led `/agent <task>`
runs (tool calling) before you rely on it.

---

## Status and limitations

VORTEX is young (first commit June 2026) and maintained by one person. Know
these before you deploy it:

- **Single node.** Clustering code exists, but state is node-local; there is
  no high-availability setup yet.
- **No container sandbox for agent tools.** Containment is the approval gate,
  a command allow-list, Auto Mode limits inside a trusted workspace, and a
  scrubbed environment for child processes. Don't point the agent at
  untrusted input unattended.
- **API key roles below admin aren't enforced yet.** Only admin-only routes
  check a role; any valid key can use the other routes. Issue keys only to
  people you'd trust with the agent.
- **Forge builds run generated code without a per-command prompt.** When you
  ask Forge for an app, it compiles the generated project (`go build`, or
  `flutter build apk`, which runs the generated Gradle scripts) in a temporary
  directory with secrets removed from the environment, but not in a sandbox.
- **WhatsApp and Slack are webhook adapters**, not full channels; Telegram is
  the primary phone interface.

The self-assessment audit, with every open finding, is in
[VORTEX_PRODUCTION_AUDIT.md](VORTEX_PRODUCTION_AUDIT.md).

---

## Installation

See [docs/install.md](docs/install.md) for the full guide and
[docs/troubleshooting.md](docs/troubleshooting.md) if something doesn't work.
In short:

**Linux / macOS:**
```bash
curl -fsSL https://vortex.run/install | sh
```

**Windows:** download `vortex_windows_amd64.zip` from the
[Releases](https://github.com/sandeepbollavaram/vortex/releases) page, extract
`vortex.exe`, and run `vortex service install`.

**Build from source? Add `vortex` to your PATH** so `vortex code` works from
any project directory:

```powershell
# This session only (from the repository root):
$env:PATH += ";$PWD\bin"

# Permanent (user PATH) — run once:
.\scripts\install-path.ps1   # or: task install:path

# Or manually via System Properties → Environment Variables → Path
# → add the repository's bin folder
```

On Linux / macOS, the equivalent helper appends to your shell rc:

```bash
./scripts/install-path.sh    # adds bin/ to ~/.zshrc or ~/.bashrc
```

Then, from anywhere:

```bash
cd ~/myproject
vortex code        # operates on THIS project (the current directory)
```

**Verify a release** before trusting it:
```bash
vortex verify            # checks this binary against the signed release
./scripts/verify-release.sh <version>   # a signed release (the next one onward)
```

---

## Configuration (`vortex.cue`)

VORTEX is configured by a single [CUE](https://cuelang.org) file. A minimal
working example:

```cue
cluster: { name: "my-cluster" }
tls: { provider: "internal", acme_email: "you@example.com" }
routes: [
  { name: "web", protocol: "https", listen: 443,
    backends: [{ host: "127.0.0.1", port: 8080 }] },
]
secrets: { keys: ["db_password"] }
observability: { log_level: "info" }
```

All options are documented in [docs/configuration.md](docs/configuration.md).
Validate without starting:

```bash
vortex check --config vortex.cue
```

---

## Agent Commands

Talk to the coordinator from the TUI, the web dashboard, Telegram, or the API.
Examples:

- _"Build me a Flutter todo app and send me the APK."_
- _"Research the top 3 Go HTTP routers and summarise the trade-offs."_
- _"SSH into web-1 and restart nginx, then confirm it's healthy."_
- _"Analyse sales.csv and chart revenue by month."_
- _"Set up a TCP route for postgres on :5432 with mTLS."_

Capabilities and examples: [docs/agents.md](docs/agents.md).

---

## Telegram Setup

1. Create a bot with [@BotFather](https://t.me/BotFather) and copy the token.
2. Run `vortex setup` and choose to configure Telegram, or set
   `VORTEX_TELEGRAM_TOKEN` and `VORTEX_TELEGRAM_DEFAULT_CHAT`.
3. Message your bot; it drives the agent and forwards alerts.

Full steps: [docs/telegram.md](docs/telegram.md).

---

## API Reference

The management API listens on `:9090`. Key endpoints:

| Method | Path | Purpose |
|---|---|---|
| GET | `/health` | Liveness + config hash |
| GET | `/ready` | Readiness (aggregates subsystem health) |
| GET | `/metrics` | Prometheus metrics |
| GET | `/api/status` | Extended status |
| POST | `/api/agents/submit` | Send a message to the agent |
| GET | `/api/agents/history` | List conversation sessions |
| POST | `/api/keys` | Issue an API key (admin) |
| GET | `/api/audit` | Audit log entries |

Full reference: [docs/api.md](docs/api.md).

---

## Architecture

```
                ┌──────────────────────────────────────────────┐
   Internet ───▶│ Edge: rate limit · IP block · TLS/mTLS · QUIC │
                └───────────────┬──────────────────────────────┘
                                │
              ┌─────────────────┼─────────────────┐
              ▼                 ▼                 ▼
        ┌──────────┐     ┌────────────┐    ┌──────────────┐
        │  Proxy   │     │  Policy    │    │ Management   │
        │ (HTTP/   │     │  (OPA) +   │    │ API + TUI +  │
        │  TCP/UDP)│     │  RBAC      │    │ Dashboard    │
        └──────────┘     └────────────┘    └──────┬───────┘
                                                  │
                          ┌───────────────────────┼──────────────────┐
                          ▼                        ▼                  ▼
                   ┌─────────────┐         ┌──────────────┐   ┌─────────────┐
                   │ Agent       │         │ Secrets +    │   │ Audit (HMAC │
                   │ runtime +   │         │ keyring      │   │ chain)      │
                   │ tools (LSP, │         │ (master key) │   └─────────────┘
                   │ FS, http)   │         └──────────────┘
                   └─────────────┘
```

The security model — keys, mTLS, the audit chain, the agent's approval gate
and its limits — is explained in [docs/security.md](docs/security.md).

---

## Building from Source

```bash
git clone https://github.com/sandeepbollavaram/vortex
cd vortex
task build      # produces ./bin/vortex
task test       # unit tests
task lint       # golangci-lint
```

Go 1.26+ and [Task](https://taskfile.dev) are required. Details in
[docs/development.md](docs/development.md).

**Full product + codebase handoff** (for humans and agents):
[docs/HANDOFF.md](docs/HANDOFF.md).

---

## Contributing

Issues and PRs are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Security reports go through
[SECURITY.md](SECURITY.md) — never open a public issue for vulnerabilities.

CI runs build, race-enabled tests, lint, dashboard build, and the integration
suite on every push and PR. Please run `task test` (and dashboard lint/build if
you touch `web/dashboard`) before opening a PR.

---

## Maintainer

VORTEX is an open source project by Sandeep Kumar Bollavaram
([@sandeepbollavaram](https://github.com/sandeepbollavaram)). It was first
published under the `vortex-run` GitHub organization; old
`github.com/vortex-run/vortex` links redirect here.

---

## License

Apache License 2.0 — Copyright 2026 VORTEX Contributors. See [LICENSE](LICENSE).
