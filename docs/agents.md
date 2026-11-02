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
rather than stranding silently. Tasks may set `max_attempts` (capped at 3) for
bounded retries on executor failure, and optional `verification_criteria` to
check executor output after each attempt. **Partial:** only structured criteria
are enforced today (`nonempty`, `contains:…`, `prefix:…`). Free-text criteria
are rejected when the plan is validated (the orchestration fails fast with
`unsupported verification_criteria` instead of running); as defence in depth, a
persisted task that still carries unsupported criteria fails at run time as
`unsupported`. There is no shell/test harness auto-verification yet.

Capability discovery uses the A2A agent catalog (`GET /api/agents/catalog`,
API-key gated like the other agent endpoints): validated static manifests with
capability-based selection and bounded delegation contracts (depth/fan-out
limits). A planner task may set `required_capabilities` (plus optional
`permission_profile`) *instead of* `agent`; setting both is a plan error. The
catalog is registry data only — listing an agent does not mean it is running.
Live specialist status comes from `GET /api/team/agents`; both are shown on the
dashboard **Agents** page.

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

### Local workspace trust (Auto Mode foundations)

Configure in `vortex.cue` under `agents.local_tools` (defaults: empty
`trusted_root`, `auto_mode: false`):

```cue
agents: {
	local_tools: {
		trusted_root: "/path/to/your/project" // or a path relative to the work dir
		auto_mode:    true                    // requires trusted_root
	}
}
```

Environment overrides: `VORTEX_AGENTS_LOCAL_TOOLS_TRUSTED_ROOT`,
`VORTEX_AGENTS_LOCAL_TOOLS_AUTO_MODE`. Invalid or missing trusted directories
fail at startup with a clear field path. When both the coordinator working
directory and `trusted_root` are set, startup requires **work dir ⊆ trusted_root**
(lexical containment). Prefer `trusted_root` equal to the project root (or `.`
relative to that root), or an absolute ancestor that contains the work dir.
A `trusted_root` that is only a subdirectory of the work dir is rejected so the
shell cwd is not left outside the FS trust boundary.

| Field | Meaning |
|---|---|
| `trusted_root` | When set, all local FS tool paths must resolve inside this directory (lexical + symlink checks). Empty = no workspace confinement (approval remains the control). |
| `auto_mode` | With a `trusted_root`, routine in-scope write/edit/create_project may skip repeated approval prompts. Shell (`run_terminal`) stays approval-gated. Release builds still force shell approval. |

Auto Mode keeps the approval prompt for `vortex.cue` and for paths inside
`.git`, `.githooks` and `.husky`, because hooks would otherwise run on a later
approved command. A custom `core.hooksPath` is not detected.

Auto Mode is **not** unrestricted host access. Out-of-scope paths are denied by
the runtime. There is still no container/namespace isolation for local tools.

### Hot reload (`SIGHUP` / `vortex reload`)

Config reload swaps the live `vortex.cue` in the config `Holder` (same path as
routes and policy). **`agents.local_tools` is validated at process startup only:**
`RegisterLocalTools` binds `trusted_root` and `auto_mode` into the coordinator’s
local tool registry once; there is no reload hook to rebuild that registry or the
specialist trusted-tool set. In-flight agent work may still be executing tools
built from the previous settings.

To change `trusted_root` or `auto_mode`, edit `vortex.cue` and **restart VORTEX**
(`vortex stop` then `vortex start`, or your service manager). Do not rely on
reload to tighten or widen workspace trust on a running process.
