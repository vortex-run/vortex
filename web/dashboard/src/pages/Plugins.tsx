import { useQuery } from "@tanstack/react-query";
import { Puzzle, ExternalLink } from "lucide-react";

interface PluginManifest {
  name: string;
  version: string;
  description?: string;
  hook_types?: string[];
}

// fetchPlugins calls the plugins API, tolerating 404 (not yet wired) as an empty
// list.
async function fetchPlugins(): Promise<PluginManifest[]> {
  const res = await fetch("/api/plugins", { headers: { Accept: "application/json" } });
  if (res.status === 404) return [];
  if (!res.ok) throw new Error(`/api/plugins returned ${res.status}`);
  const body = (await res.json()) as { plugins?: PluginManifest[] };
  return body.plugins ?? [];
}

export function Plugins() {
  const { data: plugins = [] } = useQuery({ queryKey: ["plugins"], queryFn: fetchPlugins });

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <a
          href="https://github.com/vortex-run/vortex"
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-sm text-primary hover:underline"
        >
          Plugin registry docs <ExternalLink size={14} />
        </a>
        <button className="rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground" disabled>
          Install
        </button>
      </div>

      {plugins.length === 0 ? (
        <div className="rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
          <Puzzle size={28} className="mx-auto mb-2 opacity-50" />
          <div className="text-foreground">No plugins installed</div>
          <p className="mt-1 text-sm">
            Install a plugin:{" "}
            <code className="font-mono">vortex plugin install &lt;path&gt;</code>
          </p>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {plugins.map((p) => (
            <div key={`${p.name}@${p.version}`} className="rounded-lg border border-border bg-card p-4">
              <div className="flex items-center justify-between">
                <span className="font-medium">{p.name}</span>
                <span className="font-mono text-xs text-muted-foreground">v{p.version}</span>
              </div>
              <div className="mt-2 flex flex-wrap gap-1">
                {(p.hook_types ?? []).map((h) => (
                  <span key={h} className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
                    {h}
                  </span>
                ))}
              </div>
              {p.description && (
                <p className="mt-2 text-sm text-muted-foreground">{p.description}</p>
              )}
              <div className="mt-3 text-right">
                <button className="text-xs text-red-400 hover:underline" disabled>
                  Remove
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
