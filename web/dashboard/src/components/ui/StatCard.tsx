import type { ReactNode } from "react";

interface StatCardProps {
  label: string;
  value: ReactNode;
  trend?: string; // e.g. "+5%" or "-2%"
  trendUp?: boolean;
}

// StatCard shows a single headline statistic with an optional trend indicator.
export function StatCard({ label, value, trend, trendUp }: StatCardProps) {
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <div className="mt-2 flex items-baseline gap-2">
        <span className="text-2xl font-semibold">{value}</span>
        {trend && (
          <span className={trendUp ? "text-xs text-green-400" : "text-xs text-red-400"}>
            {trend}
          </span>
        )}
      </div>
    </div>
  );
}
