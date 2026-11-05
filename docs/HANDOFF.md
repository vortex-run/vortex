# Gubern — Agent Handoff (read this first)

> **Audience:** AI coding agents and humans onboarding cold.  
> **Goal:** Understand the *product*, the *codebase*, and *how to change it safely* without prior chat context.  
> **Last aligned with:** post creator-sprint `main` (CVE-green CI, pinned release signing public key, OSS surface).

If you only read one file in this repo, read this. Then open the linked docs for depth.

---

## 0. 60-second briefing

**Gubern** (`github.com/vortex-run/vortex`) is a **self-hosted, single-tenant autonomous infrastructure platform** shipped as **one static Go binary**.

One process owns:

1. **Edge** — reverse proxy (HTTP/HTTPS/TCP/UDP/QUIC), TLS/mTLS, rate limits  
2. **Security** — secrets, API keys/RBAC, OPA policy, HMAC hash-chained audit  
3. **Observability** — Prometheus + OpenTelemetry  
4. **AI agent runtime** — coding, Forge builds, research, DevOps/SSH, pipelines, multi-agent orchestration  
5. **Control surfaces** — TUI, embedded web dashboard, Telegram (+ Slack/WhatsApp webhooks), HTTP API, OpenAI-compatible `/v1`

**Tagline:** *One binary. Any server. Fully autonomous.*  
**Config:** one CUE file (`gubern.cue`) + `GUBERN_*` env (legacy `VORTEX_*` still read). **Secrets never in CUE.**  
**License:** Apache-2.0. Nothing phones home.

**Maturity:** designed and tested as a **single-node** deployment; production readiness is not asserted by this document — judge it from the audit (`GUBERN_PRODUCTION_AUDIT.md`), CI evidence, and your own hardening review. Multi-node HA and container tool isolation are **not** done — do not claim them.

---

## 1. Product position (Gubern vs Hermes vs OpenClaw)

These three live in the same *self-hosted AI* universe. They are **not** the same product.

| | **OpenClaw** | **Hermes** | **Gubern** |
|---|---|---|---|
| **Center of gravity** | Chat↔agent **gateway** | Learning **agent** (memory/skills/cron) | **Infra platform** + agents |
| **Primary job** | Bridge WhatsApp/Telegram/… to coding agents | Persistent autonomous agent with sandboxes | Own the VPS: proxy+TLS+secrets+audit+agents |
| **Typical stack** | Node gateway, many channel plugins | Agent + Docker/SSH/Modal/Daytona backends | Single static **Go** binary + CUE |
| **Reverse proxy / QUIC / mTLS edge** | Not the product | Not the product | **Core** |
| **Secrets / OPA / HMAC audit** | Not the focus | Agent security / isolation focus | **Core** |
| **Chat channel breadth** | Huge (20+) | Broad | Telegram-first (+ Slack/WhatsApp hooks) |
| **Sandbox story** | Optional | **Strong** (container/cloud backends) | Partial: allow-list + approval + env scrub |
| **Winning line** | “Assistant in every chat app” | “Agent that learns and runs safely anywhere” | “One binary that *is* the server stack *and* runs it” |

**Pitch rule for agents writing copy or issues:**  
Do **not** say “OpenClaw in Go” or “Hermes with a proxy.” Say: *self-hosted edge + security + autonomous ops agents in one binary.*

**Claude Code comparison** (in README): Claude Code is an IDE coding agent; Gubern is coding agent **plus** the server it runs on.

---

## 2. Mental model (architecture)

```text
                    Internet / clients
                            │
                            ▼
        ┌───────────────────────────────────────────┐
        │  EDGE: proxy (HTTP/TCP/UDP/QUIC) + TLS    │
        │  + rate limit / IP policy                 │
        └───────────────────┬───────────────────────┘
                            │
         ┌──────────────────┼──────────────────┐
         ▼                  ▼                  ▼
   Backends           Policy/RBAC        Management :9090
   (your apps)        OPA / auth         API + /dashboard/
                                              │
                     ┌────────────────────────┼────────────────┐
                     ▼                        ▼                ▼
              Agent runtime            Secrets/keyring     Audit HMAC
              (coordinator +           + TLS stores        chain
               specialists)
                     │
        TUI · Telegram · OpenAI /v1 · forge/research/devops/…
```

**Two different “gateways” — never confuse them:**

| Path | Package | Meaning |
|------|---------|---------|
| `internal/gateway` | AI routing / context | Chat completions, provider failover |
| `internal/proxy/gateway` | `package proxygateway` | Edge reverse-proxy to backends |

**Composition root:** almost everything is wired in `internal/cmd/start.go` (large file — prefer extracting helpers over dumping more god-logic).

---

## 3. User journey (runtime)

```text
install → gubern setup → gubern start → control
```

| Step | Command / surface | Code / docs |
|------|-------------------|-------------|
| Install | curl installer or release zip | `docs/install.md`, `scripts/` |
| Setup | `gubern setup` | `internal/cmd/setup.go` |
| Start | `gubern start` / `gubern start --ui` | `internal/cmd/start.go` |
| TUI | `gubern ui` | `internal/tui/` |
| Dashboard | `http://localhost:9090/dashboard/` | `web/dashboard/` embedded via `internal/dashboard/` |
| Telegram | bot drives agent + approvals | `docs/telegram.md`, `internal/messaging/` |
| API | Bearer / `X-API-Key` on `:9090` | `docs/api.md`, `docs/openapi.yaml` |
| Project code | `gubern code` from a cwd | `internal/cmd/code.go` |
| Local demo | `pwsh scripts/demo/demo.ps1` | `scripts/demo/` |

Default management listen: **`:9090`**.

---

## 4. Repository map (from scratch)

```text
vortex/
├── cmd/gubern/main.go          # binary entry → internal/cmd.Execute()
├── internal/                   # ALL product logic (preferred import boundary)
├── pkg/                        # small reusable libs (atomicfile, safedial, …)
├── config/schema.cue           # embedded master schema (+ schema.go)
├── web/dashboard/              # React/Vite UI; dist/ COMMITTED + embedded
├── docs/                       # human + agent docs (you are here)
├── integration/                # process-level tests (build tag: integration)
├── scripts/                    # install, sign, demo, path helpers
├── .github/workflows/          # ci, security, release, codeql, scorecard
├── Taskfile.yml                # build/test/lint (not Makefile)
├── go.mod                      # module github.com/vortex-run/vortex ; toolchain go1.26.9
├── gubern.cue                  # example / local config
├── GUBERN_PRODUCTION_AUDIT.md  # independent audit + remediation status
└── agents|pipeline|research|plugins/   # OPERATOR scaffolding only — NOT product code
```

**Critical:** empty top-level `agents/`, `pipeline/`, `research/`, `plugins/` are drop-in scaffolds. **Implementation lives under `internal/`.** Do not add product Go packages to those empty roots.

---

## 5. `internal/` package catalog

Use this table to decide *where* a change belongs.

### Boot & CLI

| Package | Role |
|---------|------|
| `cmd/` | Cobra CLI: `start`, `stop`, `setup`, `ui`, `code`, `secret`, `service`, `verify`, `selfupdate`, … |
| `startup/` | Boot branding / progress display |
| `service/` | OS service install (systemd/Windows), firewall helper |
| `errors/` | User-facing actionable errors |

### Config & crypto

| Package | Role |
|---------|------|
| `config/` | Load/unify CUE vs embedded schema; hot reload; `GUBERN_*` env overlays |
| `keyring/` | Random master key → HKDF-SHA256 per-purpose keys |
| `secrets/` | Encrypted store + Vault/AWS SSM/GCP adapters |
| `update/` | Self-update; Ed25519 verify against `ReleaseSigningPublicKey` |

### Edge & security

| Package | Role |
|---------|------|
| `proxy/` | HTTP/TCP/UDP/QUIC reverse proxy, managers, health |
| `proxy/gateway/` | Backend gateway (**not** AI) |
| `tls/` | ACME, internal CA, mTLS mesh, cert store |
| `security/` | IP allow/block, Tor, edge rate limit |
| `policy/` | OPA/Rego engine |
| `auth/` | API keys (bcrypt), RBAC, OIDC |
| `audit/` | HMAC hash-chained NDJSON log, verify, compliance export |

### Control planes

| Package | Role |
|---------|------|
| `api/` | Management HTTP (`:9090`), OpenAI `/v1`, webhooks |
| `dashboard/` | Embed + serve React `dist/` |
| `tui/` | Bubble Tea terminal UI |
| `messaging/` | Telegram/WhatsApp/Slack + AI gateway wiring |
| `gateway/` | AI provider routing helpers / context |
| `studio/` | Browser IDE / terminal / git helpers |
| `observability/` | Metrics, OTel tracer, middleware |

### Agents & automation

| Package | Role |
|---------|------|
| `agents/` | Coordinator, tools, approval, memory, effect ledger, specialists |
| `orchestration/` | Multi-agent DAG, durable store, crash resume |
| `a2a/` | Agent-to-agent bus/RPC |
| `forge/` | Prompt → app/APK builds |
| `research/` | SSRF-hardened search/fetch/summarize |
| `devops/` | SSH / Docker / Nginx fleet ops (**known_hosts verified when configured**) |
| `pipeline/` | CSV/JSON analysis + charts |
| `healing/` | Health monitor, recovery, SLO-ish events |
| `plugins/` | Hooks + wazero WASM sandbox |
| `cluster/` | Gossip + Raft (**experimental**; state still node-local) |
| `tenancy/` | Namespace/quota primitives |
| `perf/` | Autoscaler metrics / benches |
| `testutil/` | Shared test helpers |

### Shared `pkg/`

| Package | Role |
|---------|------|
| `atomicfile/` | Temp + fsync + rename writes |
| `safedial/` | SSRF-safe dial (resolve once, pin IP, re-validate redirects) |
| `lifecycle/` | Graceful start/stop/reload hooks |
| `logger/` | Structured logging |
| `crypto/` | Small crypto helpers |
| `pidfile/` | PID file locking |
| `awsigv4/` | Shared AWS SigV4 HMAC helpers (Bedrock + SSM) |

---

## 6. Config & secrets (non-negotiable rules)

1. **Source of truth:** `gubern.cue` unified against embedded `config/schema.cue`. Invalid config = **fail start**.  
2. **Env overlays:** `GUBERN_<SECTION>_<FIELD>` for **scalars only** (`internal/config/env.go`). Lists/routes are not env-overridable.  
3. **Secrets:** names in CUE; values via secret store / keyring / provider env (`GUBERN_ANTHROPIC_KEY`, `GUBERN_OPENAI_KEY`, …).  
4. **Master key:** `GUBERN_MASTER_KEY` or generated file — never derive from cluster name (audit C1 fixed).  
5. **Loopback trust:** control endpoints may trust loopback by default; set `GUBERN_TRUST_LOOPBACK=false` behind a same-host reverse proxy.  
6. **Hot reload:** SIGHUP / `POST /internal/reload` / `gubern reload`.

Full reference: `docs/configuration.md`.

---

## 7. API surface (quick)

Public: `GET /health`, `GET /ready`, `GET /dashboard/`  

Control (loopback trust optional): `/internal/*`, `/metrics`, `/api/status`, admin keys/audit  

Data plane (always API key): `/api/agents/*`, `/api/forge/*`, research/devops/pipeline/orchestrate, logs, notify, namespaces  

OpenAI-compat: `/v1/models`, `/v1/chat/completions`, `/v1/responses`  

Messaging: `/webhook/` (signature-verified)

Narrative: `docs/api.md` · Machine sketch: `docs/openapi.yaml`

Auth: `Authorization: Bearer <key>` or `X-API-Key: <key>`.

---

## 8. Agent runtime (how AI features fit)

```text
User message (TUI / API / Telegram / /v1)
        → Coordinator (internal/agents)
        → classify / route
        → tools (sandboxed FS, http_get via safedial, shell with approval)
        → specialists: forge | research | devops | pipeline | orchestrate
        → effect ledger fences side effects on crash resume (H3)
```

**Approval gate:** primary containment for machine-touching actions. In **release** builds (`-tags=release`) the gate is unwaivable. Dev builds can differ — CI runs both.  
**Workspace trust / Auto Mode:** configure `agents.local_tools` in CUE (`trusted_root`, `auto_mode`, defaults off). Wired at startup into local tools; Auto Mode may waive repeated in-scope file approvals only. Shell stays gated. Not whole-PC access. Restart required after config change (not hot-reloaded into tool registries).  
**Catalog / routing:** `internal/a2a` manifests + `GET /api/agents/catalog`; planner/team can resolve `required_capabilities` (no silent unrelated fallback).  
**Verification:** structured `verification_criteria` only (`nonempty`, `contains:`, `prefix:`) — executed post-task; free text rejected.  
**Orchestration UI:** `GET /api/orchestration/runs` (+ tasks) + Agents page; lists recent runs (**running/done/failed**, newest first, bounded limit) via `RunStore.ListRecent`. Both routes require a valid API key (no loopback bypass).  
**Do not claim container isolation** — audit M5 still open.

Specialist docs: `docs/agents.md`.

---

## 9. Security & audit posture

Read: `docs/security.md`, `GUBERN_PRODUCTION_AUDIT.md`, `SECURITY.md`.

| Topic | Status |
|-------|--------|
| Master-key HKDF | Fixed (C1) |
| SSRF / DNS rebinding | Fixed (`pkg/safedial`) |
| Durable orchestration + effect fencing | Fixed + verified (H3) |
| Release Ed25519 signing | Public key **pinned** in `internal/update/signature.go`; private key = GH secret `VORTEX_SIGNING_KEY` |
| SSH known_hosts | Verified when path set; else TOFU |
| Multi-node shared state (M2) | **Open** |
| Container tool isolation (M5) | **Partial / open** |

Patent-sensitive areas (counsel before deep public algorithm writeups): effect fencing, HMAC audit chain, HKDF purpose keys — see `docs/IP_NOTES.md`.

---

## 10. Frontend (dashboard)

- Stack: React 19, Vite, TanStack Query, **Zustand** (`web/dashboard/src/store.ts`), React Router 8, Tailwind 3  
- Served at `/dashboard/` from embedded `web/dashboard/dist`  
- **After UI changes:** `npm ci && npm run lint && npm run build` and **commit `dist/`** — CI fails if dist is stale  
- State rule: Query = server state; Zustand = local UI. Do not add another state library.  
- Audit: `npm run audit:ci` + `audit-allowlist.json` (only allow with written reachability)

---

## 11. Build, test, CI (agent checklist)

**Toolchain:** Go **1.26.9+** (`toolchain` in `go.mod`; `GOTOOLCHAIN=auto` OK). Task. Node 24+ for dashboard.

```bash
task build
task test              # merge gate
task test:race
task lint
go test -tags release ./internal/agents/...
go test -tags integration -timeout 900s ./integration/...
cd web/dashboard && npm ci && npm run lint && npm run build && npm run audit:ci
```

**CI workflows:**

| Workflow | Purpose |
|----------|---------|
| `ci.yml` | build, race tests, release-tag agent tests, coverage artifact, lint, integration, dashboard |
| `security.yml` | pinned `govulncheck`, dependency-review |
| `release.yml` | tag `v*` → GoReleaser + SBOM + optional sign |
| `codeql.yml` / `scorecard.yml` | supply-chain / SAST signals |

**Invariants for PRs:**

- No secrets in commits (`.secrets/` gitignored)  
- No mass `internal/` folder reorg without strong reason  
- Prefer tests next to code  
- Stdlib-first; static binary ethos (`CGO_ENABLED=0` for release)  
- Atomic persistence via `pkg/atomicfile`  

---

## 12. Where to change what (routing guide)

| If you need to… | Start here |
|-----------------|------------|
| Add CLI flag/command | `internal/cmd/` |
| Add HTTP route | `internal/api/api.go` (+ handler file) |
| Change boot wiring | `internal/cmd/start.go` (keep adapters thin) |
| Add AI provider | `internal/messaging/ai_*.go`, gateway config |
| Add agent tool | `internal/agents/tools.go` / local tools + approval |
| Change proxy behavior | `internal/proxy/` |
| Change CUE schema | `config/schema.cue` + loader tests |
| Change dashboard page | `web/dashboard/src/pages/` then rebuild dist |
| Fix SSH trust | `internal/devops/ssh.go` |
| Signing / self-update | `internal/update/`, `scripts/sign.sh`, `scripts/signing-key-setup.md` |
| Demo environment | `scripts/demo/` |

---

## 13. Known pitfalls (agents get these wrong)

1. **Editing dashboard source without rebuilding/committing `dist/`** → CI Dashboard job fails.  
2. **Putting secrets in `gubern.cue` or tests** → forbidden.  
3. **Confusing AI `gateway` with `proxy/gateway`.**  
4. **Treating empty top-level `agents/` as the agent package** — real code is `internal/agents`.  
5. **Claiming multi-node HA or container isolation** — false.  
6. **Skipping `-tags=release` tests** when touching approval/security.  
7. **Allowlisting npm advisories without a reachability argument** — `audit-allowlist.json` is audited prose, not a mute button.  
8. **Leaving `ReleaseSigningPublicKey` empty** — regresses audit H4; key is pinned for a reason. Private half only in CI secret.  
9. **Ignoring `gofmt` / golangci** — CI Lint is required.  
10. **God-file growth** — prefer small helpers over another 500 lines in `start.go`.

---

## 14. Doc index (read order for depth)

| Order | Doc | Why |
|------|-----|-----|
| 1 | **This file** (`docs/HANDOFF.md`) | Orientation |
| 2 | `README.md` | Product pitch + quick start |
| 3 | `docs/development.md` | Layout + conventions |
| 4 | `docs/configuration.md` | CUE + env |
| 5 | `docs/security.md` | Threat model + honest boundaries |
| 6 | `docs/agents.md` | Agent capabilities |
| 7 | `docs/api.md` + `docs/openapi.yaml` | HTTP surface |
| 8 | `docs/telegram.md` | Phone control |
| 9 | `docs/install.md` | Install paths |
| 10 | `GUBERN_PRODUCTION_AUDIT.md` | What’s fixed vs open |
| 11 | `CONTRIBUTING.md` / `SECURITY.md` | Community + vuln reporting |
| 12 | `docs/IP_NOTES.md` | Patent counsel note (maintainers) |
| 13 | `docs/runbooks.md` | Operator incident and recovery steps |
| 14 | `docs/troubleshooting.md` | Common setup problems, state locations, messaging and approval gotchas |

---

## 15. Success criteria when you finish a task

- [ ] Change lives in the right package (table in §5 / §12)  
- [ ] Tests added/updated; `task test` (and release/integration tags if relevant) pass  
- [ ] Dashboard: lint + build + dist committed if UI touched  
- [ ] No new hardcoded secrets; config stays CUE+env  
- [ ] Docs updated if behavior or public API changed  
- [ ] Commit messages explain **why**; CI green on the PR  

---

## 16. One-paragraph product story (for issues/PRs/README)

People started with IDE coding agents, then chat gateways (OpenClaw) and learning agents (Hermes). Gubern takes the ops-shaped step: if the agent lives on your server, the product should also *be* that server’s edge and security plane. One `gubern start` replaces a pile of sidecars; the same process that terminates TLS can rebuild an app or restart nginx when you message Telegram — with an audit trail and signed updates.

---

*End of handoff. Prefer linking here from agent prompts: `docs/HANDOFF.md`.*
