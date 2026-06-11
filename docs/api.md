# API Reference

The management API listens on `:9090` (configurable). Responses are JSON.
Every response carries an `X-Correlation-ID` and the security headers
(HSTS over TLS, `X-Content-Type-Options`, CSP, etc.).

## Authentication

Protected endpoints require an API key, sent as `Authorization: Bearer <key>`
or `X-API-Key: <key>`. Issue keys with `POST /api/keys` (admin) or `vortex
setup`.

- **Public** (no auth): `/health`, `/ready`, `/dashboard/`.
- **Control plane** (`/internal/*`, `/metrics`, `/api/status`, …): allowed from
  loopback without a key by default; set `VORTEX_TRUST_LOOPBACK=false` to
  require a key even from loopback (recommended behind a same-host proxy).
- **Data plane** (`/api/agents/*`, `/api/forge/*`, …): always require a key.

Rate limits apply per source IP, per API key (1000 req/min default; admin keys
unlimited), and globally (10000 req/min), plus per-IP burst auto-banning.

## Health & readiness

```
GET /health
→ {"status":"ok","version":"v0.2.0","config_hash":"…","uptime":"3m12s"}

GET /ready
→ 200 {"ready":true}   or   503 {"ready":false,"reason":"agent queue saturated (…)"}
```

`cluster_name` is included in `/health` only for trusted (loopback/authed)
callers.

## Status & metrics

```
GET  /api/status          → extended status (TLS provider, routes, plugins, …)
GET  /api/ai/cost         → today's AI spend and budget
GET  /metrics             → Prometheus exposition
```

## Agents

```
POST /api/agents/submit
  body: {"message":"...","session_id":"optional"}
  Accept: text/event-stream  → SSE streamed chunks
  else                       → {"response":"...","session_id":"..."}

GET  /api/agents/status              → {active_agents, total_messages, queue_depth}
POST /api/agents/approve             → {"session_id":"...","approved":true}
GET  /api/agents/history             → list sessions (newest first)
GET  /api/agents/history/{id}        → messages for a session
```

`session_id` must match `^[A-Za-z0-9_.-]{1,64}$`. Responses are capped at 8 MB.

## VORTEX Forge

```
POST /api/forge/build       → start an app build
GET  /api/forge/status/{id} → build job status
GET  /api/forge/jobs        → list jobs   (503 if no AI provider configured)
```

## API keys (admin)

```
GET    /api/keys                 → list keys for ?org= (hashes never exposed)
POST   /api/keys                 → issue: {user_id, org_id, roles, ttl_seconds, rate_limit_rpm}
                                   → {id, secret, …}  (secret shown once)
DELETE /api/keys/{id}            → revoke
```

Keys are bcrypt-hashed (SHA-256 prehash), persisted atomically on issue/revoke.

## Audit

```
GET  /api/audit             → recent entries
POST /api/audit/verify      → verify the hash chain
```

CLI equivalents add reporting and archival:

```bash
vortex audit verify
vortex audit report --since 2026-01-01 --format markdown --output report.md
vortex audit archive
```

## Other data endpoints

```
GET  /api/healing/status         GET  /api/research/reports
GET  /api/devops/servers         POST /api/pipeline/analyze
GET  /api/namespaces             POST /api/orchestrate
GET  /api/secrets/status         GET  /api/plugins
GET  /api/logs
```

## Control plane (Windows-safe SIGHUP/SIGTERM)

```
POST /internal/reload       → re-read + re-validate config (vortex reload)
POST /internal/shutdown     → graceful shutdown (vortex stop)
```

Both honour loopback trust (see Authentication above).
