---
name: verify
description: Build, launch, and drive a local Gubern server end-to-end to verify changes at their runtime surface (management API, /v1 OpenAI-compat, agents).
---

# Verifying Gubern changes end-to-end

## Build & launch

```bash
go build -o bin/gubern-verify.exe ./cmd/gubern   # bin/ is gitignored

# Run from a scratch dir with a minimal config (the default gubern.cue at the
# repo root is a proxy demo, not required):
cat > /tmp/verify-home/gubern.cue <<'EOF'
cluster: { name: "verify" }
tls: { provider: "internal", acme_email: "verify@example.com" }
EOF
cd /tmp/verify-home && GUBERN_OLLAMA_URL=http://127.0.0.1:11553 \
  <repo>/bin/gubern-verify.exe start --verbose
```

- Management API listens on **:9090**.
- `gubern check --config gubern.cue` validates config without starting.
- Config is CUE; `tls.acme_email` is required even for `provider: "internal"`.

## Fake AI provider

`GUBERN_OLLAMA_URL` is the easiest provider to fake: no API key, plain HTTP,
and its wire format is NDJSON on `POST /api/generate` (`stream:true` → one
JSON object per line `{"response":"...","done":false}`, final line
`{"done":true,"eval_count":N}`; `stream:false` → single object). A ~30-line
Python `http.server` suffices. Other env keys: `GUBERN_ANTHROPIC_KEY`,
`GUBERN_OPENAI_KEY`, etc. (endpoint overrides only via setup config, not env).

## Auth

Data-plane endpoints (`/api/agents/*`, `/v1/*`) always need an API key:

- The server auto-creates a TUI key on first start and writes its plaintext to
  `os.UserConfigDir()/gubern/tui-key` (Windows: `%APPDATA%\gubern\tui-key`).
  Send it as `Authorization: Bearer <key>`.
- **Gotcha:** state is per-user, not per-cwd — `%LOCALAPPDATA%\gubern\`
  (key store, audit log, memory). If `apikeys.json` is already populated from
  an earlier run, no new tui-key is written; reuse the existing tui-key file.
- Loopback trust covers control-plane reads only, NOT `POST /api/keys`.

## Useful surfaces

- `POST /v1/chat/completions` (`stream:true` → SSE; timestamp chunk arrivals
  to distinguish true token streaming from compute-then-chunk).
- `POST /api/agents/submit` (`Accept: text/event-stream` → SSE chunks).
- `GET /health` is public; `GET /ready` aggregates subsystem readiness.
