# Agent drop-ins

This directory is **operator scaffolding** for optional agent-related assets.
Product implementation lives in [`internal/agents`](../internal/agents) and
[`internal/orchestration`](../internal/orchestration).

Do not add Go packages here unless you are intentionally extending the drop-in
layout documented in [docs/development.md](../docs/development.md).
