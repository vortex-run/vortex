# Intellectual property notes (maintainer briefing)

**This is not legal advice.** Talk to a patent attorney before filing or before
publishing detailed algorithm write-ups that could affect novelty.

## Candidates worth reviewing with counsel

1. **Side-effect fencing + crash resume** — SQLite effect journal keyed by
   scope/fingerprint/occurrence; orchestration exactly-once for completed tasks
   (`internal/agents/effect_ledger.go`, `internal/orchestration/`).
2. **HMAC hash-chained audit log** with re-key / migrate from legacy
   cluster-name keys (`internal/audit/`).
3. **Master-key HKDF purpose derivation** for at-rest crypto
   (`internal/keyring/`).
4. Possibly **edge mTLS SPIFFE-style identity** patterns (`internal/tls/mtls.go`).

## Open-source interaction

The repository is licensed under **Apache License 2.0**, which includes an
express patent grant to users for contributions. Filing strategy, provisional
timing, and what remains unpublished should be decided with counsel **before**
more public deep-dives into fencing algorithms.

## Product differentiation (usually not patent claims)

Nine-provider AI gateway, Telegram control, single-binary edge+agent — strong
product story; treat as marketing/architecture, not patent claims, unless
counsel sees otherwise.
