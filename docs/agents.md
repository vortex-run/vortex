# Agents

VORTEX runs an AI **coordinator** that classifies your message and dispatches
it to the right capability: code/file work, app builds, research, devops,
data pipelines, or multi-agent orchestration. You talk to it from the TUI
(`vortex ui`), the web dashboard, Telegram, or the HTTP API.

## AI providers

Configure one or more via `vortex setup` or environment variables. The gateway
tries providers in priority order and falls back on failure.

| Provider | Env | Notes |
|---|---|---|
| Anthropic Claude | `VORTEX_ANTHROPIC_KEY` | Best reasoning |
| DeepSeek | `VORTEX_DEEPSEEK_KEY` | Cheap, OpenAI-compatible |
| OpenAI | `VORTEX_OPENAI_KEY` | GPT-4o family |
| Google Gemini | `VORTEX_GEMINI_KEY` | Gemini 1.5 |
| Groq | `VORTEX_GROQ_KEY` | Very fast, free tier |
| AWS Bedrock | `VORTEX_BEDROCK_REGION` + AWS creds | SigV4-signed |
| Azure OpenAI | `VORTEX_AZURE_OPENAI_KEY/_ENDPOINT/_DEPLOYMENT` | |
| OpenRouter | `VORTEX_OPENROUTER_KEY` | 75+ models |
| Ollama | `VORTEX_OLLAMA_URL` | Local, free |

A daily USD budget can cap spend; usage is visible at `/api/ai/cost`.

## Capabilities

### Code & files

The agent has tools for reading/writing files, running whitelisted commands,
HTTP fetches (SSRF-protected), and **LSP diagnostics** (`lsp_diagnostics`),
which surface errors/warnings from a language server (gopls, pylsp,
typescript-language-server, rust-analyzer) when installed. Filesystem and
terminal actions are **approval-gated** — the agent asks before touching your
machine.

> _"Read main.go, find the bug in the retry loop, and fix it."_

### VORTEX Forge — autonomous app builds

Forge builds complete apps (and Android APKs) from a prompt, in a sandbox, and
delivers the artifact. Requires an AI provider.

> _"Build a Flutter expense tracker with charts and send me the APK."_

### Research agent

Web search + page fetch + summarize into a saved report. The fetcher pins DNS
resolution and re-validates redirects to prevent SSRF.

> _"Research current best practices for Postgres connection pooling."_

### DevOps agent

SSH into your servers, run Docker/Nginx operations, and report back.

> _"Restart the api container on web-2 and confirm it's healthy."_

### Data pipelines

Analyse CSV/JSON and generate charts.

> _"Summarise orders.csv and chart revenue per region."_

### Multi-agent orchestration

The orchestrator decomposes a goal into a task DAG and runs tasks with
dependency + concurrency control. Unknown or cyclic dependencies fail fast
rather than stranding silently.

> _"Research the top 2 caching libraries, benchmark both, and write up which to use."_

## Conversation memory

Conversations persist in a SQLite database with full-text search. Each session
has an ID; the coordinator includes recent turns as context. Legacy JSON
conversations are migrated automatically on first run.

- List sessions: `GET /api/agents/history`
- One session: `GET /api/agents/history/{id}`
- Submit (SSE streaming with `Accept: text/event-stream`): `POST /api/agents/submit`

`session_id` is validated (`[A-Za-z0-9_.-]{1,64}`); omit it to have the server
generate one.

## Approval gate

Tools that touch the real machine require explicit approval (TUI `[Y]/[N]`,
Telegram reply, or `POST /api/agents/approve`). This is the primary safety
control for unattended runs — keep it on in production.
