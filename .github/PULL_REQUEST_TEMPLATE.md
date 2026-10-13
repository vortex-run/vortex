## Summary

<!-- What and why (not a file list). -->

## Test plan

- [ ] `task test` (or `go test ./...`) passes locally
- [ ] If agents/release-path touched: `go test -tags release ./internal/agents/...`
- [ ] If dashboard touched: `cd web/dashboard && npm ci && npm run lint && npm run build` and commit refreshed `dist/`
- [ ] If deps touched: `govulncheck ./...` (Go 1.26.9+) is clean

## Notes

<!-- Breaking changes, follow-ups, screenshots. -->
