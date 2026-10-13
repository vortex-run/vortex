# Security Policy

## Supported versions

Security fixes land on `main` and ship in the next tagged release. Always run
the latest `v*` release (or build from `main`) when exposing VORTEX to a
network.

## Reporting a vulnerability

**Do not open a public GitHub issue for security reports.**

Email the maintainers via GitHub Security Advisories:

1. Open https://github.com/vortex-run/vortex/security/advisories/new
2. Include: affected version/commit, reproduction steps, impact, and any
   suggested fix
3. Allow a reasonable window (typically 90 days) before public disclosure

We will acknowledge receipt, assess severity, and coordinate a fix and
advisory. Please avoid sharing exploit details publicly until a release that
addresses the issue is available.

## Scope

In scope: the VORTEX binary, management API, edge proxy, agent tooling,
release/self-update path, and the embedded dashboard as shipped from this
repository.

Out of scope: third-party AI providers, user-supplied `vortex.cue` misconfiguration,
and deployments that disable the approval gate or trust loopback behind a
shared reverse proxy without `VORTEX_TRUST_LOOPBACK=false`.

## Further reading

- [docs/security.md](docs/security.md) — product security model
- [VORTEX_PRODUCTION_AUDIT.md](VORTEX_PRODUCTION_AUDIT.md) — independent audit
  status and remediation history
