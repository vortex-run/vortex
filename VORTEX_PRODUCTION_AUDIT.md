# VORTEX — Independent Production Readiness Audit

---

## Re-audit addendum — 2026-07-03 (full-repo re-scan)

**Scope:** A second full-repo pass covering everything that changed since the original 2026-06-10 audit (142 commits, +30k/-671 lines, including the entire M19/M20 tranche: SQLite conversation store, LSP client, new AI providers, signed releases, per-key rate limiting, security-headers middleware) plus a re-verification of every original finding's remediation. Build, `go vet`, and the full `go test ./...` suite are green.

**Original findings — remediation verified in-tree:**
- **C1** (keys from cluster name) → fixed: `internal/keyring` derives every at-rest key from a random 32-byte master key via HKDF-SHA256 per-purpose labels; `/health` no longer emits the cluster name; legacy migration present.
- **H1** (session_id path traversal) → fixed: `validSessionID` regex at the API plus `filepath.Base` defense-in-depth in the memory store; research-report and secret names are traversal-guarded too.
- **H2** (SSRF DNS rebinding) → fixed: `pkg/safedial` resolves once, pins the validated IP in `DialContext`, and re-validates every redirect hop.
- **H4** (unsigned self-update) → fixed: Ed25519 signature over `checksums.txt` verified against a pinned public key before the swap (integrity-only when no key is pinned).
- **H5** (unbounded maps) → fixed *for the coordinator/session/rate-limiter maps* (TTL+cap+sweep). **Gap found in a sibling package — see N1 below.**
- **M1** (loopback trust bypass), **M3** (non-atomic writes), **M6** (stranded deps), **L1/L2/L4**, **I3/I4** → all verified fixed.
- **M4** (autoscaler placeholder) and **L3** (stale WebSocket 501 stub) → fixed earlier this session (real CPU sampler + live node count; handler message reconciled with the gateway).

**New finding this pass:**

### N1 — Unbounded task-tracking maps in the A2A agent server (memory-exhaustion DoS)
- **Severity:** Medium · **Category:** Reliability / Scalability (same class as the original H5)
- **Location:** `internal/a2a/server.go` — `results map[string]TaskResult` and `tasks map[string]string`. Every `tasks/submit` RPC added a permanent entry to both; `finish` stored a result per task. No eviction, TTL, or cap. The `/a2a/` tree is served on the management listener and is **not** auth-gated.
- **Why it's a problem:** The H5 remediation bounded the coordinator/session maps but the A2A `AgentServer`, added in the same milestone family, was never retrofitted. A long-running server reachable on the A2A RPC surface grows heap without limit — one entry per task, forever.
- **Failure scenario:** A caller (or a misbehaving A2A client) loops `tasks/submit`; `results`+`tasks` grow until OOM, which — consistent with the original H3/H5 reasoning — also destroys in-flight work.
- **Fix applied:** FIFO cap of `maxTrackedTasks = 4096` via `trackTaskLocked`; oldest task's mapping and result are evicted once the cap is exceeded, and `finish` only stores a result for a still-tracked task (so a task evicted mid-run can't re-leak). Covered by `TestAgentServer_TrackedTasksBounded`.

**Not re-opened (unchanged, as the original audit scoped them):** H3 (inner task-queue durability is still in-memory; workflow-level SQLite resume exists), M2 (multi-node shared state), M5 (tool sandbox depth). These remain multi-week architectural items, deliberately out of scope for a bug-fix pass.

---

**Auditor role:** Principal Engineer / Security Reviewer / SRE / Platform Architect
**Method:** Direct source inspection. Execution paths, imports, state transitions, and runtime behavior were traced from `cmd/vortex/main.go` through the `internal/*` subsystems. Documentation and milestone comments were treated as claims to be verified, not as ground truth.
**Repository state:** Single Go module (`github.com/vortex-run/vortex`, Go 1.26), one binary, ~190 Go source files across 30+ `internal/*` packages, a React dashboard (`web/dashboard`), and an extensive integration test suite (`integration/*_test.go`). Build plan milestones referenced in code span **M1 → M18**.

> **Bottom line up front:** VORTEX is an unusually broad and *well-organized* codebase with genuinely good test breadth and clean package boundaries. But it is **not production-ready**. The most serious problems are systemic, not cosmetic: every at-rest encryption key and the "tamper-proof" audit HMAC are derived from a **non-secret, publicly-exposed cluster name**; there is **no durable execution layer** despite the stated goal of running thousands of long-running agent executions; and several advertised capabilities (horizontal autoscaling, multi-node state) are placeholders or node-local. See the scorecard and verdict at the end.

---

## How to read this report

Each finding carries: Severity, Category, Title, Location, Why it's a problem, Production impact, Failure scenario, Recommended solution, Difficulty, Effort.

Severity legend: **Critical** (exploitable / data loss now) · **High** (serious, likely under load or attack) · **Medium** (real risk, conditional) · **Low** (minor) · **Improvement** (hardening / polish).

---

# CRITICAL FINDINGS

## C1 — All at-rest encryption & audit-integrity keys are derived from the (public) cluster name
- **Severity:** Critical
- **Category:** Security / Secret Handling / Cryptography
- **Location:**
  - `internal/cmd/secretbackend.go:41` — `secrets.NewSecretStore(secretStorePath(), []byte(cfg.Cluster.Name+"-secrets"))`
  - `internal/cmd/start.go:512` — `storeKey := []byte(cfg.Cluster.Name + "-tls-key")`
  - `internal/cmd/start.go:554` — `vtls.NewStore(storePath, []byte(cfg.Cluster.Name+"-mtls-key"))`
  - `internal/cmd/start.go:619` & `internal/cmd/audit.go:41` — `audit.NewLog(path, []byte(cfg.Cluster.Name+"-audit-key"))`
  - Key derivation: `internal/secrets/store.go:48` (`sha256.Sum256(key)`), `internal/audit/log.go:84` (HMAC keyed by the same material).
  - **Exposure:** `internal/api/api.go:429` returns `ClusterName: cfg.Cluster.Name` in the **unauthenticated** `/health` response.
- **Why it's a problem:** The XChaCha20-Poly1305 secret store, the TLS cert store, the mTLS cluster CA store, and the HMAC-SHA256 audit chain are all keyed by `clusterName + "-<suffix>"`. The cluster name is non-secret configuration — it is logged, shown in the dashboard, and **served to anonymous callers on `/health`**. Anyone who learns the cluster name (trivially) can reconstruct every key.
- **Production impact:**
  - Secrets "encrypted at rest" (DB passwords, API keys, JWT secrets) are decryptable by anyone with the cluster name and read access to the `.secret` files. The encryption provides essentially no confidentiality against an attacker who has the files.
  - The "tamper-proof" audit log is **forgeable**: with the derived HMAC key, an attacker can rewrite history and recompute a valid chain. The core security guarantee of `internal/audit` is void.
  - The mTLS cluster CA store key is likewise guessable, undermining the identity mesh.
- **Failure scenario:** Host is compromised or a backup of `<cache>/vortex/secrets/*.secret` leaks. Attacker reads `clusterName` from `GET /health` (no auth), computes `sha256("prod-cluster-secrets")`, decrypts every secret. Then computes the audit HMAC key and rewrites `audit.log` to erase the intrusion — `vortex audit verify` still passes.
- **Recommended solution:** Derive keys from a real secret: a random 32-byte master key generated on first run and stored with `0600` perms outside config (or in OS keychain / KMS / `VORTEX_MASTER_KEY` env), or use a KDF (Argon2id/scrypt) over an operator-supplied passphrase with a stored salt. Never key crypto from config that ends up in `/health`. Add a migration path for existing stores. Stop emitting `ClusterName` from the unauthenticated `/health`.
- **Difficulty:** Medium
- **Effort:** 2–3 days (key management, migration, tests across secrets/tls/mtls/audit).

---

# HIGH FINDINGS

## H1 — Path traversal via unsanitized, user-supplied `session_id` (arbitrary file write/read)
- **Severity:** High
- **Category:** Security / API / Storage
- **Location:** `internal/api/agents.go:69` (`SessionID` from request JSON) → `internal/agents/coordinator.go:173-186` (`memory(sessionID)` → `NewMemory(...).Load(sessionID)`) → `internal/agents/memory.go:72` `path()` = `filepath.Join(storePath, sessionID+".json")`, written by `Save()` (`memory.go:89`) and read by `Load()`/`List()`. No `filepath.Base`, no name validation.
- **Why it's a problem:** `session_id` flows straight from the POST body into a filesystem path. `filepath.Join(store, "../../../../home/user/.config/x")` resolves **outside** the store directory. `Save()` then writes attacker-influenced JSON (conversation content) there; `SessionHistory`/`Load` can read arbitrary `*.json` paths back.
- **Production impact:** Authenticated arbitrary file write (constrained to a `.json` suffix and JSON content) and arbitrary `.json` read. On a shared/multi-tenant deployment this is cross-tenant data tampering and a foothold for further escalation (e.g. writing into a watched directory).
- **Failure scenario:** A holder of any valid API key POSTs `{"message":"…","session_id":"../../../../etc/cron.d/evil"}` repeatedly; the conversation file is written outside the sandbox. Even without RCE, it corrupts other sessions by ID collision across tenants.
- **Recommended solution:** Validate `session_id` against `^[A-Za-z0-9_-]{1,64}$` (reuse the `secrets.ValidateName` pattern), or hash/`filepath.Base` it before use, and reject anything else with 400. Generate server-side session IDs when the client omits one.
- **Difficulty:** Easy
- **Effort:** 2–4 hours including tests.

## H2 — SSRF guard is bypassable via DNS rebinding (check-then-resolve TOCTOU)
- **Severity:** High
- **Category:** Security / Reliability
- **Location:** `internal/research/fetcher.go:46-66, 129-163` (`checkSSRF` resolves with `net.LookupIP` and validates, then `f.client.Do(req)` re-resolves independently). The same validate-then-fetch shape is used by the agent `http_get` tool family.
- **Why it's a problem:** `checkSSRF` resolves the hostname and rejects private/loopback/metadata IPs, but the subsequent `http.Client.Do` performs its **own** DNS resolution at dial time. A hostname whose DNS record flips between a public IP (at check time) and `169.254.169.254` / `127.0.0.1` / RFC1918 (at dial time) passes the guard and then connects to the internal target. Redirects are also not re-validated per hop.
- **Production impact:** Server-side request forgery against cloud metadata (credential theft), internal services, and loopback control planes — from an authenticated agent or research request.
- **Failure scenario:** Attacker hosts `rebind.evil.com` with a 1-second TTL alternating between `203.0.113.10` and `169.254.169.254`. The guard sees the public IP; the dialer hits metadata and returns IAM credentials in the fetched "page content."
- **Recommended solution:** Use a custom `http.Transport.DialContext` that resolves once, validates the resolved IP, and dials **that pinned IP** (control DNS and connection in one step). Re-validate on every redirect (`CheckRedirect`). Apply uniformly to research fetcher and all agent network tools.
- **Difficulty:** Medium
- **Effort:** 1 day.

## H3 — No durable execution / crash recovery for workflows, orchestration, or agent runs
- **Severity:** High
- **Category:** Reliability / State Management / Workflow Engine / Milestone Gap
- **Location:** `internal/orchestration/task_queue.go` (state in a `map[string]*Task` guarded by a mutex; no persistence), `internal/orchestration/orchestrator.go:71-133` (run loop holds all state in memory), `internal/agents/runtime.go` (in-flight Submits tracked only by a `sync.WaitGroup`).
- **Why it's a problem:** The task DAG, its state transitions, results, and shared memory exist **only in process memory**. There is no write-ahead log, no checkpoint, no resume. `Stop` waits for in-flight work but a crash/OOM/SIGKILL loses everything with no replay.
- **Production impact:** Directly contradicts the stated goal ("thousands of workflows and long-running agent executions"). A node restart abandons every running orchestration mid-flight. No idempotency or duplicate-execution prevention means a client retry re-runs side-effecting tasks (commands, deploys, payments via the commerce agent) from scratch.
- **Failure scenario:** A 40-minute multi-agent orchestration is 80% done when the process is OOM-killed. On restart there is no record it ever ran; the user re-submits; previously-completed side effects (e.g. a devops command, a forge build that pushed code) execute a second time.
- **Recommended solution:** Introduce a durable execution store (append-only event log or embedded DB such as SQLite/BoltDB; or adopt a proper workflow engine pattern). Persist task state transitions, make `Complete/Fail` idempotent keyed by task ID, and add a recovery path that rehydrates the queue on boot. Require executors to be idempotent or fence side effects with a dedup key.
- **Difficulty:** Hard
- **Effort:** 2–4 weeks (this is foundational and should precede further agent milestones).

## H4 — Self-update trusts a checksum from the same source as the binary (no signature/authenticity)
- **Severity:** High
- **Category:** Security / Supply Chain / DevOps
- **Location:** `internal/update/download.go:25-69` (SHA-256 verified against `expectedSHA256`), `internal/update/github.go` (checksum + asset both come from the GitHub release), `.github/workflows/release.yml` (GoReleaser publishes checksums but **no signing step**), `internal/cmd/selfupdate.go`.
- **Why it's a problem:** The SHA-256 check only proves the download matches the checksum *published alongside it*. There is no code-signing or detached signature (e.g. cosign/minisign/GPG). Whoever controls the release (compromised CI token, account takeover, or a tampered mirror) controls both the binary and its checksum.
- **Production impact:** A single compromised release auto-propagates a malicious binary to every node that self-updates — i.e. fleet-wide remote code execution.
- **Failure scenario:** `GITHUB_TOKEN` or a maintainer account is compromised; attacker publishes `v9.9.9` with a backdoored binary and matching checksum. Nodes running `vortex selfupdate` (or an auto-update timer) install it and pass verification.
- **Recommended solution:** Sign release artifacts (cosign keyless or minisign) and **verify the signature** before `AtomicReplace`. Pin the signing public key in the binary. Keep the SHA-256 check for integrity-in-transit but never treat it as authenticity.
- **Difficulty:** Medium
- **Effort:** 1–2 days (release pipeline + verification path + tests).

## H5 — Unbounded in-memory state (session/memory/rate-limit maps) → memory exhaustion DoS
- **Severity:** High
- **Category:** Reliability / Scalability / Performance
- **Location:** `internal/agents/coordinator.go:179-186` (`c.memories[sessionID]` cached forever), `coordinator.go:663` (`c.sessions[sessionID]`), per-IP limiter map in `internal/security` (`HTTPRateLimiter`), API-key store map. None have eviction/TTL.
- **Why it's a problem:** Each distinct `session_id` (attacker-chosen, see H1) allocates and **retains** a `*Memory` and `SessionState` for the process lifetime. The per-IP rate-limiter map grows with unique source IPs. No LRU, no TTL, no cap.
- **Production impact:** A modest stream of unique session IDs or source IPs grows heap without bound until OOM — and (per H3) the OOM destroys all in-flight work.
- **Failure scenario:** Authenticated client loops `session_id = uuid()` 10M times; resident memory climbs until the kernel OOM-kills the process.
- **Recommended solution:** Bound these maps with an LRU + TTL (e.g. evict idle sessions, cap entries), and prune the rate-limiter map periodically (sweep stale buckets). Persist session memory to disk and keep only a hot working set in RAM.
- **Difficulty:** Medium
- **Effort:** 1–2 days.

---

# MEDIUM FINDINGS

## M1 — Loopback "implicit trust" bypass on control-plane and metrics endpoints
- **Severity:** Medium
- **Category:** Security / API
- **Location:** `internal/api/api.go:357-365` (`protected` skips auth when `localhostOnly(r)`), `internal/api/reload.go:13-20` (`localhostOnly` trusts `RemoteAddr` loopback). Applies to `/internal/reload`, `/internal/shutdown`, `/metrics`, `/api/status`, `/api/secrets/status`, `/api/plugins`, `/api/audit`.
- **Why it's a problem:** Any process on the host — or any request that arrives with a loopback `RemoteAddr` (common when VORTEX sits behind a same-host reverse proxy/sidecar that doesn't set, or that the app doesn't honor, `X-Forwarded-For`) — gets unauthenticated control-plane access including **shutdown**, metrics, and secret set/unset status.
- **Production impact:** Co-tenant or sidecar processes can shut the server down or scrape operational metadata without a key. In containerized/proxied deployments the loopback assumption frequently does not hold the way intended.
- **Failure scenario:** VORTEX runs behind nginx on the same host; nginx proxies to `127.0.0.1:9090`, so every external request presents a loopback `RemoteAddr`. The auth gate is effectively disabled for all "protected" endpoints, and `POST /internal/shutdown` is reachable from the internet.
- **Recommended solution:** Don't equate loopback with authenticated. Gate the control plane on a unix socket or a required local token, or make the loopback exemption opt-in and explicitly disabled when a trusted-proxy mode is configured. Never expose `/internal/shutdown` via the same listener as proxied traffic.
- **Difficulty:** Medium
- **Effort:** 1 day.

## M2 — Node-local state prevents real horizontal scaling and multi-node consistency
- **Severity:** Medium
- **Category:** Scalability / Architecture / State Management
- **Location:** API keys (`internal/auth/apikey.go` JSON file), secrets (`internal/secrets/store.go` local files), namespaces (`reg.Save(namespaceStorePath())` in `start.go:774`), sessions (`internal/agents/memory.go` local JSON), audit log (local file). Raft (`internal/cluster`) is wired for membership/leadership, but application state is **not** replicated through it.
- **Why it's a problem:** Each node persists keys, secrets, namespaces, sessions, and audit entries to its own disk. Two nodes behind a load balancer will disagree on who has which API key, which secrets are set, and what the audit history is. The Raft FSM is not the source of truth for this data.
- **Production impact:** Horizontal scale-out produces split-brain control-plane state. A key created on node A is invalid on node B; audit chains diverge; quota enforcement is per-node, not global.
- **Failure scenario:** Operator runs 3 replicas. Admin issues an API key (served by node A). The client's next request lands on node B → 401. Audit verification differs per node.
- **Recommended solution:** Move shared control-plane state into the Raft FSM or an external store (Postgres/etcd). Define a single source of truth for keys/secrets/namespaces/quota counters. Until then, document VORTEX as single-node and disable the multi-replica story.
- **Difficulty:** Hard
- **Effort:** 3–6 weeks.

## M3 — Non-atomic persistence (key store, secrets, sessions, namespaces) risks corruption
- **Severity:** Medium
- **Category:** Reliability / Storage / Database
- **Location:** `internal/auth/apikey.go:158` (`os.WriteFile` direct), `internal/secrets/store.go:70`, `internal/agents/memory.go:89`. All write in place with no temp-file + `rename` + `fsync`.
- **Why it's a problem:** A crash or full disk mid-write truncates the target file. The API key store is saved **only on shutdown** (`start.go:149-154`), so an unclean exit loses every key issued since boot.
- **Production impact:** Data loss / corruption of credentials and secrets across crashes; "save on shutdown only" means SIGKILL loses all newly-issued keys.
- **Failure scenario:** Server is OOM-killed; `apikeys.json` never gets its shutdown-time write; all keys issued that session vanish. Or the process dies during `WriteFile` and leaves a half-written `*.secret`.
- **Recommended solution:** Write to a temp file in the same dir, `fsync`, then `os.Rename` (atomic). Persist key issuance immediately (on Issue/Revoke), not just at shutdown. Add corruption detection on load.
- **Difficulty:** Easy
- **Effort:** 0.5–1 day.

## M4 — Autoscaler is a non-functional placeholder presented as a feature
- **Severity:** Medium
- **Category:** Scalability / Maintainability / Milestone Gap
- **Location:** `internal/cmd/start.go:842-866` — the autoscaler loop is started with hardcoded metric callbacks `func() float64 { return 0 }` (CPU) and `func() int { return 1 }` (node count), with the comment "providers are placeholders until cluster metrics are wired."
- **Why it's a problem:** Logs say "autoscaler enabled" but it can never scale: it always observes 0% CPU and 1 node. Operators may believe autoscaling is protecting them.
- **Production impact:** False sense of elasticity; under load nothing scales. Operational surprise.
- **Failure scenario:** Traffic spikes; operator assumes autoscaling will add nodes; it never does because CPU is hardwired to 0%.
- **Recommended solution:** Wire real metric sources (cgroup/host CPU, cluster size from `internal/cluster`) or gate the feature behind an explicit "experimental/disabled" flag and stop logging "enabled."
- **Difficulty:** Medium
- **Effort:** 2–4 days to make real; 1 hour to honestly disable.

## M5 — Command whitelist provides little real containment; approval gate is the only control
- **Severity:** Medium
- **Category:** Security / Agent Runtime
- **Location:** `internal/agents/tools.go:380-505` (`DefaultAllowedCommands = {go, flutter, git, tar, unzip}`, `validateArgs`, `dangerousArgPatterns`), `internal/agents/local_tools.go:31-58` (catastrophic-pattern blocklist), `internal/cmd/start.go:96-103` (working dir is "INFORMATION ONLY… no path confinement").
- **Why it's a problem:** The code itself admits this (`tools.go:380-383`): whitelisted binaries (`go`, `git`, `tar`) can execute arbitrary code (`go run`, `git core.sshCommand`, `tar --to-command`). The substring/regex denylists are bypassable (encoding, alternate flags, shell features) and there is no OS-level sandbox (namespaces/seccomp/jail). For the local FS/terminal tools there is **no path confinement at all** — only a human-approval prompt and a small catastrophic-command regex.
- **Production impact:** If `RequireApproval` is ever disabled (it's a struct field that callers can clear), or if an approver rubber-stamps, the agent has full machine access. Denylist regexes give false confidence.
- **Failure scenario:** Operator disables approval for "automation"; agent runs `go run ./payload` or `git -c core.sshCommand='…' fetch` to execute arbitrary code outside any sandbox.
- **Recommended solution:** Treat the approval gate as the *only* real control and make it non-bypassable in production builds; add a genuine OS sandbox (Linux user namespaces + seccomp, or run tools in a container/microVM) before allowing unattended execution. Replace substring denylists with an allowlist of fully-resolved argv where feasible.
- **Difficulty:** Hard
- **Effort:** 1–3 weeks for a real sandbox.

## M6 — Orchestrator silently strands tasks with unknown/unsatisfiable dependencies
- **Severity:** Medium
- **Category:** Reliability / Workflow Engine
- **Location:** `internal/orchestration/task_queue.go:115-131` (`depStatus` returns `depWaiting` for an unknown dependency "may be added later"), `internal/orchestration/orchestrator.go:112-122` (loop breaks when nothing is ready, nothing in flight, and `drainBlocked` reports no change).
- **Why it's a problem:** A task that depends on an ID never added stays `pending` forever. The run loop exits and `summarize` reports it as **neither completed nor failed** — it just disappears from the success/failure accounting with no error surfaced.
- **Production impact:** Silent partial execution. A typo'd dependency ID yields a "successful-looking" run that quietly skipped work.
- **Failure scenario:** Planner emits a task `DependsOn:["analyze-2"]` but the task is named `analyze_2`. The dependent task never runs; the RunResult shows it as pending with no failure; downstream consumers assume completion.
- **Recommended solution:** Validate at load that every `DependsOn` ID exists (alongside the existing `HasCycle` check) and fail fast, or mark unresolved-dependency tasks `failed` with a clear error before returning.
- **Difficulty:** Easy
- **Effort:** 2–4 hours.

## M7 — CI runs tests only on release tags, not on PRs/commits
- **Severity:** Medium
- **Category:** DevOps / Testing
- **Location:** `.github/workflows/release.yml` (only trigger is `push: tags: v*`). No PR/branch workflow found under `.github/workflows/`.
- **Why it's a problem:** The race-enabled `go test ./...` gate runs **at release time only**. Regressions land on the main branch undetected until someone cuts a tag.
- **Production impact:** Broken or racy code can sit in `main` indefinitely; the release is the first time anyone finds out, blocking the release.
- **Failure scenario:** A PR introduces a data race; it merges green (no CI); two weeks later a release tag fails `go test -race`, stalling an urgent fix.
- **Recommended solution:** Add a `push`/`pull_request` CI workflow running `go vet`, `golangci-lint` (config already present at `.golangci.yml`), and `go test -race ./...` plus the dashboard build/lint. Make it a required status check.
- **Difficulty:** Easy
- **Effort:** 0.5 day.

## M8 — No application-level observability for the agent/workflow plane (operational blind spot)
- **Severity:** Medium
- **Category:** Observability
- **Location:** Metrics are wired for the proxy/data plane (`observability.NewMetrics`, `internal/observability/*`), but the agent runtime exposes only counters via `RuntimeStats` (`internal/agents/runtime.go:207-246`). No per-task metrics, no orchestration tracing spans, no SLOs for agent latency/failure, no health gate that reflects agent/queue health (`/ready` always returns `true`, `internal/api/api.go:455-460`).
- **Why it's a problem:** The most complex, most failure-prone subsystem (multi-agent orchestration) is the least observable. `/ready` is a constant `true`, so orchestration deadlock or executor stalls won't fail readiness or alert anyone.
- **Production impact:** Operators cannot see queue depth growth, task failure rates, or stuck workflows until users complain.
- **Failure scenario:** Executors hang (e.g. an AI provider timeout misconfigured); queue depth climbs; `/ready` still says ready; no metric or alert fires.
- **Recommended solution:** Emit Prometheus metrics for task state counts, durations, failures, and queue depth; add OTel spans around `runTask`; aggregate subsystem health into `/ready`.
- **Difficulty:** Medium
- **Effort:** 2–3 days.

---

# LOW FINDINGS

## L1 — Non-streaming agent submit buffers the entire response in memory
- **Severity:** Low · **Category:** Performance / API
- **Location:** `internal/api/agents.go:112-120` (`for chunk := range ch { resp += chunk }`).
- **Why:** String concatenation accumulates the full agent output before responding; large outputs cause O(n²) allocation and memory spikes.
- **Impact:** Latency and memory pressure on big outputs.
- **Fix:** Use `strings.Builder`, or prefer SSE; cap maximum response size.
- **Difficulty:** Easy · **Effort:** 1 hour.

## L2 — `Memory.List()` reads and JSON-parses every session file on each call
- **Severity:** Low · **Category:** Performance / Scalability
- **Location:** `internal/agents/memory.go:151-177`.
- **Why:** O(n) disk reads + unmarshal per listing; `/api/agents/history` becomes slow as sessions accumulate.
- **Impact:** Dashboard history view degrades linearly with session count.
- **Fix:** Maintain an index file or in-memory cache of summaries; paginate.
- **Difficulty:** Easy · **Effort:** 0.5 day.

## L3 — WebSocket proxying advertised but returns 501 Not Implemented
- **Severity:** Low · **Category:** Milestone Gap / UX
- **Location:** `internal/proxy/http/handler.go:118-119` ("Full WebSocket proxying lands in M2.6… writeJSONError(... "websocket support coming in M2.6")"). M2.6 (`internal/proxy/gateway/websocket.go`) exists separately, so the HTTP handler's inline path is stale/contradictory.
- **Why:** Conflicting signals about whether WS is supported on a given path.
- **Fix:** Reconcile the handler with the gateway implementation or update the message/routing.
- **Difficulty:** Medium · **Effort:** depends on intended routing; 1 day to reconcile.

## L4 — `protected`/secrets-status endpoint constructs a new secrets adapter on every call
- **Severity:** Low · **Category:** Performance
- **Location:** `internal/cmd/start.go:720-736` (`SetSecretsProvider` builds an adapter and calls `Get` per declared key on each `/api/secrets/status` request).
- **Why:** Rebuilds backend clients (Vault/AWS/GCP/local) per request; adds latency and load on the secret backend.
- **Fix:** Construct the adapter once and reuse; cache set/unset state briefly.
- **Difficulty:** Easy · **Effort:** 2–3 hours.

## L5 — Drives crypto from cluster name also makes secret/cert stores non-portable & un-rotatable
- **Severity:** Low (consequence of C1) · **Category:** Maintainability / Security
- **Location:** Same as C1.
- **Why:** Renaming a cluster silently invalidates all stored secrets/certs (key changes); there is no key-rotation path.
- **Fix:** Addressed by the C1 master-key remediation, plus a documented rotation procedure.
- **Difficulty:** Medium · **Effort:** folded into C1.

---

# IMPROVEMENTS

- **I1 — Documentation is effectively absent.** `docs/` contains only `.gitkeep`. No architecture overview, no API reference (no OpenAPI for the `/api/*` surface), no operational runbooks (start/stop/reload/restore/rotate keys), no threat model. For a platform of this scope, this is a major gap for operability and onboarding. *(Category: Documentation · Effort: 1–2 weeks for a baseline.)*
- **I2 — Many subsystem directories are scaffolds.** `internal/{config,cluster,security,observability,agents,messaging,studio}/.gitkeep`, `plugins/{runtime,sdk-go,builtin}/.gitkeep`, and all `agents/*/.gitkeep` role dirs are empty placeholders alongside real code in sibling files — verify which represent planned-but-empty modules vs. relocated code, to avoid "looks done" confusion. *(Category: Technical Debt.)*
- **I3 — `/ready` should aggregate subsystem readiness** rather than returning a constant `true` (`api.go:455-460`). *(Category: Observability.)*
- **I4 — Add request body size limits and timeouts uniformly.** `ReadHeaderTimeout` is set (`api.go:346`) but there is no `ReadTimeout`/`WriteTimeout`/`IdleTimeout` on the management server; slowloris-style and long-lived connections are unbounded except where `MaxBytesReader` is used. *(Category: Reliability/Security.)*
- **I5 — Pin and scan dependencies.** The module pulls a large, modern dependency set (OPA, quic-go, wazero, raft, memberlist, jwx). Add `govulncheck` and dependency review to CI; the supply-chain surface is wide. *(Category: Security/DevOps.)*
- **I6 — Make `RequireApproval` impossible to disable in release builds** (build tag or config that cannot be set in prod), given M5. *(Category: Security.)*

---

# Deep-Audit Area Summaries

**1. Runtime correctness.** State machines (`TaskQueue`, agent `Runtime`) are clearly written and the concurrency primitives are careful — the `Runtime.Submit`/`Stop` TOCTOU between `wg.Add` and `wg.Wait` is explicitly closed under a mutex (`runtime.go:107-205`), and `HasCycle` guards against DAG deadlock. Real gaps: unknown-dependency stranding (M6), and **no persistence** of any of it (H3). Cancellation/timeout paths exist (`orchestrator.go:136-155`). Idempotency / duplicate-execution prevention is **absent**.

**2. Reliability.** No crash recovery or durable execution (H3); non-atomic writes (M3); unbounded memory (H5). Graceful shutdown and reload are well-handled via the `lifecycle` manager and a thoughtful "stop old listener before binding new" rebuild (`start.go:431-474`).

**3. Security.** One critical key-management flaw (C1) undermines secrets-at-rest, mTLS CA, and audit integrity simultaneously. Plus path traversal (H1), SSRF rebinding (H2), unsigned auto-update (H4), and the loopback-trust bypass (M1). On the positive side: API keys are bcrypt(prehash) hashed and never stored in plaintext (`apikey.go`), the WASM plugin runtime is genuinely sandboxed (no host imports, memory + CPU caps, `WithCloseOnContextDone`), zip-slip is handled in `update.Extract`, and SSRF *is* at least attempted (just not rebinding-safe).

**4. Scalability.** Single-node by construction: control-plane state is node-local (M2); in-memory maps are unbounded (H5); autoscaler is a stub (M4). At ~100 users on one node it is plausibly fine; at 1k–10k concurrent sessions the unbounded maps and `Memory.List()` O(n) scans bite; at 100k it cannot work without the durable/shared-state and multi-node rework. **Exact first bottlenecks:** session/limiter maps (heap), per-request secret adapter construction (L4), and history listing (L2).

**5. Performance.** Hot paths in the proxy plane use a connection pool and are reasonable. Application-plane hot paths have the per-request allocations noted (L1, L2, L4). No N+1 DB queries — because there is no database (which is itself the scalability problem).

**6. Architecture.** Strong package boundaries and dependency direction: `api` is decoupled from subsystems via callback wiring (`SetXProvider`), avoiding import cycles (e.g. forge↔agents handled by callbacks). Single binary / single module is a deliberate constraint. The weakness is the absence of a persistence/state layer that every other subsystem implicitly needs.

**7. Testing.** Breadth is genuinely strong: ~90 `_test.go` files plus a wide `integration/` suite (auth, tls, mtls, security, cluster, tenancy, perf, orchestrate, etc.), and CI runs `-race`. The gaps are the **untestable-because-absent** paths: crash recovery, durable resume, idempotent re-execution, and multi-node consistency. Failure-injection and SSRF-rebinding tests are missing. CI doesn't run on PRs (M7).

**8. Observability.** Good for the data plane (Prometheus metrics, OTel tracing, correlation IDs, structured logging with sampling and a ring buffer). Blind on the agent/workflow plane (M8) and `/ready` is a constant.

**9. DevOps.** GoReleaser multi-platform builds with checksums and a release-time test gate exist — but no PR CI (M7), no signing (H4), no container/Helm/compose artifacts found, and rollback is binary-only (`Rollback` of `.bak`), not stateful.

**10. Documentation.** Near-zero (I1). Inline package comments are excellent and milestone-referenced, which partly compensates for engineers but not for operators.

---

# Milestone Verification (evidence-based)

Milestones are referenced in code as M1–M18. Independent inspection of the corresponding packages:

| Milestone (inferred) | Code present | Assessment |
|---|---|---|
| M1 lifecycle/CLI/config/logging/service install | `pkg/lifecycle`, `internal/cmd`, `internal/config`, `pkg/logger`, `internal/service` | **Genuinely complete & solid.** Reload-never-crashes, PID files, signals, log rotation/journal all implemented and tested. |
| M2 proxy (TCP/HTTP/QUIC/UDP/gateway/TLS) | `internal/proxy/*`, `internal/tls` | **Largely complete.** Caveat L3 (WS handler vs gateway contradiction); TLS store key flaw (C1). |
| M3 security (secrets, RBAC, OPA policy, audit, edge, mTLS) | `internal/secrets`, `internal/auth`, `internal/policy`, `internal/audit`, `internal/security` | **Functionally present but not production-grade:** C1 voids secrets-at-rest and audit integrity; M1 loopback bypass. |
| M6 plugins (WASM) | `internal/plugins` | **Genuinely complete & well-sandboxed.** One of the strongest areas. |
| M10/M11 agents + messaging | `internal/agents`, `internal/messaging` | **Functional prototype.** No durability (H3), traversal (H1), unbounded memory (H5), sandbox caveats (M5). |
| M12 Studio / M13 Forge / M14 healing / M15 research / M16 devops / M17 pipeline / M18 orchestration | `internal/studio`, `internal/forge`, `internal/healing`, `internal/research`, `internal/devops`, `internal/pipeline`, `internal/orchestration` | **Breadth is real; depth is prototype.** Each runs in-process with no persistence; research has the SSRF-rebinding gap; orchestration strands unknown deps. |
| Autoscaling (perf) | `internal/perf/autoscale.go` + wiring | **Placeholder (M4).** Advertised, not functional. |
| Multi-node clustering | `internal/cluster` (raft/gossip) | **Membership only.** App state not replicated (M2); the multi-node *value* (shared state) is not delivered. |

**Hidden gaps disguised as completion:** secrets "encrypted at rest" (key is public), audit "tamper-proof" (key is public/forgeable), "autoscaler enabled" (stub), multi-node (no shared state), "durable" long-running agents (in-memory only).

**Future-milestone risks / required refactors before continuing:** (1) Build the durable execution + shared state layer **first** (H3/M2) — every later agent/workflow milestone compounds on its absence. (2) Fix key management (C1) before any production data is stored. (3) Add a real tool sandbox (M5) before unattended agent execution. Building more agent features on the current in-memory, single-node, public-key foundation will multiply the rework.

---

# Final Scorecard

| Dimension | Score | Rationale |
|---|---:|---|
| **Security** | **3 / 10** | C1 alone is disqualifying for prod; plus H1/H2/H4/M1. Offset slightly by good API-key hashing and a real WASM sandbox. |
| **Reliability** | **4 / 10** | Excellent lifecycle/shutdown/reload, but no durable execution, non-atomic writes, unbounded memory. |
| **Scalability** | **3 / 10** | Single-node by construction; node-local state; placeholder autoscaler; unbounded maps. |
| **Maintainability** | **7 / 10** | Clean boundaries, careful concurrency, strong inline docs and tests. Held back by missing external docs and placeholder scaffolds. |
| **Production Readiness** | **3 / 10** | Impressive surface area, but core production guarantees (secret confidentiality, audit integrity, durability, scale) are not met. |

---

# Final Verdict

## **Alpha** (leaning Prototype on the security/durability axes)

**Justification — no sugarcoating:** VORTEX is a genuinely impressive *engineering breadth* achievement — ~30 subsystems, a single clean binary, careful concurrency, a real WASM sandbox, broad integration tests, and a coherent architecture. As a feature demonstrator it is well past prototype. But "production-ready" is a claim about guarantees under failure and attack, and on that axis it falls short in foundational ways:

- The platform's **secret-at-rest encryption and "tamper-proof" audit log are both keyed by a value it serves to anonymous clients** (C1). That is not a bug to patch around the edges; it nullifies two of the platform's headline security properties.
- It is designed to run **"thousands of workflows and long-running agent executions,"** yet a single restart loses every in-flight workflow with no recovery, no idempotency, and no replay (H3). For long-running execution specifically, this is the defining requirement and it is unmet.
- Several **advertised capabilities are placeholders or node-local** (autoscaling, multi-node shared state), which means the operational story doesn't match the runtime reality.

These are not polish items; they are foundational. They should be fixed **before** layering on further milestones, because every additional agent/workflow feature increases the cost of retrofitting durability, shared state, and proper key management.

**Path to Beta:** remediate C1 (key management), H1/H2/H4 (traversal, SSRF rebinding, signed updates), and H3/H5 (durable execution + bounded memory); add PR CI; reconcile or honestly disable the placeholder features. That is a focused, achievable program of work — call it ~6–10 engineer-weeks for the criticals/highs — and it would move the realistic verdict to **Beta**.

*Audit performed by direct source inspection on 2026-06-10. Findings cite exact file:line locations for independent re-verification.*
