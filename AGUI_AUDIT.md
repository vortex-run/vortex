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

## Remaining (lower impact — not yet done)

A. **Checkpoint "Edit" (E) action unimplemented in the TUI/API.** The
   `CheckpointManager` supports `Edit`, but there is no `POST /api/checkpoints/{id}/edit`
   route and the code view only offers V/A/R/S. Edit path is dead from the UI.
B. **Collapsible tool results (BUG 3.2) not built.** Tool calls (write_file, etc.)
   are not shown as collapsible `▶ write_file calc.py [expand]` rows.
C. **True per-token streaming.** We stream *events* (task/result), not AI tokens.
   Real token streaming needs `CompleteStream` plumbed through every provider in
   the AI gateway — a large, provider-by-provider change.
D. **MINOR — Direct-chatting the coordinator 502s.** `/api/agents/coordinator/chat`
   → `DirectChatFor("coordinator")` is nil (coordinator isn't a registered A2A
   specialist). Not reachable from the TUI; the endpoint is just misleading.
E. **MINOR — `MessageBus.AgentMessages` is test-only.** No production caller;
   expose it (per-agent history endpoint) or remove.

All FIXED items are committed, tested, and CI-green on `8fa868c`.
