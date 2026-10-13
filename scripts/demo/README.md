# Demo environment

A self-contained VORTEX deployment for screenshots, screen recordings, and
manual exploration. Isolated from your real installation.

```powershell
pwsh scripts/demo/demo.ps1
```

Ctrl+C stops everything.

## Why this exists

Recording a demo against a normal install produces unusable footage:

- **The template config has no working backends.** `vortex.cue` points at
  `10.0.0.2` and `myapp.com`, which resolve to nothing, so every health check
  renders red and the Routes view is a wall of failures.
- **Routes without an explicit `listen` bind to `:0`.** See `listenAddr` in
  `internal/proxy/manager.go` — the port is chosen at random, so there is no URL
  to type on camera.
- **A real install has real state on screen.** Your audit log, your secrets,
  your API keys, and your provider tokens.

`demo.cue` fixes the first two. `demo.ps1` fixes the third by redirecting every
persistent store into a throwaway directory.

## What it starts

| Component | Where | Notes |
|---|---|---|
| `frontend` route | `https://127.0.0.1:8443` | Host `demo.localhost`, health checked |
| `api` route | `https://127.0.0.1:8444` | Host `api.demo.localhost`, rate limited 600rpm |
| `staging` route | `http://127.0.0.1:8080` | Host `staging.localhost` |
| `postgres` route | `tcp://127.0.0.1:15432` | L4, proves non-HTTP proxying |
| Management API | `http://127.0.0.1:9090` | Dashboard at `/dashboard/` |
| Demo upstreams | `127.0.0.1:3000/4000/4100`, tcp `15433` | `scripts/demo/backends` |

TLS uses the internal (self-signed) CA, so the demo runs offline and never
touches ACME rate limits on a throwaway hostname.

## Isolation

Every store is redirected via environment overrides, so nothing touches
`%LOCALAPPDATA%\vortex`:

`VORTEX_AUDIT_LOG`, `VORTEX_APIKEY_STORE`, `VORTEX_SECRET_STORE`,
`VORTEX_PLUGIN_DIR`, `VORTEX_NAMESPACE_STORE`, `VORTEX_MTLS_STORE`

Reset by deleting the data directory (default `%TEMP%\vortex-demo`).

Two stores have no override and still land in the real data directory: the
agent sandbox base and the SQLite memory databases (`conversations.db`,
`skills.db`, `episodes.db`). They are not shown in the boot output or the
default views, so they do not appear on camera — but they are not isolated, and
adding overrides for them would make this harness complete.

## Flags

| Flag | Effect |
|---|---|
| `-DataDir <path>` | Where isolated state lives. Default `%TEMP%\vortex-demo`. |
| `-Keep` | Reuse existing state instead of starting clean. Good for a second take. |
| `-NoTraffic` | Skip the background traffic generator (metrics and logs stay empty). |

## Before recording

1. **Allow the firewall rule once, off camera.** HTTP routes bind `:port`
   (wildcard), which is what raises the Windows prompt. Start the demo once,
   click Allow, then stop and restart it for the take. Installing VORTEX as a
   service adds a persistent rule instead (`internal/service/firewall_windows_rule.go`).
2. **Check the log for anything you would not want published** before you
   publish the video: `Get-Content $env:TEMP\vortex-demo\vortex.log`.
3. Terminal at ~100x30, font 16pt or larger, dark high-contrast theme.
