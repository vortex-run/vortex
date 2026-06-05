import { useQuery } from "@tanstack/react-query";
import { Bell, Moon, Sun } from "lucide-react";
import { fetchHealth } from "../../lib/api";
import { useUIStore } from "../../store";

interface TopBarProps {
  title: string;
}

// healthPill maps health status to a coloured pill.
function HealthPill() {
  const { data, isError } = useQuery({
    queryKey: ["health"],
    queryFn: fetchHealth,
    refetchInterval: 5000,
  });

  let color = "bg-amber-500";
  let label = "connecting";
  if (isError) {
    color = "bg-red-500";
    label = "unreachable";
  } else if (data?.status === "ok") {
    color = "bg-green-500";
    label = "healthy";
  }

  return (
    <span className="inline-flex items-center gap-2 rounded-full border border-border px-3 py-1 text-xs">
      <span className={`h-2 w-2 rounded-full ${color}`} />
      {label}
    </span>
  );
}

export function TopBar({ title }: TopBarProps) {
  const theme = useUIStore((s) => s.theme);
  const toggleTheme = useUIStore((s) => s.toggleTheme);

  return (
    <header className="flex h-14 items-center justify-between border-b border-border px-6">
      <h1 className="text-lg font-semibold">{title}</h1>
      <div className="flex items-center gap-4">
        <HealthPill />
        <button
          type="button"
          aria-label="notifications"
          className="text-muted-foreground hover:text-foreground"
        >
          <Bell size={18} />
        </button>
        <button
          type="button"
          aria-label="toggle theme"
          onClick={toggleTheme}
          className="text-muted-foreground hover:text-foreground"
        >
          {theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}
        </button>
      </div>
    </header>
  );
}
