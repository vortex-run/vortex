---
name: verify
description: Build, launch, and drive a local VORTEX server end-to-end to verify changes at their runtime surface (management API, /v1 OpenAI-compat, agents).
---

# Verifying VORTEX changes end-to-end

## Build & launch

```bash
go build -o bin/vortex-verify.exe ./cmd/vortex   # bin/ is gitignored

# Run from a scratch dir with a minimal config (the default vortex.cue at the
# repo root is a proxy demo, not required):
cat > /tmp/verify-home/vortex.cue <<'EOF'
cluster: { name: "verify" }
tls: { provider: "internal", acme_email: "verify@example.com" }
EOF
cd /tmp/verify-home && VORTEX_OLLAMA_URL=http://127.0.0.1:11553 \
  <repo>/bin/vortex-verify.exe start --verbose
```

- Management API listens on **:9090**.
- `vortex check --config vortex.cue` validates config without starting.
- Config is CUE; `tls.acme_email` is required even for `provider: "internal"`.

## Fake AI provider

`VORTEX_OLLAMA_URL` is the easiest provider to fake: no API key, plain HTTP,
and its wire format is NDJSON on `POST /api/generate` (`stream:true` → one
JSON object per line `{"response":"...","done":false}`, final line
`{"done":true,"eval_count":N}`; `stream:false` → single object). A ~30-line
Python `http.server` suffices. Other env keys: `VORTEX_ANTHROPIC_KEY`,
`VORTEX_OPENAI_KEY`, etc. (endpoint overrides only via setup config, not env).

## Auth

Data-plane endpoints (`/api/agents/*`, `/v1/*`) always need an API key:

- The server auto-creates a TUI key on first start and writes its plaintext to
  `os.UserConfigDir()/vortex/tui-key` (Windows: `%APPDATA%\vortex\tui-key`).
  Send it as `Authorization: Bearer <key>`.
- **Gotcha:** state is per-user, not per-cwd — `%LOCALAPPDATA%\vortex\`
  (key store, audit log, memory). If `apikeys.json` is already populated from
  an earlier run, no new tui-key is written; reuse the existing tui-key file.
- Loopback trust covers control-plane reads only, NOT `POST /api/keys`.

## Useful surfaces

- `POST /v1/chat/completions` (`stream:true` → SSE; timestamp chunk arrivals
  to distinguish true token streaming from compute-then-chunk).
- `POST /api/agents/submit` (`Accept: text/event-stream` → SSE chunks).
- `GET /health` is public; `GET /ready` aggregates subsystem readiness.
