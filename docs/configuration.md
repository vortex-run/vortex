# Configuration

Gubern reads a single [CUE](https://cuelang.org) file (default
`gubern.cue` / `/etc/gubern/gubern.cue`). CUE gives schema validation and
clear errors. Validate without starting:

```bash
gubern check --config gubern.cue
```

Reload a running server after editing (no restart, never drops connections):

```bash
gubern reload
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
gubern secret set db_password 's3cret' --expires-in 90d --rotate-every 30d
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
| `GUBERN_MASTER_KEY` | Inline master key (hex/base64). Overrides the key file. |
| `GUBERN_MASTER_KEY_FILE` | Path to the master key file. |
| `GUBERN_TRUST_LOOPBACK` | `false` to require auth on the control plane even from loopback. |
| `GUBERN_API_LISTEN` | Management API address. Default `:9090` (all interfaces); `127.0.0.1:9090` keeps it local. `gubern stop`, `status`, `reload`, `ui` and `code` read it too, so set it in the shell you run them from (or pass `--api-port` / `--addr`). |
| `GUBERN_EDGE_BIND_HOST` | Interface the edge listens on: route listeners and the ACME challenge server on :80. Default empty (all interfaces); set an address such as `10.0.0.5` or `127.0.0.1` to restrict it. |
| `GUBERN_ANTHROPIC_KEY`, `GUBERN_OPENAI_KEY`, `GUBERN_DEEPSEEK_KEY`, `GUBERN_GEMINI_KEY`, `GUBERN_GROQ_KEY`, `GUBERN_OPENROUTER_KEY` | AI provider keys. |
| `GUBERN_BEDROCK_REGION` + `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | AWS Bedrock. |
| `GUBERN_AZURE_OPENAI_KEY` / `_ENDPOINT` / `_DEPLOYMENT` | Azure OpenAI. |
| `GUBERN_TELEGRAM_TOKEN`, `GUBERN_TELEGRAM_DEFAULT_CHAT` | Telegram bot. |
| `GUBERN_TELEGRAM_ALLOWED_IDS` | Comma-separated chat and user IDs allowed to command the bot. Empty allows nobody. The sender's own user ID must be listed for both messages and approval buttons; in a group chat, list the group ID **and** each member allowed to command the bot. |
| `GUBERN_TELEGRAM_SECRET` | Secret Telegram sends with each webhook update (`secret_token`); `/webhook/telegram` refuses updates without it. Unset: a random one is generated per start and registered when Gubern sets the webhook itself (`GUBERN_PUBLIC_URL`). Set it if you register the webhook yourself. |
| `GUBERN_WA_PHONE_ID`, `GUBERN_WA_TOKEN` | WhatsApp Cloud API (outbound messages and alerts). |
| `GUBERN_WA_APP_SECRET` | Verifies `X-Hub-Signature-256` on inbound messages. **Required for `/webhook/whatsapp` to be mounted**; without it, WhatsApp is outbound-only. |
| `GUBERN_WA_VERIFY_TOKEN` | Token for Meta's webhook verification handshake. An empty token never matches. |
| `GUBERN_WA_ALLOWED_NUMBERS` | Comma-separated sender numbers allowed to drive the agent (any formatting; digits are compared). Empty allows nobody: a valid signature only proves Meta delivered the message. |
| `GUBERN_SLACK_WEBHOOK` | Slack incoming webhook for outbound alerts. |
| `GUBERN_SLACK_SIGNING_SECRET` | Verifies slash-command requests. **Required for `/webhook/slack` to be mounted**; without it, Slack is outbound-only. |
| `GUBERN_STUDIO_WORKSPACE` | Enables Studio under `/studio/` (browser terminal, git panel, DB console, and code-server if installed) for this directory. Off when unset. Admin API keys only: the terminal is a shell that bypasses the approval gate. code-server listens on a Unix socket in a private directory (Windows: a random loopback port), never on a public or fixed port. |
| `GUBERN_STUDIO_DB_READONLY` | Reserved. The Studio DB console lists mTLS TCP routes but ships no database driver, so it runs no queries yet; when one is added, this defaults to read-only. |
| `GUBERN_SECRET_STORE`, `GUBERN_AUDIT_LOG`, `GUBERN_MTLS_STORE` | Relocate on-disk state. |

Environment variables always take precedence over the saved `gubern setup`
config.
