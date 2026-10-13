# VORTEX Web Dashboard

React management UI embedded into the VORTEX binary and served at `/dashboard/`.

## Stack

- React 19 + TypeScript + Vite
- TanStack Query (server state) + Zustand ([`src/store.ts`](src/store.ts)) for UI state
- React Router 8, Tailwind CSS 3, Recharts, Lucide

## Develop

```bash
npm ci
npm run dev      # Vite dev server (proxy API to a running vortex on :9090 if configured)
npm run lint
npm run build    # writes dist/ — must be committed (Go embeds it)
```

CI rebuilds `dist/` and fails if the committed tree is stale. After UI changes:

```bash
npm run build
git add dist
```

## Layout

| Path | Role |
|------|------|
| `src/pages/` | Route screens |
| `src/components/` | Layout + shared UI |
| `src/lib/` | API client, hooks, Prometheus parse |
| `src/store.ts` | Client UI state (Zustand) |
| `dist/` | Production build (embedded) |

Do not invent a second state library — Query for remote data, Zustand for local UI.
