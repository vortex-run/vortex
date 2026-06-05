import { create } from "zustand";

// UIState is the global UI store. It currently tracks the dark/light theme;
// pages add their own slices as the dashboard grows.
interface UIState {
  theme: "dark" | "light";
  toggleTheme: () => void;
}

export const useUIStore = create<UIState>((set) => ({
  theme: "dark",
  toggleTheme: () =>
    set((s) => {
      const next = s.theme === "dark" ? "light" : "dark";
      document.documentElement.classList.toggle("dark", next === "dark");
      return { theme: next };
    }),
}));
