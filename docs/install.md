# Installation

Upgrading a VORTEX install? Gubern is the same product under a new name, and
the old settings, files and service keep working; see
[migrating-from-vortex.md](migrating-from-vortex.md).

Gubern ships as a single static binary with no runtime dependencies. Pick the
method that fits your platform.

## Linux / macOS — one-line installer

```bash
curl -fsSL https://vortex.run/install | sh
```

The installer detects your OS/arch, downloads the matching release, verifies
its SHA-256 against `checksums.txt`, installs the binary to
`/usr/local/bin/gubern`, and seeds an example config at
`/etc/gubern/gubern.cue`. Re-running it upgrades in place and never clobbers an
existing config. It runs under any POSIX `sh` (dash, BusyBox ash, bash) and
only uses `sudo` for directories you can't write.

**Release signature.** A checksum only proves the download matches
`checksums.txt`, which comes from the same place. The installer also verifies
the release's Ed25519 signature over `checksums.txt` (as `gubern self-update`
does) and refuses to install if it doesn't match. That check needs OpenSSL 3,
which current Debian, Ubuntu, Fedora and Alpine have; macOS (LibreSSL) and
older systems don't, and releases up to v0.3.0 aren't signed. In those cases
the installer warns and continues. Set `GUBERN_REQUIRE_SIGNATURE=1` to refuse
instead:

```bash
curl -fsSL https://vortex.run/install | GUBERN_REQUIRE_SIGNATURE=1 sh
```

Options:

```bash
# install a specific version
curl -fsSL https://vortex.run/install | sh -s -- --version v0.2.0

# install to a custom directory
curl -fsSL https://vortex.run/install | sh -s -- --install-dir "$HOME/.local/bin"
```

Environment overrides: `GUBERN_VERSION`, `GUBERN_INSTALL_DIR`,
`GUBERN_CONFIG_DIR`.

## Manual download

Grab the archive for your platform from the
[Releases](https://github.com/sandeepbollavaram/vortex/releases) page:

| Platform | Asset |
|---|---|
| Linux x86-64 | `gubern_linux_amd64.tar.gz` |
| Linux ARM64 | `gubern_linux_arm64.tar.gz` |
| macOS Apple Silicon | `gubern_darwin_arm64.tar.gz` |
| macOS Intel | `gubern_darwin_amd64.tar.gz` |
| Windows x86-64 | `gubern_windows_amd64.zip` |

```bash
tar -xzf gubern_linux_amd64.tar.gz
sudo install -m 0755 gubern /usr/local/bin/gubern
gubern version
```

## Windows

1. Download `gubern_windows_amd64.zip` and extract `gubern.exe`.
2. Move it to `C:\Program Files\gubern\gubern.exe`.
3. Install it as a service:

   ```powershell
   gubern service install
   ```

## Verifying a release

Every release publishes `checksums.txt` and (when signing is enabled) an
Ed25519 signature `checksums.txt.sig`. Verify before trusting a binary:

```bash
# verify the running binary against the published release
gubern verify

# verify every artifact of a tag end-to-end
./scripts/verify-release.sh v0.2.0
```

`gubern self-update` performs the same signature check automatically before
replacing the binary. It takes the archive's checksum from the same verified
`checksums.txt` bytes, installs only a release newer than the running binary
(`--allow-downgrade` overrides this), and refuses an archive whose binary reports
a different version than the release tag. It then runs the new binary's `version` command and puts
the old one back if that fails. On Windows the running `gubern.exe` is moved
aside to `vortex.exe.old` (Windows can rename a running program but not
overwrite it) and removed on a later run; a server that is running keeps the
old version until you restart it.

## Running as a service

```bash
# Linux (systemd)
sudo gubern service install
sudo systemctl start gubern

# print the unit file without installing
gubern service generate --format systemd
```

`service install` (systemd or OpenRC):

- creates the unprivileged system user `gubern` with home `/var/lib/gubern`
  if it doesn't exist (`useradd`, or `adduser` on BusyBox/Alpine). The home
  is the service's working directory, so it's also the agent's default
  workspace, and the user's state (keys, audit log, secrets) lives under it.
- grants the service only `CAP_NET_BIND_SERVICE`, so it can listen on :80
  and :443 without running as root.
- applies systemd hardening that doesn't get in Gubern's way: no new
  privileges, read-only kernel tunables/modules/logs/cgroups, no SUID/SGID,
  native syscalls only. The agent can still work on the host (with
  approval); it isn't sandboxed.

Self-update can't replace a binary the `gubern` user doesn't own (for
example `/usr/bin/gubern` from a package). Update packaged installs with your
package manager.

## First run

```bash
gubern setup          # choose an AI provider, optional Telegram, API key
gubern start          # start the server
gubern status         # confirm it is running
```

See [configuration.md](configuration.md) to tailor `gubern.cue`.

If something doesn't work, see [troubleshooting.md](troubleshooting.md).
