# Installation

VORTEX ships as a single static binary with no runtime dependencies. Pick the
method that fits your platform.

## Linux / macOS — one-line installer

```bash
curl -fsSL https://vortex.run/install | sh
```

The installer detects your OS/arch, downloads the matching release, verifies
its SHA-256 against `checksums.txt`, installs the binary to
`/usr/local/bin/vortex`, and seeds an example config at
`/etc/vortex/vortex.cue`. Re-running it upgrades in place and never clobbers an
existing config.

Options:

```bash
# install a specific version
curl -fsSL https://vortex.run/install | sh -s -- --version v0.2.0

# install to a custom directory
curl -fsSL https://vortex.run/install | sh -s -- --install-dir "$HOME/.local/bin"
```

Environment overrides: `VORTEX_VERSION`, `VORTEX_INSTALL_DIR`,
`VORTEX_CONFIG_DIR`.

## Manual download

Grab the archive for your platform from the
[Releases](https://github.com/vortex-run/vortex/releases) page:

| Platform | Asset |
|---|---|
| Linux x86-64 | `vortex_linux_amd64.tar.gz` |
| Linux ARM64 | `vortex_linux_arm64.tar.gz` |
| macOS Apple Silicon | `vortex_darwin_arm64.tar.gz` |
| macOS Intel | `vortex_darwin_amd64.tar.gz` |
| Windows x86-64 | `vortex_windows_amd64.zip` |

```bash
tar -xzf vortex_linux_amd64.tar.gz
sudo install -m 0755 vortex /usr/local/bin/vortex
vortex version
```

## Windows

1. Download `vortex_windows_amd64.zip` and extract `vortex.exe`.
2. Move it to `C:\Program Files\vortex\vortex.exe`.
3. Install it as a service:

   ```powershell
   vortex service install
   ```

## Verifying a release

Every release publishes `checksums.txt` and (when signing is enabled) an
Ed25519 signature `checksums.txt.sig`. Verify before trusting a binary:

```bash
# verify the running binary against the published release
vortex verify

# verify every artifact of a tag end-to-end
./scripts/verify-release.sh v0.2.0
```

`vortex self-update` performs the same signature check automatically before
replacing the binary.

## Running as a service

```bash
# Linux (systemd)
sudo vortex service install
sudo systemctl start vortex

# print the unit file without installing
vortex service generate --format systemd
```

## First run

```bash
vortex setup          # choose an AI provider, optional Telegram, API key
vortex start          # start the server
vortex status         # confirm it is running
```

See [configuration.md](configuration.md) to tailor `vortex.cue`.
