# Renamed: VORTEX is now Gubern

Gubern is the new name of VORTEX. The command is `gubern`, settings are
`GUBERN_*`, and new installs keep their files in `gubern` folders. An existing
VORTEX install keeps working without changes; you can switch to the new names
when it suits you.

## What still works

| You have | What happens |
|---|---|
| `VORTEX_*` environment variables | Still read, as the matching `GUBERN_*` name. If both are set, `GUBERN_*` wins. The CLI prints one line naming the variables to rename. |
| `vortex.cue` and no `gubern.cue` | Used as the config file when you don't pass `--config`. |
| Data in `<config>/vortex` or `<cache>/vortex` (keys, audit log, API keys, secrets, certificates, history) | Keeps being used where it is when there is no `gubern` folder. Nothing is moved. |
| `vortex.pid` from a server started by the old version | `gubern stop`, `status` and `reload` find it. |
| A `vortex` binary on your PATH | The installer replaces it with a link to `gubern`; the Linux packages install `/usr/bin/vortex` as a link. |
| A systemd/OpenRC service named `vortex` (account `vortex`, `/var/lib/vortex`) | `gubern service install` keeps that service's name, account and home and points it at the new binary; your data stays in place. |
| `vortex self-update` on an old binary | Releases still publish `vortex_<os>_<arch>` archives, so it keeps updating. |

## Unchanged names

These keep their old names because existing data and integrations depend on
them: the `X-Vortex-*` HTTP headers, the `vortex_*` Prometheus metric names,
the secret-store prefixes (`vortex/` in Vault, `/vortex/` in AWS SSM, `vortex-`
in GCP Secret Manager), the Go module path (`github.com/vortex-run/vortex`),
and the GitHub repository and download URLs.

## Moving to the new names (optional)

1. Rename `VORTEX_*` variables to `GUBERN_*` in your shell profile, service
   environment or container spec.
2. Rename `vortex.cue` to `gubern.cue` (or keep passing `--config`).
3. To move per-user data, stop Gubern and rename the `vortex` folder in your
   config and cache directories to `gubern` (both, if both exist). Don't
   copy only some of the files: the master key, audit log and stores belong
   together.
4. Use `gubern` in scripts. The `vortex` link can then be removed.
