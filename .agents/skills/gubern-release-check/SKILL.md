---
name: gubern-release-check
description: >
  How to validate Gubern's release configuration and install scripts without
  publishing anything: goreleaser check and snapshot build, archive contents,
  and the installer tests. Use after changing .goreleaser.yaml, scripts/install.sh
  or other release scripts.
license: Apache-2.0
metadata:
  project: gubern
---

# Checking release and install changes

Nothing here publishes a release; never run `goreleaser release` without
`--snapshot`.

1. Validate the config: `goreleaser check`.
2. Build every platform locally:
   `goreleaser release --snapshot --clean --skip=sign,publish,sbom`
   (signing needs the release key, SBOMs need syft).
3. Check the archives in `dist/`:
   - `gubern_<os>_<arch>` archives contain `gubern` (or `gubern.exe`) and
     `gubern.cue`.
   - The legacy `vortex_<os>_<arch>` archives still exist and contain a
     `vortex` binary and `vortex.cue`. Installs from before the VORTEX → Gubern
     rename self-update from these; removing them breaks those installs.
4. Installer tests (POSIX sh; run on Linux or macOS):
   - `sh scripts/install_sig_test.sh` (signature checks)
   - `sh scripts/install_flow_test.sh dist` (fresh host, pre-rename host,
     pre-rename release; uses the snapshot from step 2)
5. `shellcheck` any shell script you changed.

Report each step's result, and anything you could not run.
