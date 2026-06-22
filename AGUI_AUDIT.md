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

## Confirmed gaps (feature present but unconnected)

5. **TODO — LEFT roster status not driven by live comms.** `HandleAGUI(CommsMsg)`
   appends to the feed but never updates agent statuses, so the roster only
   reflects the local submit/reply toggle (Coordinator + Code Agent), never the
   real active agent (tester/reviewer) from the bus.
6. **TODO — Checkpoint "Edit" (E) action unimplemented in the TUI.** The
   CheckpointManager supports `Edit`, and the API has no edit route either; the
   code view footer/handler only offers V/A/R/S. Edit path is dead from the UI.
7. **TODO — No streaming output.** `Submit` is request/response; coordinator
   replies appear all at once. (BUG 3.1 — deferred commit 4.)
8. **TODO — No thinking animation in the chat panel / input history.** Spinner
   shows in the input line only; no per-message animated indicator, no up/down
   history recall. (BUG 3.3/3.4 — deferred commit 5.)
9. **MINOR — Direct-chatting the coordinator 502s.** `/api/agents/coordinator/chat`
   → `DirectChatFor("coordinator")` returns nil (coordinator isn't a registered
   A2A specialist). Not reachable from the TUI (coordinator uses submit), but the
   API endpoint is misleading.

## Dead / test-only code (not runtime bugs, but rot)

10. **MINOR — `MessageBus.AgentMessages` is used only by its own test.** No
    production caller. Either expose it (per-agent history endpoint) or remove.

(Investigation continues; see commits for fixes.)
