# Troubleshooting

Common problems and their fixes. For recovering from crashes, failed audit
verification, master-key problems and self-update failures, see
[runbooks.md](runbooks.md).

Start with:

```bash
vortex status                      # is it running, which config, which version
vortex check --config vortex.cue   # does the config parse and validate
vortex start --verbose             # foreground, with debug logs
curl -s http://127.0.0.1:9090/ready
```

## Where VORTEX keeps its state

State is per user, not per directory. Two locations:

| Kind | Linux | macOS | Windows |
|---|---|---|---|
| Config (`master.key`, `tui-key`, `ai-provider.json`) | `~/.config/vortex` | `~/Library/Application Support/vortex` | `%APPDATA%\vortex` |
| Data (`apikeys.json`, `audit.log`, `secrets/`, `keyslots.db`, `plugins/`, `memory/*.db`) | `~/.cache/vortex` | `~/Library/Caches/vortex` | `%LOCALAPPDATA%\vortex` |

For the Linux service, `~` is `/var/lib/vortex` (the `vortex` user's home).

**Back up both, together.** Secrets and the audit log are keyed by
`master.key`. Restoring one location without the other, or running as a
different user (or with a different `APPDATA`), leaves VORTEX unable to read
its secrets and reports the audit chain as broken. Don't let a cache cleaner
delete the data directory.

## The server won't start

**`address already in use`.** Another process holds the port: 9090 for the
management API, or a route's `listen` port. Find it with `ss -ltnp` (Linux),
`lsof -i :9090` (macOS) or `netstat -ano | findstr :9090` (Windows). The API
address can be changed with `VORTEX_API_LISTEN` (for example
`127.0.0.1:9091`).

**`permission denied` binding :80 or :443 on Linux.** Unprivileged processes
can't bind ports below 1024. `vortex service install` grants the service
`CAP_NET_BIND_SERVICE`. When running by hand, either use ports ≥ 1024 or grant
the binary the capability: `sudo setcap cap_net_bind_service=+ep $(command -v vortex)`.

**The systemd service fails with `217/USER`.** The `vortex` user doesn't
exist. Re-run `sudo vortex service install`, which creates it, or create it
yourself with home `/var/lib/vortex`.

**`can't find a private config directory`.** VORTEX keeps its master key,
API keys and secrets in your user config and cache directories, found from
`HOME` (Linux, macOS, BSD; or `XDG_CONFIG_HOME`/`XDG_CACHE_HOME`) or
`APPDATA`/`LOCALAPPDATA` (Windows). Some service managers and containers start
programs with none of these set. Set `HOME` to a directory only the VORTEX
user can write; VORTEX won't fall back to the shared temp directory.

**A route's port is taken during `vortex reload`.** The reload is refused, the
previous routes keep running, and `vortex reload` prints the bind error.

**Config errors.** `vortex check` prints the CUE error with a line number.
`tls.acme_email` is required even with `provider: "internal"`.

## Windows Firewall keeps asking about vortex.exe

Windows asks once per program that listens on a non-loopback address. To
keep the management API local only, set `VORTEX_API_LISTEN=127.0.0.1:9090`.
If VORTEX should be reachable from other machines, allow it once
(`vortex service install` on Windows can add the rule). Clicking **Cancel**
blocks other machines but doesn't stop VORTEX. Local use keeps working.

## Certificates (ACME / Let's Encrypt)

Let's Encrypt (the default `tls.provider`) proves domain ownership over
HTTP-01: it connects to `http://<your-domain>/.well-known/acme-challenge/` on
**port 80**. That needs:

- DNS for the route's `host` pointing at this server,
- port 80 open in the firewall and in your cloud provider's security group,
- VORTEX able to bind :80 (see above).

For local testing use `tls: { provider: "internal" }` to get certificates
from VORTEX's own CA.

## Requests are rejected with 401

- `/api/agents/*`, `/v1/*` and most `/api/*` routes always need an API key,
  even from localhost: `Authorization: Bearer <key>` or `X-API-Key: <key>`.
- The TUI's key is written to the config directory as `tui-key` on first
  start. If `apikeys.json` already existed, no new `tui-key` is written; use
  the existing one or issue a key with `POST /api/keys` (admin).
- `/internal/*`, `/metrics` and `/api/status` accept loopback without a key
  **unless** `VORTEX_TRUST_LOOPBACK=false`. Behind a reverse proxy on the same
  host, set it to `false`, since every proxied request looks like loopback.

## Messaging

**`/webhook/whatsapp` or `/webhook/slack` returns 404.** The inbound route
is only mounted when its secret is set: `VORTEX_WA_APP_SECRET` for WhatsApp,
`VORTEX_SLACK_SIGNING_SECRET` for Slack. The startup log says so. Outbound
alerts work without it.

**WhatsApp messages get no reply.** Only senders listed in
`VORTEX_WA_ALLOWED_NUMBERS` reach the agent; an empty list allows nobody.
Unlisted senders are ignored without a reply. Any formatting works
(`+1 (555) 010-0000`); digits are compared.

**Telegram doesn't answer.** Only chat IDs in `VORTEX_TELEGRAM_ALLOWED_IDS`
are served; an empty list allows nobody. See [telegram.md](telegram.md).

## The agent says "action rejected by user" (but nobody rejected it)

Shell commands need someone to approve them. Approval requests go to the
Telegram chat in `VORTEX_TELEGRAM_DEFAULT_CHAT` (with `VORTEX_TELEGRAM_TOKEN`
set). Without an approver configured, approval-gated actions are refused
with this message. Local file and terminal tools started from the TUI wait
for approval in the TUI instead.

## Agent team (`--team`) errors

`/a2a/` is only for the server's own agents and returns 401 to anything
else. That's expected. Use the `/api/agents/...` routes (with an API key) to
inspect the team.

## Tests on Windows

- Run Go commands with `TMP`/`TEMP`/`GOTMPDIR` on a drive with free space;
  test binaries and fuzz caches are large.
- Cluster tests bind UDP on loopback. Windows reserves dynamic UDP port
  ranges (`netsh int ipv4 show excludedportrange protocol=udp`); the tests
  pick ports outside them.
- There is no `-race` without a C compiler; CI runs the race detector on
  Linux.
