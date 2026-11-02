import { ApiAuthError } from "../lib/api";
import { useAgentsCatalog, useOrchRunTasksByRun, useOrchRuns, useTeamAgents } from "../lib/hooks";
import { StatusDot } from "../components/ui/StatusDot";
import type { OrchRun, OrchTask } from "../types";

function statusTone(status: string): "green" | "amber" | "red" {
  switch ((status || "").toLowerCase()) {
    case "idle":
    case "running":
      return "green";
    case "complete":
    case "completed":
    case "done":
    case "busy":
    case "pending":
    case "blocked":
      return "amber";
    case "failed":
    case "error":
    case "offline":
      return "red";
    default:
      return "amber";
  }
}

// Agents shows live team agent cards (when team mode is on) plus the static
// capability catalog. Empty/loading/auth-denied states are explicit — no fake
// progress or mock agents in the production path.
function tasksForRun(
  runId: string,
  taskQueries: ReturnType<typeof useOrchRunTasksByRun>,
  runs: OrchRun[],
): OrchTask[] {
  const idx = runs.findIndex((r) => r.id === runId);
  if (idx < 0) return [];
  const q = taskQueries[idx];
  if (!q?.data || q.data.run_id !== runId) return [];
  return q.data.tasks;
}

export function Agents() {
  const team = useTeamAgents();
  const catalog = useAgentsCatalog();
  const orch = useOrchRuns();
  const live = team.data?.agents ?? [];
  const defs = catalog.data?.agents ?? [];
  const orchRuns: OrchRun[] = orch.data?.runs ?? [];
  const orchTasksEnabled = !orch.authDenied && orchRuns.length > 0;
  const orchTaskQueries = useOrchRunTasksByRun(orchRuns, orchTasksEnabled);
  const orchTasksAuthDenied = orchTaskQueries.some((q) => q.error instanceof ApiAuthError);
  const authDenied =
    team.authDenied || catalog.authDenied || orch.authDenied || orchTasksAuthDenied;

  return (
    <div className="space-y-4">
      <div className="rounded-lg border border-border bg-card p-4">
        <h2 className="mb-1 text-sm font-semibold text-foreground">Agent runtime</h2>
        <p className="text-sm text-muted-foreground">
          Live team status when agent teams are enabled, plus the capability catalog used for
          selection. Auto Mode workspace trust is enforced by the runtime, not shown as a global
          PC-wide unlock.
        </p>
      </div>

      {authDenied && (
        <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-4 text-sm text-amber-200">
          API key required to load agent endpoints. Configure a management API key and retry.
        </div>
      )}

      <div className="rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold text-foreground">Live team</h2>
        {team.isLoading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : team.authDenied ? (
          <p className="text-sm text-muted-foreground">Not loaded — API key required.</p>
        ) : team.isError ? (
          <p className="text-sm text-red-400">Failed to load team status.</p>
        ) : live.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No live team agents. Enable team mode or check that specialists are registered.
          </p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                <th className="pb-2">ID</th>
                <th className="pb-2">NAME</th>
                <th className="pb-2">ROLE</th>
                <th className="pb-2">STATUS</th>
                <th className="pb-2">CAPABILITIES</th>
              </tr>
            </thead>
            <tbody>
              {live.map((a) => (
                <tr key={a.id} className="border-t border-border/60">
                  <td className="py-1.5 font-mono text-xs">{a.id}</td>
                  <td className="py-1.5">{a.name}</td>
                  <td className="py-1.5">{a.role}</td>
                  <td className="py-1.5">
                    <span className="inline-flex items-center gap-2">
                      <StatusDot status={statusTone(a.status)} />
                      {a.status || "unknown"}
                    </span>
                  </td>
                  <td className="py-1.5 text-muted-foreground">
                    {(a.capabilities ?? []).join(", ") || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold text-foreground">Orchestration runs</h2>
        {orch.isLoading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : orch.authDenied ? (
          <p className="text-sm text-muted-foreground">Not loaded — API key required.</p>
        ) : orch.isError ? (
          <p className="text-sm text-red-400">Failed to load orchestration runs.</p>
        ) : orchRuns.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No orchestration runs yet. Recent runs (running, done, or failed) appear here when
            RunStore is wired via SetOrchStoreProvider.
          </p>
        ) : (
          <div className="space-y-4">
            {orchRuns.map((run, idx) => {
              const taskQ = orchTaskQueries[idx];
              const tasks = tasksForRun(run.id, orchTaskQueries, orchRuns);
              const tasksLoading = taskQ?.isLoading;
              const tasksAuthDenied = taskQ?.error instanceof ApiAuthError;
              const tasksError = taskQ?.isError && !tasksAuthDenied;
              return (
                <div key={run.id} className="rounded-md border border-border/60 p-3">
                  <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
                    <span className="font-mono text-xs text-muted-foreground">{run.id}</span>
                    <span className="inline-flex items-center gap-2">
                      <StatusDot status={statusTone(run.status)} />
                      {run.status || "unknown"}
                    </span>
                    {run.session_id ? (
                      <span className="text-xs text-muted-foreground">session {run.session_id}</span>
                    ) : null}
                  </div>
                  <p className="mb-2 text-sm text-foreground">{run.goal}</p>
                  {tasksLoading ? (
                    <p className="text-xs text-muted-foreground">Loading tasks…</p>
                  ) : tasksAuthDenied ? (
                    <p className="text-xs text-muted-foreground">Tasks not loaded — API key required.</p>
                  ) : tasksError ? (
                    <p className="text-xs text-red-400">Failed to load tasks for this run.</p>
                  ) : tasks.length === 0 ? (
                    <p className="text-xs text-muted-foreground">No tasks loaded for this run.</p>
                  ) : (
                    <table className="w-full text-sm">
                      <thead>
                        <tr className="text-left text-xs text-muted-foreground">
                          <th className="pb-1">TASK</th>
                          <th className="pb-1">AGENT</th>
                          <th className="pb-1">STATE</th>
                          <th className="pb-1">ATTEMPTS</th>
                        </tr>
                      </thead>
                      <tbody>
                        {tasks.map((t) => (
                          <tr key={t.id} className="border-t border-border/40">
                            <td className="py-1 font-mono text-xs">{t.name || t.id}</td>
                            <td className="py-1 text-muted-foreground">{t.agent_type || "—"}</td>
                            <td className="py-1">
                              <span className="inline-flex items-center gap-2">
                                <StatusDot status={statusTone(t.state)} />
                                {t.state}
                              </span>
                            </td>
                            <td className="py-1 text-muted-foreground">
                              {t.attempts ?? 0}
                              {t.max_attempts ? ` / ${t.max_attempts}` : ""}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>

      <div className="rounded-lg border border-border bg-card p-4">
        <h2 className="mb-3 text-sm font-semibold text-foreground">Capability catalog</h2>
        {catalog.isLoading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : catalog.authDenied ? (
          <p className="text-sm text-muted-foreground">Not loaded — API key required.</p>
        ) : catalog.isError ? (
          <p className="text-sm text-red-400">Failed to load catalog.</p>
        ) : defs.length === 0 ? (
          <p className="text-sm text-muted-foreground">Catalog unavailable.</p>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                <th className="pb-2">ID</th>
                <th className="pb-2">ROLE</th>
                <th className="pb-2">PROFILE</th>
                <th className="pb-2">CAPABILITIES</th>
              </tr>
            </thead>
            <tbody>
              {defs.map((a) => (
                <tr key={a.id} className="border-t border-border/60">
                  <td className="py-1.5 font-mono text-xs">{a.id}</td>
                  <td className="py-1.5">{a.role}</td>
                  <td className="py-1.5 text-muted-foreground">{a.permission_profile || "—"}</td>
                  <td className="py-1.5 text-muted-foreground">
                    {(a.capabilities ?? []).join(", ")}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
