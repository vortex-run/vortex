import { X } from "lucide-react";
import type { RouteHealth } from "../../types";
import { ProtocolBadge } from "./ProtocolBadge";

interface Props {
  route: RouteHealth | null;
  onClose: () => void;
}

// RouteDetailDrawer slides in from the right showing a route's details. Backend
// list, health checks, rate limits, and plugins are placeholders until the
// management API exposes full route config (M8).
export function RouteDetailDrawer({ route, onClose }: Props) {
  if (!route) return null;
  return (
    <div className="fixed inset-0 z-40 flex justify-end">
      {/* Scrim */}
      <div className="absolute inset-0 bg-black/50" onClick={onClose} />
      {/* Panel */}
      <div className="relative z-50 h-full w-96 overflow-auto border-l border-border bg-card p-5">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-semibold">{route.name}</h2>
          <button onClick={onClose} aria-label="close" className="text-muted-foreground hover:text-foreground">
            <X size={18} />
          </button>
        </div>

        <div className="mt-4 space-y-4 text-sm">
          <Field label="Protocol">
            <ProtocolBadge protocol={route.protocol} />
          </Field>
          <Field label="Listen">
            <span className="font-mono">{route.listen}</span>
          </Field>
          <Field label="Active connections">
            <span className="font-mono">{route.active}</span>
          </Field>

          <Section title="Backends">
            <p className="text-muted-foreground">
              Backend detail requires the route config API (coming in M8).
            </p>
          </Section>
          <Section title="Rate limit">
            <p className="text-muted-foreground">Not exposed yet.</p>
          </Section>
          <Section title="Plugins">
            <p className="text-muted-foreground">Not exposed yet.</p>
          </Section>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-muted-foreground">{label}</span>
      {children}
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-md border border-border p-3">
      <div className="mb-1 font-medium">{title}</div>
      {children}
    </div>
  );
}
