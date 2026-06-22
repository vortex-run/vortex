# VORTEX

> **One binary. Any server. Fully autonomous.**

VORTEX is a self-hosted autonomous infrastructure platform: a single static Go
binary that owns your edge (reverse proxy, TLS, QUIC), your security (mTLS,
RBAC, OPA policy, tamper-proof audit), your observability (Prometheus, OTel),
and an AI agent runtime that can read, write, and run things on your machine,
build apps, run research, and manage your servers — all controlled from a
terminal dashboard, a browser, or Telegram on your phone.

```
$ curl -fsSL https://vortex.run/install | sh
$ vortex setup        # pick an AI provider, optional Telegram
$ vortex start        # edge + security + agent runtime, one process
```

> _Screenshot / demo gif placeholder — `docs/` has full walkthroughs._

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
| 🔒 **Zero Trust** | mTLS identity mesh, RBAC, OPA policy engine, edge rate limiting |
| 📱 **Telegram Control** | Drive the agent and get alerts from your phone |
| 🏗️ **Autonomous Builds** | VORTEX Forge builds apps + Android APKs from a prompt |
| 🔍 **Research Agent** | Web search + summarize into reports (SSRF-hardened) |
| 🖥️ **DevOps Agent** | SSH, Docker, Nginx management over your fleet |
| 📊 **Data Pipelines** | CSV/JSON analysis + chart generation |
| 🏥 **Self-Healing** | Auto-detect failures and recover, with SLO tracking |
| 🤝 **Multi-Agent** | Orchestrate complex tasks across specialised agents |
| 📟 **Terminal UI** | Full dashboard in your terminal (optional vim keybindings) |
| 🌐 **Web Dashboard** | Browser-based management at `/dashboard/` |
| 🔐 **Encrypted Secrets** | XChaCha20-Poly1305 at rest, keyed by a random master key |
| 📋 **Tamper-Proof Audit** | HMAC hash-chained, compliance-report exportable |
| 📦 **Signed Releases** | Ed25519-signed checksums, verified before self-update |

---

## Quick Start (30 seconds)

```bash
curl -fsSL https://vortex.run/install | sh
vortex setup
vortex start
```

Then open the dashboard at <http://localhost:9090/dashboard/> or the terminal
UI with `vortex ui`.

---

## Compared to Claude Code

VORTEX does everything an AI coding agent does **and operates the server it
runs on**:

| | Claude Code | VORTEX |
|---|:---:|:---:|
| AI coding agent | ✅ | ✅ |
| Multiple AI providers | — | ✅ (9 providers) |
| Conversation persistence | ✅ | ✅ (SQLite + full-text search) |
| LSP code intelligence | ✅ | ✅ |
| Reverse proxy / TLS | — | ✅ |
| Secrets manager | — | ✅ (encrypted at rest) |
| mTLS / RBAC / policy | — | ✅ |
| Telegram / phone control | — | ✅ |
| Autonomous app builds | — | ✅ (Forge) |
| Self-healing infra | — | ✅ |
| Multi-agent orchestration | — | ✅ |
| Audit log (tamper-proof) | — | ✅ |
| Single self-hosted binary | — | ✅ |

---

## Installation

See [docs/install.md](docs/install.md) for the full guide. In short:

**Linux / macOS:**
```bash
curl -fsSL https://vortex.run/install | sh
```

**Windows:** download `vortex_windows_amd64.zip` from the
[Releases](https://github.com/vortex-run/vortex/releases) page, extract
`vortex.exe`, and run `vortex service install`.

**Build from source? Add `vortex` to your PATH** so `vortex code` works from
any project directory:

```powershell
# This session only:
$env:PATH += ";S:\vortex\bin"

# Permanent (user PATH) — run once:
.\scripts\install-path.ps1   # or: task install:path

# Or manually via System Properties → Environment Variables → Path
# → Add S:\vortex\bin
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
./scripts/verify-release.sh v0.2.0
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

The security model — keys, mTLS, the audit chain, the agent sandbox — is
explained in [docs/security.md](docs/security.md).

---

## Building from Source

```bash
git clone https://github.com/vortex-run/vortex
cd vortex
task build      # produces ./bin/vortex
task test       # unit tests
task lint       # golangci-lint
```

Go 1.26+ and [Task](https://taskfile.dev) are required. Details in
[docs/development.md](docs/development.md).

---

## Contributing

Issues and PRs are welcome. CI runs build, race-enabled tests, lint, and the
integration suite on every push and PR. Please run `task test` and `task lint`
before opening a PR, and keep the stdlib-first, single-binary ethos. See
[docs/development.md](docs/development.md).

---

## License

Apache 2.0 — Copyright 2026 VORTEX Contributors. See [LICENSE](LICENSE).
