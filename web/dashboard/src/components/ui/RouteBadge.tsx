// RouteBadge renders a route's protocol as a coloured pill.
const COLORS: Record<string, string> = {
  https: "bg-blue-500/15 text-blue-400 border-blue-500/30",
  http: "bg-sky-500/15 text-sky-400 border-sky-500/30",
  tcp: "bg-green-500/15 text-green-400 border-green-500/30",
  udp: "bg-amber-500/15 text-amber-400 border-amber-500/30",
  h3: "bg-purple-500/15 text-purple-400 border-purple-500/30",
};

export function RouteBadge({ protocol }: { protocol: string }) {
  const cls = COLORS[protocol] ?? "bg-muted text-muted-foreground border-border";
  return (
    <span className={`inline-block rounded border px-2 py-0.5 text-xs font-medium ${cls}`}>
      {protocol}
    </span>
  );
}
