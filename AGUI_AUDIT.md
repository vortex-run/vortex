# VORTEX `vortex code` / AG-UI — Wiring Audit

Senior review of "built but not connected" defects. Status legend:
**FIXED** (this session) · **TODO** (confirmed gap, fix pending) · **MINOR** (cosmetic / low impact) · **OK** (verified working).

## Critical data-path bugs (make the UI non-functional)

1. **FIXED — User message invisible in CHAT panel.** Coordinator submit wrote
   only to the activity feed; team mode renders the chat panel. Now mirrored.
   (`4a31acc`)
2. **FIXED — Coordinator reply invisible in CHAT panel.** `codeReplyMsg` only
   fed `ingestReply` (feed). Now also appended as an agent chat line. (`d1df10c`)
3. **FIXED — AGENT COMMS panel never receives live data.** No bus subscription
   in the TUI. Added `client.StreamComms` (SSE) + a re-arming listen loop.
   (`32ff3ca`)
4. **FIXED — Team pipeline never published to the bus.** `MessageBus` was wired
   into `TeamConfig` but `Execute` never called `Publish`, so even with the
   stream connected the comms panel stayed empty (only checkpoint events showed).
   Now publishes task/progress/result. (`58d7ae4`)

## Coordinator correctness (the second cluster — all fixed)

5. **FIXED — Coordinator leaked skill/tool/step internals + task metrics + goal
   echo.** Root: the TUI sent `/orchestrate`, routing to the orchestration engine
   (which appended the skill block to the goal) instead of the team. Rewrote the
   assistant prompt with no-leak rules + `filterCoordinatorResponse` net.
   (`c9d2421`)
6. **FIXED — Coordinator never called the agent team; "2" treated as a new
   task.** The `/orchestrate` prefix bypassed the team. TUI now sends `/team`,
   coordinator force-routes it to `handleTeamTask` (coder→tester→reviewer).
   (`11c22ca`)
7. **FIXED — Streaming output.** Live task/result bus events stream into the chat
   panel as the team works. (`2832b2f`)
8. **FIXED — Thinking animation + input history.** Animated indicator in the chat
   panel; ↑/↓ history recall. (`479a260`)
9. **FIXED — LEFT roster status from live comms.** task→busy, result→ready.
   (`e8cc49c`)
10. **FIXED — Arrow-key option selector.** QUESTION/OPTIONS parsed into an
    interactive ↑↓ menu. (`1e0f2db`)
11. **FIXED — Code Agent files landed in the server cwd, not the project dir.**
    `resolveWorkingDir` now honours `VORTEX_WORK_DIR`. Live-verified: real
    `calc.py` written to disk. (`8fa868c`)

## Remaining (lower impact)

A. **FIXED — Checkpoint "Edit" (E) action unimplemented in the TUI/API.** Added
   `POST /api/checkpoints/{id}/edit` (+ provider `Edit` + client `EditCheckpoint`);
   E opens an inline editor in the checkpoint review, Ctrl+S saves and resolves
   the checkpoint as edited so the pipeline continues. A/R now hit the real
   approve/reject endpoints too. (`6c7f7c0`)
B. **FIXED — Collapsible tool results (BUG 3.2) not built.** Team tool executor
   publishes `tool_result` bus messages; the chat panel renders them as
   collapsible `▶ write_file calc.py` rows (Enter toggles, ▼ expands to a
   line-numbered body). (`26aab99`)
C. **FIXED — True per-token streaming.** End to end:
   - *Gateway:* `CompleteStreamForModel`/`CompleteStream` stream natively from
     claude / openai / deepseek / groq / azure-openai / openrouter (SSE),
     ollama (NDJSON), and gemini (SSE), with a buffered single-delta fallback
     for bedrock. Provider/slot failover applies only until the first delta.
   - *API:* `POST /v1/chat/completions` (`stream:true`) and
     `POST /api/agents/submit` (`Accept: text/event-stream`) forward real
     deltas as they arrive. Coordinator replies stream at line granularity
     through a state machine shared with `filterCoordinatorResponse`, so
     internal artifacts never reach the stream (verified live: an injected
     `Goal:` line was stripped mid-stream).
   - *TUI:* `Client.SubmitStream` consumes the SSE chunks; the `vortex code`
     chat panel renders the reply as a live growing line with the spinner as
     its cursor, replacing the static "thinking..." wait.
   (The legacy dashboard agents tab still uses buffered `Submit` — cosmetic,
   it renders replies whole.)
D. **FIXED — Direct-chatting the coordinator 502s.** `teamCollab.Chat` now routes
   `agentID=="coordinator"` to the coordinator's `HandleMessage` entry point
   (own system prompt + response filtering) instead of the nil A2A
   `DirectChatFor`. (`5073f15`)
E. **FIXED — `MessageBus.AgentMessages` was test-only.** Now exposed via
   `GET /api/agents/team/{id}/messages` (auth-gated), returning the per-agent
   slice of the comms feed for dashboard/TUI drill-down. Wired through the
   `CommsProvider` interface + `teamCollab` adapter over `*a2a.MessageBus`.

All items are FIXED, committed, and tested.
