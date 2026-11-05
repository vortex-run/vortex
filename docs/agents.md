# Agents

Gubern runs an AI **coordinator** that classifies your message and dispatches
it to the right capability: code/file work, app builds, research, devops,
data pipelines, or multi-agent orchestration. You talk to it from the TUI
(`gubern ui`), the web dashboard, Telegram, or the HTTP API.

## AI providers

Configure one or more via `gubern setup` or environment variables. The gateway
tries providers in priority order and falls back on failure.

| Provider | Env | Notes |
|---|---|---|
| Anthropic Claude | `GUBERN_ANTHROPIC_KEY` | Best reasoning |
| DeepSeek | `GUBERN_DEEPSEEK_KEY` | Cheap, OpenAI-compatible |
| OpenAI | `GUBERN_OPENAI_KEY` | GPT-4o family |
| Google Gemini | `GUBERN_GEMINI_KEY` | Gemini 1.5 |
| Groq | `GUBERN_GROQ_KEY` | Very fast, free tier |
| AWS Bedrock | `GUBERN_BEDROCK_REGION` + AWS creds | SigV4-signed |
| Azure OpenAI | `GUBERN_AZURE_OPENAI_KEY/_ENDPOINT/_DEPLOYMENT` | |
| OpenRouter | `GUBERN_OPENROUTER_KEY` | 75+ models |
| Ollama | `GUBERN_OLLAMA_URL` | Local, free |

A daily USD budget can cap spend; usage is visible at `/api/ai/cost`.

## Capabilities

### Code & files

The agent has tools for listing, reading, searching, writing and editing files,
running terminal commands, git status/diff/add/commit, and undoing its last
write. Writes and terminal commands are **approval-gated**: the agent asks
before touching your machine. Tools are chosen from your request (commands
and keywords), not by the model itself, and the model doesn't see tool output.

Not reachable from chat yet: an `http_get` tool (web fetching happens inside
the research agent) and **LSP diagnostics** (`lsp_diagnostics`). Both exist in
the code but nothing routes requests to them.

### Model-driven runs: `/agent <task>` (new)

`/agent fix the failing test in internal/foo` hands the task to the model
with the same local tools: it chooses which tools to call, sees their
results, and keeps going until it answers. Every write, command and commit
still goes through the approval gate (Telegram if configured, otherwise the
TUI/API approve call; pending approvals expire after 15 minutes), and each
decision is in the audit log. A run stops after 20 model turns or about two
minutes, or at 250k tokens. Works with every built-in provider (Claude,
OpenAI, DeepSeek, Groq, OpenRouter, Azure OpenAI, Gemini, Bedrock via the
Converse API, Ollama). Models without native tool calling (many small
local models) fall back to a text protocol: the model asks for a tool in a
JSON block. It is less reliable, and every call still goes through the same
checks and approvals. Without
`/agent`, requests use the existing command and keyword routing.

> _"Read main.go, find the bug in the retry loop, and fix it."_

Give a run a completion check to make "done" mean something:

```
/agent --check "go test ./internal/foo/..." fix the failing test
```

When the model says it is finished, Gubern runs the check (through the
approval gate, like any command). If it fails, the output goes back to the
model, up to three times. The run ends with "✓ Verified" or "✗ Not verified";
without a check, the answer is only the model's claim.

Not every model can drive this loop. `gubern ai probe` sends each configured
provider a few small requests and checks that it calls a tool, uses typed
arguments, reads a tool result back and ignores an instruction planted in a
result; nothing is executed. It exits non-zero if any check fails, and
`--provider ollama` limits it to one provider. Each provider costs a few
short model calls.

#### Skills

`/agent` reads [Agent Skills](https://agentskills.io) — `SKILL.md` folders
written for Claude Code, Codex, OpenCode and similar tools — from the
workspace's `.agents/skills/`, `.claude/skills/` and `.opencode/skills/`
directories. Only each skill's name and description are given to the model,
in its first message and marked as text from the repository (never in the
system prompt); it reads the full `SKILL.md` with `read_file` when a task
calls for it. Skills are instructions, not permissions: they can't run anything on
load, `allowed-tools` is ignored for now, and every action a skill suggests
still goes through the approval gate. Invalid skills (bad name, name not
matching the folder, missing description, unsupported YAML such as `[a, b]`
lists, symlinked files, files over 256 KiB) are skipped with a warning in the
log. Skills are re-read on every run. `gubern skills list` (or `--dir <workspace>`)
shows which ones load and why any are skipped.

### Gubern Forge — autonomous app builds

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
control for unattended runs — keep it on in production. Approvals can be
bound to the exact action shown (`action_id`, see [api.md](api.md)) and
expire after 15 minutes.

### Local workspace trust (Auto Mode foundations)

Configure in `gubern.cue` under `agents.local_tools` (defaults: empty
`trusted_root`, `auto_mode: false`):

```cue
agents: {
	local_tools: {
		trusted_root: "/path/to/your/project" // or a path relative to the work dir
		auto_mode:    true                    // requires trusted_root
	}
}
```

Environment overrides: `GUBERN_AGENTS_LOCAL_TOOLS_TRUSTED_ROOT`,
`GUBERN_AGENTS_LOCAL_TOOLS_AUTO_MODE`. Invalid or missing trusted directories
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

Auto Mode keeps the approval prompt for `gubern.cue` and for paths inside
`.git`, `.githooks` and `.husky`, because hooks would otherwise run on a later
approved command. A custom `core.hooksPath` is not detected.

Auto Mode is **not** unrestricted host access. Out-of-scope paths are denied by
the runtime. There is still no container/namespace isolation for local tools.

### Hot reload (`SIGHUP` / `gubern reload`)

Config reload swaps the live `gubern.cue` in the config `Holder` (same path as
routes and policy). **`agents.local_tools` is validated at process startup only:**
`RegisterLocalTools` binds `trusted_root` and `auto_mode` into the coordinator’s
local tool registry once; there is no reload hook to rebuild that registry or the
specialist trusted-tool set. In-flight agent work may still be executing tools
built from the previous settings.

To change `trusted_root` or `auto_mode`, edit `gubern.cue` and **restart Gubern**
(`gubern stop` then `gubern start`, or your service manager). Do not rely on
reload to tighten or widen workspace trust on a running process.
