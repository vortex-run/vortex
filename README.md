# VORTEX — Complete Project Brief for Claude Code

> Read this entire file before writing a single line of code.
> This is the full product specification, architecture, and build plan
> derived from a complete design session. Every decision here is intentional.

---

## What is VORTEX?

VORTEX is a **self-hosted autonomous platform** that runs 24/7 on any VPS or bare-metal server. It is a single binary that replaces 15+ separate infrastructure tools and adds autonomous AI agent capabilities that no existing tool has.

**Tagline:** *One binary. Any server. Fully autonomous.*

**What it replaces:** Nginx, Caddy, WireGuard, Certbot, Consul, Prometheus, Grafana, HashiCorp Vault, PagerDuty, ngrok, OpenClaw, Hermes, Replit, Gitpod, TablePlus, Sidekiq, Airflow, Intercom, Zendesk, Shopify (partially), Snyk, CrowdStrike (partially).

**What it adds that nothing else has:**
- Autonomous AI sub-agents that talk to each other independently and build real apps
- User sends a Telegram/WhatsApp message → VORTEX builds a complete Android APK and live web app → delivers it back to the chat
- Sub-agents chat to each other internally (no human in the loop) to complete complex tasks
- Full browser-based IDE (VS Code) served from the binary itself
- Self-healing infrastructure that diagnoses and fixes problems while you sleep
- AI gateway that routes between Claude, GPT, Gemini, Mistral, and local Ollama
- Two-way control of everything via Telegram and WhatsApp

---

## Why it cannot be replaced by competitors

| Tool | What it does well | What it lacks vs VORTEX |
|---|---|---|
| OpenClaw | Tunneling, port exposure | No clustering, no AI, no agents, dev-only |
| Hermes | Service mesh for microservices | Cloud/k8s only, no AI, no messaging, 1.5GB RAM idle |
| Nginx | Reverse proxy | No TLS automation, no clustering, no AI, no agents |
| Traefik | Reverse proxy + discovery | No AI, no agents, no messaging integration |
| Devin | AI coding agent | Cloud-only, your code leaves your server, expensive |
| Replit | Browser IDE | Cloud-only, monthly fee, no infra management |
| Coolify/CapRover | Self-hosted PaaS | No AI, no agents, no networking layer, no messaging |

VORTEX owns all layers natively. The AI layer reads the observability layer. The messaging layer triggers from the cluster layer. The agent runtime uses the networking layer to deploy. Nothing is bolted on.

---

## Technology Stack

### Primary language: Go
- Best-in-class for network systems and concurrency
- Compiles to a single static binary — drop on any VPS, no runtime deps
- Standard library covers HTTP/2, TLS, crypto, net
- Used by Docker, Kubernetes, Caddy, Traefik, Consul

### Secondary language: Rust (hot-path only)
- Zero-cost abstractions, no GC pauses for packet forwarding
- Used for the inner packet processing loop when throughput > 1Gbps
- Exposed to Go via CGo FFI
- Only activate when needed — Go handles everything else

### Frontend / Dashboard
- TypeScript + React + Vite
- shadcn/ui component library
- Tanstack Query for data fetching
- Zustand for state management
- Embedded as static files inside the Go binary (no separate server)

### Config format: CUE lang (NOT YAML)
- Machine-validatable, typed, composable
- Schema-first: wrong key name = startup error with line number
- Hot-reload on SIGHUP
- Can be committed to Git safely (no secrets in config)

### Agent / AI components
- Python for ML components inside Forge and Data modes
- Flutter for mobile app delivery capability (builds APKs)
- Local vector store: pgvector (embedded)
- Local LLM: Ollama integration

### Build tooling
- Taskfile for build commands (not Makefile)
- GoReleaser for releases
- GitHub Actions for CI/CD
- golangci-lint for linting

---

## Repository Structure

```
vortex/
├── cmd/
│   └── vortex/
│       └── main.go              # Binary entrypoint
├── internal/
│   ├── config/                  # CUE config engine
│   ├── cluster/                 # Raft + gossip
│   ├── proxy/                   # Reverse proxy + tunnels
│   ├── tls/                     # ACME + cert management
│   ├── security/                # mTLS, OPA, secrets, audit
│   ├── observability/           # OTEL, Prometheus, tracing
│   ├── agents/                  # Agent runtime + coordinator
│   ├── messaging/               # Telegram, WhatsApp, Slack
│   ├── studio/                  # code-server, terminal, DB GUI
│   └── api/                     # Management REST API
├── pkg/
│   ├── logger/                  # slog-based structured logging
│   ├── lifecycle/               # Signal handling, graceful shutdown
│   └── crypto/                  # Encryption utilities
├── plugins/
│   ├── runtime/                 # WASM plugin runtime (wasmtime)
│   ├── sdk-go/                  # Go plugin SDK
│   └── builtin/                 # jwt-auth, rate-limiter, cors, etc.
├── agents/
│   ├── coordinator/             # Master coordinator agent
│   ├── forge/                   # App builder sub-agents
│   ├── devops/                  # Infrastructure management agents
│   ├── research/                # Research + scraping agents
│   ├── data/                    # Data pipeline agents
│   ├── guard/                   # Security patrol agents
│   ├── comms/                   # Email + content agents
│   ├── commerce/                # E-commerce agents
│   └── learn/                   # Codebase indexing agents
├── web/
│   ├── dashboard/               # React management dashboard
│   └── studio/                  # Studio UI components
├── config/
│   └── schema.cue               # Master CUE schema
├── scripts/
│   ├── install.sh               # One-line installer
│   └── tune.sh                  # OS-level network tuning
├── docs/                        # Documentation source
├── .github/
│   └── workflows/               # GitHub Actions CI/CD
├── Taskfile.yml                 # Build commands
├── go.mod
├── go.work                      # Go workspace
└── VORTEX_README.md             # This file
```

---

## Config File Format

The config file is `vortex.cue`. Here is a real example:

```cue
// vortex.cue — validated at startup, hot-reload on SIGHUP

cluster: {
  name:        "prod-cluster-1"
  nodes:       ["10.0.0.1", "10.0.0.2"]
  gossip_port: 7946   // UDP — internal only, never public
  raft_port:   7947   // TCP — internal only, never public
}

tls: {
  acme_email:  "you@example.com"
  provider:    "letsencrypt"  // or "zerossl" or "internal"
  min_version: "TLS1.2"
}

routes: [
  {
    name:         "frontend"
    host:         "myapp.com"
    protocol:     "https"
    backends:     [{host: "127.0.0.1", port: 4000}]
    health_check: {path: "/health", interval: "10s"}
  },
  {
    name:       "api"
    host:       "api.myapp.com"
    protocol:   "https"
    backends:   [{host: "127.0.0.1", port: 3000}]
    rate_limit: {rpm: 600, burst: 50}
    timeout:    "30s"
    plugins:    ["request-logger", "jwt-auth"]
    health_check: {path: "/api/health", interval: "10s"}
  },
  {
    name:     "postgres"
    protocol: "tcp"
    listen:   5432
    backends: [{host: "10.0.0.2", port: 5432}]
    mtls:     true
  },
  {
    name:     "redis"
    protocol: "tcp"
    listen:   6379
    backends: [{host: "10.0.0.2", port: 6379}]
    mtls:     true
  },
]

security: {
  block_tor:    true
  block_clouds: false
  ip_allowlist: null
}

secrets: {
  store:      "local"   // or "vault", "aws-ssm", "gcp-sm"
  keys:       ["db_password", "jwt_secret", "redis_password"]
  inject_env: true
}

observability: {
  metrics_path:   "/metrics"
  tracing:        true
  trace_endpoint: "http://localhost:4318"
  log_level:      "info"
}
```

**Key rules about config:**
- Secrets NEVER appear in config. Only secret names (keys) are declared here.
- Set actual values with: `vortex secret set jwt_secret "$(openssl rand -hex 32)"`
- Config is safe to commit to Git
- Wrong key name = error at startup with exact line number, not at runtime

---

## The 9 Autonomous Modes

Each mode is a named set of agents that work together. Activated by user command via Telegram/CLI/dashboard.

### Mode 1: VORTEX Forge
**Trigger:** User sends "build me X" on Telegram or WhatsApp
**What it does:** Builds a complete working application and delivers APK + live URL back to the chat
**Agent chain:** Coordinator → IntentParser → StackSelector → DependencyAgent → CodegenAgent → BuildAgent → QAAgent → DeployAgent → DeliverAgent
**Outputs:** Android APK (delivered as file to Telegram), live web URL (auto-routed + TLS), API endpoint
**Example:** "build me an app that predicts IPL match scores" → 12 minutes → Flutter APK + live web app delivered

### Mode 2: VORTEX DevOps
**Trigger:** Always running in background (24/7)
**What it does:** Monitors the server, detects problems, heals them, and reports
**Agent chain:** WatchdogAgent → DiagnosticAgent → HealAgent (requires human approval for code changes)
**Outputs:** Telegram alerts with root cause, incident reports, automatic restarts, GitHub issues filed
**Example:** App crashes at 3am → VORTEX restarts it, reads crash logs, sends Telegram: "Restarted. OOM in connection pool, line 847 db.go"

### Mode 3: VORTEX Research
**Trigger:** "research X" command or scheduled
**What it does:** Multi-source deep research, delivers professional PDF report
**Agent chain:** Coordinator → SearchAgent(×N) → ScraperAgent → AnalystAgent → WriterAgent → DeliverAgent
**Outputs:** PDF report, summary, charts — delivered to Telegram/WhatsApp

### Mode 4: VORTEX Data
**Trigger:** Scheduled (cron) or event-based
**What it does:** Pulls data from any source, transforms it, visualizes it, delivers to chat
**Agent chain:** IngestAgent → TransformAgent → VizAgent → DeliverAgent
**Outputs:** Charts as images, tables, summary text — delivered to WhatsApp daily

### Mode 5: VORTEX Studio
**What it does:** Full browser-based development environment on your VPS
**Components:** code-server (VS Code), web terminal, DB GUI, Git panel, container manager
**Access:** browser → `studio.yourdomain.com` → full IDE — works from phone, any device
**Security:** authenticated via VORTEX identity, sessions recorded in audit log

### Mode 6: VORTEX Guard
**Trigger:** Always running (nightly scans + real-time monitoring)
**What it does:** Autonomous security operations
**Agent chain:** ScanAgent → Coordinator → BlockAgent / PatchAgent / ReportAgent
**Outputs:** Blocks brute force in real time, auto-patches low-risk CVEs, morning security report to Telegram

### Mode 7: VORTEX Comms
**Trigger:** Incoming email or scheduled
**What it does:** Monitors inbox, drafts replies, sends for human approval before sending
**Flow:** Incoming email → classify → draft reply → Telegram [Approve/Edit/Escalate] → send on approval
**Key principle:** Never sends without human approval — configurable

### Mode 8: VORTEX Commerce
**Trigger:** "build me a shop" command
**What it does:** Builds full e-commerce store, manages it autonomously
**Agent chain:** ShopBuilder → ProductAgent(vision) → PaymentAgent → OperationsAgent
**Outputs:** Live Next.js store, Razorpay/Stripe payments, daily WhatsApp sales reports

### Mode 9: VORTEX Learn
**Trigger:** Always indexing in background
**What it does:** Indexes your entire codebase and knowledge base, answers questions
**Components:** Vector indexer (pgvector), Q&A agent, auto-docs generator
**Example:** New developer asks "how does auth work?" → VORTEX reads indexed code, past issues, Slack exports → answers with file + line references

---

## Agent Architecture (Critical — Read This)

### The fundamental design

Agents are NOT one big AI doing everything. The architecture is:

```
User (Telegram/WhatsApp/CLI)
        ↓
  Coordinator Agent          ← Only agent that talks to users
        ↓
  [Message Bus]
   ↙    ↓    ↘
Agent1  Agent2  Agent3      ← Specialist agents, talk to each other only
                             ← Each has its own permission sandbox
```

### Coordinator Agent
- The ONLY agent that communicates with the user
- Receives user message → decomposes into task brief → spawns specialist agents
- Monitors progress of all sub-agents
- Handles failures: retry, ask user, or escalate
- Delivers final output back to user
- Always running, listening on messaging webhook

### Specialist Agents
- Spawned by Coordinator for specific tasks
- Task agents: spawn → complete task → die
- Persistent agents (Watchdog, Guard): run forever, supervised restart on crash
- Communicate ONLY via the typed message bus (never shared memory)
- Cannot talk to users directly

### Tool Registry (Security Core)
Every action an agent can take is a registered Tool. Agents declare which tools they need. They get EXACTLY those tools, nothing more.

Examples:
- `FileAgent`: can write to `/var/vortex/sandbox/{agent-id}/` only
- `BuildAgent`: can run `flutter build`, `npm run build`, `go build` only
- `SearchAgent`: can make HTTP GET requests to public URLs only
- `DeployAgent`: can call VORTEX routing API only — cannot touch secrets
- `SecretAgent`: can read from secret store — cannot make network requests

Enforcement: seccomp at kernel level. A hallucinating or compromised agent hits a permission wall before it can do damage.

### Human-in-the-loop
Agents MUST pause and request human approval via Telegram before:
- Any irreversible action (delete, send email, make payment)
- Code changes pushed to production
- Dependency updates
- Config changes affecting live traffic

Telegram message format:
```
[VORTEX] PatchAgent wants to:
Update lodash 4.17.20 → 4.17.21 (CVE-2023-1234 fix)
Test suite: all 847 tests passing on staging

[✅ Approve] [✏️ Edit] [❌ Reject]
```

### Inter-agent message format
```go
type AgentMessage struct {
    ID        string
    FromAgent string
    ToAgent   string
    Type      string   // "task_brief" | "result" | "error" | "status"
    Payload   any
    Timestamp time.Time
}
```

### Agent self-healing loop
This is what makes Forge robust:
```
CodegenAgent writes code
    → BuildAgent runs tests
    → Tests fail
    → BuildAgent reports failure + stderr to Coordinator
    → Coordinator asks Claude to fix the specific failure
    → CodegenAgent patches the code
    → BuildAgent runs tests again
    → Repeat until pass or max_retries exceeded
    → If max_retries: report to user asking for guidance
```

---

## Networking Architecture

### What VORTEX handles at the network layer

```
Internet
    ↓
VORTEX Edge (TLS termination, rate limiting, DDoS, IP blocking)
    ↓
VORTEX Router (host-header routing, path routing, SNI routing, load balancing)
    ↓
Backend (your app on localhost:3000, or tunnel to another node)
```

### Protocol support
- `https`: TLS-terminated HTTP/1.1 + HTTP/2 reverse proxy
- `http`: Plain HTTP (use behind another TLS terminator)
- `h3`: HTTP/3 over QUIC
- `tcp`: Raw TCP tunnel with optional mTLS
- `udp`: Raw UDP forwarding with rate limiting
- `ws`: WebSocket proxy (sticky sessions)
- `grpc`: gRPC proxy with HTTP transcoding

### TLS certificate lifecycle
1. First request hits `api.myapp.com`
2. VORTEX checks cert cache
3. No cert found → ACME challenge to Let's Encrypt
4. Cert issued in ~3 seconds
5. Cert stored encrypted on disk
6. At 30 days before expiry → auto-renew
7. Human never touches a certificate file

### mTLS for inter-service communication
- Every VORTEX node gets a SPIFFE-compatible certificate on join
- Cert issued by cluster-internal CA
- Rotated every 24 hours automatically
- All TCP tunnel routes can enable `mtls: true`
- Effect: your database port is NEVER accessible without a valid cert, even on the private network

---

## Security Model

### Zero-trust principle
Every connection must prove identity before it carries data. This applies even on the private network between your own VPS nodes.

### Layers (outside to inside)
1. **Edge**: DDoS detection, IP blocklist, rate limiting, Tor blocking
2. **TLS**: Minimum TLS 1.2, auto-renewed certs
3. **Authentication**: OIDC/SSO, API keys, mTLS for inter-service
4. **Authorization**: OPA policy engine with Rego rules
5. **Secret access**: ChaCha20-Poly1305 encrypted store, injection only at runtime
6. **Audit**: HMAC-chained tamper-proof log of every action

### Secret management rules
```bash
# CORRECT — set value once, never in config
vortex secret set db_password "my-actual-password"

# The config only declares the name
secrets: { keys: ["db_password"] }

# VORTEX injects at runtime as env var
# Your app reads: process.env.DB_PASSWORD
# The value never touches a file or config
```

### Audit log integrity
Every audit log entry signs the previous entry with HMAC-SHA256.
```bash
vortex audit verify   # Mathematically proves log has not been tampered with
vortex audit export --format=splunk --since=7d
```

---

## Dashboard

The web UI is served by the VORTEX binary itself at `http://your-server:9090` (configurable). No separate server, no separate process, no extra RAM.

### What it shows
- **Overview**: cluster health, req/s, P99 latency, error rate, active connections, 30-min traffic chart, recent events, security snapshot
- **Nodes**: each node's CPU/RAM/disk, Raft leader indicator, join/drain/restart controls
- **Routes**: all active routes, traffic per route, add/edit/delete routes visually
- **Traffic inspector**: tap live traffic on any route, filter by status/path/IP
- **Metrics**: Prometheus-compatible graphs, SLO tracking, error budget
- **Security**: cert expiry, blocked IPs, mTLS status, secret store health
- **Agents**: status of all running agents, inter-agent conversation log, token usage, cost
- **Audit log**: searchable tamper-proof timeline of every action
- **Studio**: embedded link to browser IDE
- **Settings**: users, teams, API keys, SSO config, notification rules

---

## Messaging Integration

### Two-way Telegram control
```
User → Telegram → VORTEX bot → Coordinator Agent → action → result → Telegram → User
```

Commands (natural language, not slash commands):
- "build me a currency converter app"
- "why is the API slow?"
- "block 198.51.100.4"
- "show me the last 50 errors from the payments service"
- "scale up by one node"
- "deploy staging to production"
- "what's the disk usage?"

### WhatsApp
- Same capabilities as Telegram via WhatsApp Business API
- Ideal for: daily reports delivered as images, receiving alerts, approving agent actions
- File delivery: APKs, PDFs, charts

### Notification escalation chain
```
Severity low  → Slack only
Severity med  → Slack + Telegram
Severity high → Slack + Telegram + WhatsApp
Critical      → All channels + SMS (Twilio) + PagerDuty page
```
Escalation stops the moment someone acknowledges.

---

## VORTEX Forge — Detailed Flow

This is the flagship feature. Here is exactly what happens:

### Step 1 — User sends message
```
User [Telegram]: build me an app that predicts IPL match scores
```

### Step 2 — Coordinator asks clarifying questions (max 3)
```
VORTEX: Quick questions:
1. Win probability or score prediction?
2. Live data or historical only?
3. Android APK, web app, or both?

User: win probability, live data, both
```

### Step 3 — Coordinator creates task brief, spawns agents
```
Task: IPL win probability predictor
Stack: Python FastAPI (backend) + Flutter (mobile) + Next.js (web)
Data: CricAPI for live data
Agents: DataAgent, ModelAgent, BackendAgent, FrontendAgent, BuildAgent, QAAgent, DeployAgent
Budget: $0.50 AI token cap
```

### Step 4 — Agents work autonomously (no user involvement)
```
DataAgent     → fetches 952 IPL matches, cleans data, sends to ModelAgent
ModelAgent    → trains classifier, 71% accuracy (below threshold)
              → adds features, retrains, 78.4% (threshold met), sends to BackendAgent
BackendAgent  → writes FastAPI app, starts on :8421, self-tests, calls DeployAgent
DeployAgent   → registers vortex route api.ipl.yourdomain.com → :8421 with TLS
FrontendAgent → writes Flutter app pointing to API, calls BuildAgent
BuildAgent    → flutter build apk --release (takes ~3 min)
QAAgent       → tests API (all pass), smoke-tests APK on headless emulator (pass)
DeliverAgent  → sends to Telegram
```

### Step 5 — Delivery
```
VORTEX [Telegram]:
✅ Done in 11m 32s

🌐 Web app: https://ipl.yourdomain.com
📡 API: https://api.ipl.yourdomain.com/predict
📱 [APK attached — 28.4 MB]

Model accuracy: 78.4% on 2015-2024 data
AI cost: $0.07
Agents used: 7
```

### Self-healing during build
If any agent fails:
- Build fails → Claude reads error, patches code, rebuilds
- Max 3 retries per agent
- If still failing → Coordinator asks user: "The Flutter build is failing due to an Android SDK version conflict. Should I try React Native instead?"

---

## Complete M1–M20 Build Plan

### Phase 1 — Foundation (M1–M4) — weeks 1–20

#### M1 — Core runtime & project foundation
- M1.1: Mono-repo scaffold (cmd/, internal/, pkg/, web/, agents/, plugins/)
- M1.2: CUE config engine with hot-reload and startup validation
- M1.3: CLI foundation (Cobra) — start, stop, status, reload, version
- M1.4: Lifecycle manager — graceful shutdown, SIGTERM/SIGHUP, systemd unit generator
- M1.5: Structured logging (slog) — JSON + human modes, correlation IDs
- M1.6: CI/CD pipeline — GitHub Actions, golangci-lint, GoReleaser, build matrix
- M1.7: Installer script — curl | sh, SHA256 verify, auto-detect OS/arch

#### M2 — Networking core
- M2.1: TCP tunnel engine — bidirectional, connection pooling, backpressure
- M2.2: HTTP/1.1 + HTTP/2 reverse proxy — host/path/SNI routing, weighted backends
- M2.3: QUIC/HTTP3 transport — quic-go, 0-RTT reconnect, TCP fallback
- M2.4: Auto TLS (ACME) — Let's Encrypt, auto-renew, local CA for dev
- M2.5: UDP tunnel — raw forwarding, rate limiting, DSCP marking
- M2.6: Protocol gateway — HTTP↔gRPC transcoding, WebSocket sticky sessions
- M2.7: Rust hot-path (FFI) — packet forwarding core for >1Gbps

#### M3 — Security & zero-trust
- M3.1: mTLS identity mesh — SPIFFE-compatible, auto-rotation every 24h
- M3.2: Encrypted secret store — ChaCha20-Poly1305, env injection
- M3.3: Secret adapters — HashiCorp Vault, AWS SSM, GCP Secret Manager
- M3.4: OPA policy engine — embedded, Rego policies, hot-reload
- M3.5: RBAC system — Org/Team/User hierarchy, OIDC SSO
- M3.6: Tamper-proof audit log — HMAC chain, export to S3/Splunk/Elastic
- M3.7: Rate limiting + DDoS — token bucket, auto-ban, Tor blocklist

#### M4 — Cluster & HA
- M4.1: Raft consensus — hashicorp/raft, split-brain prevention
- M4.2: SWIM gossip discovery — hashicorp/memberlist, no Consul needed
- M4.3: Auto-failover — <1s reroute, health probes every 500ms
- M4.4: Distributed config sync — Raft-replicated, rollback command
- M4.5: Geo-aware routing — latency-based, region pinning
- M4.6: Cluster CLI — join/leave/status/drain/promote commands
- M4.7: Chaos mode — controlled fault injection for resilience testing

### Phase 2 — Platform Core (M5–M8) — weeks 21–50

#### M5 — Observability stack
- M5.1: OpenTelemetry native — traces/metrics/logs by trace ID, OTLP export
- M5.2: Prometheus metrics — 1000+ metrics, per-route/node/tenant labels
- M5.3: Continuous profiling — pprof, heap/CPU/goroutine, Pyroscope integration
- M5.4: Live traffic inspector — `vortex inspect <route>`, HTTP-aware tap
- M5.5: SLO engine — error budget tracking, burn rate alerts, multi-window
- M5.6: Alert rule engine — declarative rules, webhook/Slack/Telegram notifiers

#### M6 — Plugin system
- M6.1: WASM plugin runtime — wasmtime, memory/CPU limits, crash isolation
- M6.2: Hook middleware chain — priority-ordered, abort-on-error, per-route
- M6.3: Go plugin SDK — typed interfaces, helper functions, example plugins
- M6.4: Rust plugin SDK — performance-critical plugins, WASM compile target
- M6.5: Plugin registry — semantic versioning, ed25519 signing, trust verification
- M6.6: Built-in plugin pack — jwt-auth, rate-limiter, cors, compression, oauth2-proxy

#### M7 — Management dashboard
- M7.1: React + Vite scaffold — TypeScript, shadcn/ui, embedded in binary
- M7.2: Overview dashboard — live metrics, traffic chart, events, security snapshot
- M7.3: Visual route builder — drag-and-drop, weight sliders, plugin attachment
- M7.4: Cluster topology map — visual node map, traffic flow lines, drill-down
- M7.5: Audit log viewer — searchable timeline, diff viewer, integrity verify
- M7.6: User + team management — invites, roles, API keys, MFA, SSO config
- M7.7: Secret manager UI — names only (never values), usage audit, rotation

#### M8 — Multi-tenancy
- M8.1: Namespace engine — complete isolation, cgroups resource limits
- M8.2: Resource quotas — bandwidth/connection/route limits, soft+hard thresholds
- M8.3: Usage metering — byte/request counters, Stripe/Lago billing export
- M8.4: White-label dashboard — custom logo/colors/domain per org
- M8.5: Compliance exports — SOC2, GDPR, HIPAA, PII redaction

### Phase 3 — Intelligence Layer (M9–M12) — weeks 51–82

#### M9 — AI gateway & provider abstraction
- M9.1: Universal AI gateway — Claude, GPT, Gemini, Mistral, Cohere, Groq, Ollama
- M9.2: Smart model router — route by cost, speed, capability (rules in CUE)
- M9.3: Local LLM (Ollama) — pull models, run on-VPS, zero data leaves server
- M9.4: AI cost tracking — per-request tokens, per-app daily cost, budget caps
- M9.5: Context & memory layer — session history, pgvector long-term memory, RAG
- M9.6: AI audit log — every call logged, PII scrubbing, tamper-proof

#### M10 — Agent runtime & coordinator
- M10.1: Agent lifecycle manager — CUE-defined agents, persistent + task agents
- M10.2: Tool registry — every agent action is a declared, enumerated tool
- M10.3: Permission sandbox — seccomp enforcement, no tool = no action
- M10.4: Inter-agent message bus — typed messages, pub/sub + request/reply
- M10.5: Coordinator agent — user-facing, decomposes tasks, manages sub-agents
- M10.6: Agent observability — per-agent metrics, conversation log, cost
- M10.7: Human-in-the-loop — pause + Telegram approval for irreversible actions

#### M11 — Messaging integration
- M11.1: Telegram bot engine — natural language, file delivery, inline buttons
- M11.2: WhatsApp integration — WhatsApp Business API, rich media, groups
- M11.3: Slack integration — slash commands, alert threads, interactive buttons
- M11.4: Discord integration — bot commands, role-mapped permissions
- M11.5: Email integration — SMTP send, IMAP monitoring, HTML reports
- M11.6: SMS + PagerDuty — Twilio/SNS, severity-gated, deduplication
- M11.7: Notification router — rules engine, silence windows, ack tracking

#### M12 — VORTEX Studio
- M12.1: code-server integration — VS Code in browser, TLS + auth, mobile-ready
- M12.2: AI pair programmer — codebase-aware, uses VORTEX AI gateway
- M12.3: Web terminal — xterm.js, session recording, SSH gateway
- M12.4: DB Studio — browser DB GUI via mTLS tunnel, read-only production mode
- M12.5: Git panel — branches, PRs, deploy triggers, GitHub/GitLab OAuth
- M12.6: Container panel — Docker management, auto-route new containers

### Phase 4 — Autonomous Modes (M13–M16) — weeks 83–118

#### M13 — VORTEX Forge (app builder)
- M13.1: Intent parser — classifies request, extracts requirements, asks ≤3 questions
- M13.2: Stack selector — chooses tech stack based on requirements
- M13.3: Dependency agent — installs packages, verifies, caches, handles conflicts
- M13.4: Code generation agent — Claude writes code, iterates on test failures
- M13.5: Build agent — headless builds (Flutter/npm/go), progress to Coordinator
- M13.6: QA agent — API tests, APK smoke test, performance check, security check
- M13.7: Deploy + deliver agent — routes web app, sends APK+URL to Telegram

#### M14 — VORTEX DevOps (infra management)
- M14.1: Watchdog agent — 5s process monitoring, crash detect → restart → alert
- M14.2: Resource monitor agent — 30s sampling, auto-cleanup, threshold alerts
- M14.3: Diagnostic agent — root cause analysis, heap dump reading, plain English
- M14.4: Heal agent — safe autonomous fixes, human approval for code changes
- M14.5: Predictive scale agent — 14-day pattern learning, pre-scale before peaks
- M14.6: Cron engine — tracked jobs, failure alerts, retry with backoff
- M14.7: Incident report agent — correlate signals, write postmortem, file GitHub issue

#### M15 — Research, Data, Learn modes
- M15.1: Research mode — multi-agent deep research, PDF report delivery
- M15.2: Competitor monitor agent — watches URLs, detects changes in 30 min
- M15.3: Data pipeline engine — ingest/transform/visualize, any source to WhatsApp
- M15.4: DuckDB local warehouse — embedded, SQL query, historical storage
- M15.5: Learn mode — codebase indexer (pgvector), re-indexes on every deploy
- M15.6: Knowledge Q&A agent — answers questions with file+line references
- M15.7: Auto-docs agent — reads code → generates docs, runs on every deploy

#### M16 — Guard, Comms, Commerce modes
- M16.1: Guard — security patrol agent (nightly CVE scan, auto-patch)
- M16.2: Guard — threat response (real-time brute force/DDoS blocking)
- M16.3: Guard — compliance agent (SOC2/GDPR/HIPAA report generation)
- M16.4: Comms — inbox agent (email draft + Telegram approval before send)
- M16.5: Comms — content agent (blog posts, social media, scheduled)
- M16.6: Commerce — shop builder (Next.js store, product photos → listings)
- M16.7: Commerce — operations agent (daily reports, low stock, SEO)

### Phase 5 — Production & Launch (M17–M20) — weeks 119–152

#### M17 — Developer experience & ecosystem
- M17.1: VS Code extension — CUE validation, autocomplete, hover tooltips
- M17.2: Terraform provider — full CRUD for routes/secrets/nodes/plugins
- M17.3: GitHub Actions — validate, deploy, health-gate actions
- M17.4: OpenAPI 3.1 spec + SDKs (TypeScript, Python, Go, Rust)
- M17.5: Buildpacks — Heroku-style `git push` deploy, language auto-detection
- M17.6: Migration tools — nginx.conf → VORTEX config converter, OpenClaw/Hermes

#### M18 — Performance hardening
- M18.1: Benchmark suite — k6 + wrk2 in CI, 100k conn/node target
- M18.2: Memory + CPU profiling — continuous, leak detection, regression alerts
- M18.3: Horizontal autoscale hooks — Hetzner/DO/Vultr/Linode APIs
- M18.4: OS-level tuning — `vortex tune` applies optimal sysctl settings
- M18.5: Adaptive buffer sizing — dynamic based on measured RTT

#### M19 — Security audit & compliance
- M19.1: Third-party pentest — external firm, full scope
- M19.2: Fuzzing suite — go-fuzz on all protocol parsers, OSS-Fuzz integration
- M19.3: SBOM generation — CycloneDX format, published with releases
- M19.4: SLSA Level 2 — signed provenance, reproducible builds, Sigstore
- M19.5: CVE process — security.txt, HackerOne, 90-day disclosure policy
- M19.6: Dependency audit — govulncheck + trivy on every PR, Dependabot

#### M20 — Public launch v1.0.0
- M20.1: Documentation site — Docusaurus, docs.vortex.run
- M20.2: One-line installer — curl | sh, all major Linux distros + macOS
- M20.3: Package ecosystem — Debian/RPM, Homebrew, Docker, Helm, Nix, Winget
- M20.4: VORTEX Cloud — managed SaaS on top of OSS, funds development
- M20.5: Community infrastructure — Discord, GitHub Discussions, office hours, RFC process
- M20.6: Launch campaign — Product Hunt, HN, YouTube demo
- M20.7: v1.0.0 stability commitment — stable API contract, LTS every 6 months

---

## Key Go Packages to Use

```
github.com/hashicorp/raft          // Raft consensus
github.com/hashicorp/memberlist    // SWIM gossip
github.com/open-policy-agent/opa   // OPA policy engine
github.com/quic-go/quic-go         // QUIC/HTTP3
github.com/tetratelabs/wazero      // WASM runtime (or wasmtime-go)
go.opentelemetry.io/otel           // OpenTelemetry
github.com/prometheus/client_golang // Prometheus
github.com/spf13/cobra             // CLI
github.com/cue-lang/cue            // CUE config
golang.org/x/crypto/acme           // ACME / Let's Encrypt
github.com/dop251/goja             // (if JS scripting needed)
pgvector/pgvector-go               // Vector embeddings
github.com/go-telegram-bot-api/...  // Telegram bot
```

---

## Where to Start — M1 Exact Steps

Claude Code: start here, in this order, nothing else first.

1. Create `go.mod` with module name `github.com/vortex-run/vortex`
2. Create the directory structure listed in the repo structure section above
3. Create `cmd/vortex/main.go` — bare bones, just boots and prints version
4. Create `internal/config/` — CUE schema loader, validates on boot
5. Create `pkg/logger/` — slog wrapper with correlation ID
6. Create `pkg/lifecycle/` — SIGTERM/SIGHUP handlers, graceful shutdown
7. Create `internal/api/` — minimal HTTP server for health check endpoint
8. Create `Taskfile.yml` — build, test, lint, run commands
9. Create `.github/workflows/ci.yml` — test + lint on every PR
10. Create `scripts/install.sh` — detects OS, downloads binary, installs service
11. Create `config/schema.cue` — the master config schema

Each step should result in `task build` producing a working binary before moving to the next step.

---

## Non-Negotiable Architecture Rules

1. **Single binary** — everything compiles into one binary. No sidecar processes required to run.
2. **Secrets never in config files** — config only has key names. Values live in encrypted store only.
3. **Config is always valid or rejected** — CUE validation at startup, never at first use.
4. **No agent talks to users directly** — only the Coordinator talks to Telegram/WhatsApp/CLI.
5. **Every agent action is a declared Tool** — no agent can do anything not in its tool registry.
6. **Nothing ships from Forge until QA passes** — the QA gate is non-negotiable.
7. **Every action is audited** — config changes, deploys, secret access, agent actions, all in the HMAC-chained log.
8. **Human approval before irreversible agent actions** — Telegram/WhatsApp button approval required.
9. **Tests before merging** — `task test` must pass. No exceptions.
10. **Go standard library first** — only add a dependency if stdlib genuinely cannot do it.

---

## What Success Looks Like at Each Phase

**After M4 (Phase 1 complete):**
```bash
curl -sL get.vortex.run | sh
# Edit vortex.cue with your domain and backend port
vortex start
# Your app is now at https://yourdomain.com with auto TLS
# Add a second VPS: vortex cluster join 10.0.0.2
# Traffic automatically load-balanced across both nodes
```

**After M8 (Phase 2 complete):**
```bash
# Open browser → https://dashboard.yourdomain.com
# See live traffic, metrics, route builder, audit log
# Invite team member → they get scoped dashboard access
# Plugin: vortex plugin install jwt-auth
```

**After M12 (Phase 3 complete):**
```bash
# Telegram: "why was the API slow at 3pm?"
# VORTEX: reads logs/traces/metrics → plain English answer
# Browser: studio.yourdomain.com → full VS Code on your VPS
```

**After M16 (Phase 4 complete):**
```bash
# Telegram: "build me a cricket score app"
# 12 minutes later:
# VORTEX: [APK attached] + https://cricket.yourdomain.com
```

**After M20 (launch):**
```bash
curl -sL get.vortex.run | sh
# Anyone in the world can install VORTEX in 60 seconds
```

---

## Final Notes for Claude Code

- **Do not skip M1** and jump to exciting features. The foundation must be solid.
- **Run `task test` after every sub-module** before moving to the next.
- **Keep the binary size minimal** — profile before adding any dependency.
- **Read the CUE documentation** before implementing M1.2 — CUE is not YAML and not JSON Schema.
- **The Rust FFI (M2.7) is optional for M2** — build Go path first, add Rust only after benchmarking shows it's needed.
- **Agent runtime (M10) is the most complex module** — do not rush it. Get M9 (AI gateway) solid first.
- **Every PR needs a test** — this project will be used in production by real people on real servers.

The entire product has been designed. Now it needs to be built. Start at M1.1.