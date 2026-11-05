---
name: gubern-dev-checks
description: >
  How to check a change to the Gubern codebase before calling it done:
  formatting, vet, unit tests, the integration suite, and the dashboard.
  Use after editing Go or dashboard code in this repository.
license: Apache-2.0
metadata:
  project: gubern
---

# Checking a change to Gubern

Run the checks that match what you changed, in this order. Stop at the first
failure and fix it; don't skip a failing check.

## Go code

1. Format: `gofmt -l ./internal ./cmd ./pkg` must print nothing. On a
   Windows checkout with CRLF line endings gofmt lists every file; check a
   copy with the carriage returns removed instead (the repository stores LF).
2. Vet: `go vet ./...`
3. Unit tests for the packages you touched, then all of them:
   `go test ./internal/<pkg>/...`, then `go test ./...`
4. If you changed anything a running server does (routes, auth, config,
   startup, the agent runtime), run the integration suite:
   `go test -tags integration -timeout 900s ./integration/...`
   It builds `./cmd/gubern` and starts real servers on 127.0.0.1:9090, so
   nothing else may be using that port.

## Dashboard (`web/dashboard/`)

1. `npm run lint`
2. `npm run build` (type-checks, then rebuilds `dist/`, which is committed
   and embedded in the binary; commit the new `dist/` with the change).

## Release config or install scripts

Use the `gubern-release-check` skill.

## Reporting

Say which checks you ran and their results. If you couldn't run one (for
example the integration suite needs a free port), say so instead of
implying it passed.
