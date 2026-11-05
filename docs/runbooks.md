# Operator runbooks

What to do when something goes wrong on a running VORTEX node. For background,
see [security.md](security.md) and [configuration.md](configuration.md).

Default paths use the OS config and cache directories:

| Data | Location |
|---|---|
| Master key | `<config>/vortex/master.key` |
| Orchestration runs | `<cache>/vortex/memory/orchestration.db` |
| Workflows | `<cache>/vortex/memory/workflows.db` |
| Conversations | `<cache>/vortex/memory/conversations.db` |

## First checks

```bash
vortex status                     # is the server up?
curl -s localhost:9090/health     # public liveness
curl -s localhost:9090/ready      # subsystem readiness
vortex check --config vortex.cue  # is the config valid?
vortex start --verbose            # startup detail, in the foreground
```

## The server crashed or was killed

1. Start it again with `vortex start`, or let your service manager or
   `vortex watchdog` do it.
2. Interrupted orchestration runs resume on start, as long as an AI provider is
   configured. Completed tasks are not repeated, and journaled side effects are
   replayed rather than run again.
3. Check the result on the dashboard Agents page or at
   `GET /api/orchestration/runs` (API key required).

A side effect that was in progress at the moment of the crash may not have been
journaled. Check the affected system if a task was changing something external.

## A config change had no effect

`vortex reload` applies most of `vortex.cue` without a restart. It does **not**
apply `agents.local_tools`. To change `trusted_root` or `auto_mode`, restart:

```bash
vortex check --config vortex.cue
vortex stop
vortex start
```

A config that fails validation stops the server from starting. Run
`vortex check` first so a typo doesn't turn into an outage.

## Audit verification failed

Run `vortex audit verify`. The server also verifies at startup and logs the
result. The error message tells you which case you have:

- **Chain broken at entry 1, "written with a different key, or entry 1 was
  modified".** Usually a key problem, such as a restored backup or a changed
  `VORTEX_MASTER_KEY`. Restore the original master key and verify again. If it
  still fails with the right key, treat it as the next case.
- **Chain broken at a later entry, "entry modified".** The key is proven correct
  by the entries before it, so the log was changed. Treat this as a security
  incident: keep a copy of the log and the host's other logs before changing
  anything, and do not archive or rotate the log until you have them.

## Master key

Back up `master.key` somewhere separate from the data it protects. **If it is
lost, encrypted secrets cannot be recovered.**

To rotate, set `VORTEX_MASTER_KEY` to a new value while the old data is still
decryptable, so the automatic migration can run. Back up the data first.

## Self-update failed

```bash
vortex self-update --check   # what is available?
vortex verify                # does this binary match its release?
```

- **"release has no checksums.txt signature"**: the release is unsigned. This
  binary pins a signing key, so it refuses. Do not bypass this; the release must
  be republished with a signature.
- **"checksums signature verification failed"**: the signature does not match
  the pinned key. Treat the release as suspect and report it. See
  [SECURITY.md](../SECURITY.md).
- **Windows: "a previous update's vortex.exe.old is still in use"**: an older
  VORTEX process from the last update is still running. Stop it
  (`vortex stop`, or end it in Task Manager) and run `vortex self-update`
  again.

## Rotating the release signing key

Plan this carefully. Installed binaries trust only the key they were built with,
so **they will reject every release signed with a new key** and can't update
through `vortex self-update`. Users have to install the first new-key release
manually with `scripts/verify-release.sh` or the installer, so announce it.

The steps are in [security.md](security.md#release-signing). Change the pinned
key and the `VORTEX_SIGNING_KEY` secret together: the release fails at signing
if they don't match.

If the old private key leaked, rotate immediately and say so publicly.
