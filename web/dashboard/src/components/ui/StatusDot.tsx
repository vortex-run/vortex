type Status = "green" | "amber" | "red";

const COLOR: Record<Status, string> = {
  green: "bg-green-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
};

// StatusDot is a small coloured liveness indicator.
export function StatusDot({ status }: { status: Status }) {
  return <span className={`inline-block h-2.5 w-2.5 rounded-full ${COLOR[status]}`} />;
}
