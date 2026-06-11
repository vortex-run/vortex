# Configuration

VORTEX reads a single [CUE](https://cuelang.org) file (default
`vortex.cue` / `/etc/vortex/vortex.cue`). CUE gives schema validation and
clear errors. Validate without starting:

```bash
vortex check --config vortex.cue
```

Reload a running server after editing (no restart, never drops connections):

```bash
vortex reload
```

## Top-level sections

```cue
cluster:       { ... }   // node + cluster identity
tls:           { ... }   // certificate provider
routes:        [ ... ]   // proxy listeners
security:      { ... }   // edge protection, blocklists
secrets:       { ... }   // declared secret keys + injection
observability: { ... }   // logging, metrics, tracing
```

### cluster

```cue
cluster: {
  name: "my-cluster"   // identity; shown in /api/status
}
```

> **Note:** the cluster name is **not** a secret and is no longer used to derive
> any encryption key (see [security.md](security.md)). At-rest keys come from a
> random master key.

### tls

```cue
tls: {
  provider:    "internal"   // "internal" | "letsencrypt" | "zerossl"
  acme_email:  "you@example.com"
  min_version: "TLS1.2"     // "TLS1.2" | "TLS1.3"
}
```

- `internal` — a local CA issues certs (great for dev / mTLS mesh).
- `letsencrypt` / `zerossl` — ACME, requires a reachable HTTP-01 challenge.

TLS is hardened by default: forward-secret ECDHE-only cipher suites, 24h
session-ticket-key rotation.

### routes

```cue
routes: [
  { name: "web",  protocol: "https", listen: 443,
    backends: [{ host: "127.0.0.1", port: 8080 }] },
  { name: "db",   protocol: "tcp",   listen: 5432, mtls: true,
    backends: [{ host: "127.0.0.1", port: 5432 }] },
  { name: "edge", protocol: "h3",    listen: 8443,
    backends: [{ host: "127.0.0.1", port: 9000 }] },
]
```

Protocols: `http`, `https`, `tcp`, `udp`, `h3` (HTTP/3 over QUIC). Set
`mtls: true` to require a client certificate from the cluster CA.

### security

```cue
security: {
  rate_limit: { rpm: 600, burst: 100 }
  ip_blocklist: ["10.0.0.0/8"]
  ip_allowlist: []           // when non-empty, only these are allowed
  block_tor: false
}
```

The management API additionally enforces per-API-key and global rate limits and
per-IP burst auto-banning (>100 req/s → 5-min ban).

### secrets

```cue
secrets: {
  store:      "local"          // "local" | "vault" | "aws-ssm" | "gcp-sm"
  keys:       ["db_password", "jwt_secret"]
  inject_env: true             // inject set secrets into managed processes
}
```

Only the backend **selection** lives in config. All credentials/endpoints come
from the environment, never the config file. Set values with:

```bash
vortex secret set db_password 's3cret' --expires-in 90d --rotate-every 30d
```

### observability

```cue
observability: {
  log_level: "info"            // debug | info | warn | error
  log_sink:  "stderr"          // stderr | file
  metrics:   true              // Prometheus at /metrics
  tracing:   { enabled: false, otlp_endpoint: "" }
}
```

## Environment variables

| Variable | Purpose |
|---|---|
| `VORTEX_MASTER_KEY` | Inline master key (hex/base64). Overrides the key file. |
| `VORTEX_MASTER_KEY_FILE` | Path to the master key file. |
| `VORTEX_TRUST_LOOPBACK` | `false` to require auth on the control plane even from loopback. |
| `VORTEX_ANTHROPIC_KEY`, `VORTEX_OPENAI_KEY`, `VORTEX_DEEPSEEK_KEY`, `VORTEX_GEMINI_KEY`, `VORTEX_GROQ_KEY`, `VORTEX_OPENROUTER_KEY` | AI provider keys. |
| `VORTEX_BEDROCK_REGION` + `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | AWS Bedrock. |
| `VORTEX_AZURE_OPENAI_KEY` / `_ENDPOINT` / `_DEPLOYMENT` | Azure OpenAI. |
| `VORTEX_TELEGRAM_TOKEN`, `VORTEX_TELEGRAM_DEFAULT_CHAT` | Telegram bot. |
| `VORTEX_SECRET_STORE`, `VORTEX_AUDIT_LOG`, `VORTEX_MTLS_STORE` | Relocate on-disk state. |

Environment variables always take precedence over the saved `vortex setup`
config.
