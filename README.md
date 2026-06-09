# VORTEX

**One binary. Any server. Fully autonomous.**

VORTEX is a self-hosted platform you run on any VPS or server. It handles your
reverse proxy, TLS, secrets, clustering, metrics, and a built-in AI agent that
can read, write, and run things on your machine — and build apps for you — all
controlled from a terminal dashboard or from Telegram on your phone.

---

## Install

### Download a release

Grab the single binary for your platform from the
[Releases](https://github.com/vortex-run/vortex/releases) page, then make it
executable and put it on your `PATH`:

```bash
chmod +x vortex
sudo mv vortex /usr/local/bin/
```

### Verify it works

```bash
vortex version
vortex --help
```

VORTEX is a single static binary — there is nothing else to install.

---

## Quick start

### 1. Run first-time setup

```bash
vortex setup
```

This interactive wizard walks you through:

- **Choosing an AI provider** — Anthropic Claude, DeepSeek, OpenAI, Google
  Gemini, or local **Ollama** (free). It verifies your API key live.
- **Optional Telegram** — connect a bot so you can control VORTEX from your
  phone.
- **Your API key** — VORTEX prints a key once. Save it; you'll use it for the
  dashboard and API.

Everything you enter is stored locally (the API key is encrypted at rest).

### 2. Start the server

```bash
vortex start
```

The management server listens on **`http://localhost:9090`** by default. Leave
this running — VORTEX is meant to run 24/7.

To check it's up from another terminal:

```bash
vortex status
```

### 3. Open the dashboard

```bash
vortex ui
```

This opens the full-screen **terminal dashboard**: an overview screen, live
logs, metrics, routes, security, secrets, and an interactive **agent chat**.

Prefer the web view? Open <http://localhost:9090/dashboard/> in your browser.

To start the server **and** open the dashboard in one step:

```bash
vortex ui --start
```

---

## Using the agent

Inside the dashboard's **Agents** screen (or over Telegram), just talk to it.

### Files and commands

Read-only actions run instantly. Anything that writes or runs asks for your
approval first (press **Y** then **Enter** to approve, **N** to reject).

| You type | What happens |
|---|---|
| `/ls` | List files in the working directory |
| `/read main.go` | Show a file's contents |
| `/search func main` | Search file contents |
| `/find *.go` | Find files by name |
| `/run python calc.py` | Run a command (asks approval, streams output) |
| `create a file and save it to /path/file.py` | Generates the code, shows a preview, asks approval, writes it |
| `/edit notes.txt` | Edit a file |
| `/undo` | Restore the last file you wrote (asks approval) |
| `git status` · `/diff` · `/commit fix the bug` | Git operations (commits ask approval) |

### Building apps

Describe what you want and VORTEX builds it:

```
build me a flutter calculator app
```

If it needs to know more, it asks a couple of quick questions with **numbered
options** — type the numbers (e.g. `2 1`) or describe what you want in your own
words. Then it generates, builds, and delivers the result.

### Conversation history

- `/history` — list your past sessions
- `/resume <session-id>` — reload a past conversation

### Ask anything

```
what is 2 + 2
```

The agent answers directly using your configured provider.

---

## Controlling VORTEX from Telegram

If you connected Telegram during `vortex setup`, you can drive VORTEX from your
phone.

For local use (no public URL needed), start VORTEX in polling mode:

```bash
VORTEX_TELEGRAM_POLLING=true vortex start
```

Then message your bot:

| Command | Does |
|---|---|
| `/status` | Show VORTEX status |
| `/routes` | List active routes |
| `/ls` | List files in the working directory |
| `/build <description>` | Start a build |
| `/cost` | Show today's AI usage cost |
| `/approve` · `/reject` | Approve or reject a pending action |
| `/help` | Show all commands |

When the agent needs approval or wants to ask a question, you'll get **tap
buttons** right in the chat. Plain messages are sent to the agent as requests.

---

## Configuration

VORTEX reads its config from a `vortex.cue` file. Validate it before starting:

```bash
vortex check
vortex start --config /path/to/vortex.cue
```

Reload config without restarting:

```bash
vortex reload
```

### Secrets

```bash
vortex secret set db_password
vortex secret list
```

Secrets are encrypted at rest and injected into your routes — they never appear
in the config file or logs.

---

## Common commands

| Command | Description |
|---|---|
| `vortex setup` | Interactive first-run setup |
| `vortex start` | Start the server |
| `vortex stop` | Stop the running server |
| `vortex status` | Show status of the running server |
| `vortex ui` | Open the terminal dashboard |
| `vortex reload` | Reload configuration without restarting |
| `vortex check` | Validate config without starting |
| `vortex secret` | Manage secrets (encrypted at rest) |
| `vortex cluster` | Inspect and manage cluster membership |
| `vortex namespace` | Manage tenant namespaces and quotas |
| `vortex plugin` | Manage plugins |
| `vortex audit` | Verify and export the audit log |
| `vortex tune` | Inspect/apply OS tuning and run benchmarks |
| `vortex service` | Manage VORTEX as a system service |
| `vortex self-update` | Update to the latest release |
| `vortex version` | Print version information |

Run any command with `--help` for its full options.

---

## API quick reference

All API endpoints are served from the management address (default
`localhost:9090`) and require your API key via the `X-API-Key` header.

```bash
KEY="<your-api-key>"

# Health (no key needed)
curl http://localhost:9090/health

# Status, AI cost, agent status
curl -H "X-API-Key: $KEY" http://localhost:9090/api/status
curl -H "X-API-Key: $KEY" http://localhost:9090/api/ai/cost
curl -H "X-API-Key: $KEY" http://localhost:9090/api/agents/status

# Prometheus metrics
curl http://localhost:9090/metrics
```

---

## Running as a service

To keep VORTEX running across reboots:

```bash
vortex service install
vortex service status
```

---

## Updating

```bash
vortex self-update
```

---

## License

See the [LICENSE](LICENSE) file.
