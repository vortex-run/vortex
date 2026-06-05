// Stub renders a placeholder for pages not yet implemented. Real implementations
// replace these as the milestones progress.
export function Stub({ name }: { name: string }) {
  return (
    <div className="rounded-lg border border-border bg-card p-8 text-center text-muted-foreground">
      <div className="text-xl font-semibold text-foreground">{name}</div>
      <p className="mt-2 text-sm">This page is coming soon.</p>
    </div>
  );
}
