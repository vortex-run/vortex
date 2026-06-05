import type { ReactNode } from "react";

// MetricCard is a compact panel for a labelled metric (used in the security
// snapshot row and similar).
export function MetricCard({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="text-xl font-semibold">{value}</div>
      <div className="mt-1 text-xs text-muted-foreground">{label}</div>
    </div>
  );
}
