#Requires -Version 5.0
#
# VORTEX PATH installer (Windows).
#
# Adds the repo's bin/ directory to the *user* PATH permanently, so `vortex`
# (and `vortex code`) work from any directory. Run once:
#
#   .\scripts\install-path.ps1
#
# Re-running is safe: it is a no-op if bin/ is already on PATH.

$ErrorActionPreference = "Stop"

# Resolve <repo>/bin from this script's location (scripts/ -> repo root -> bin).
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$binPath = Join-Path (Split-Path -Parent $scriptDir) "bin"

$currentPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if ($null -eq $currentPath) { $currentPath = "" }

# Compare against the existing entries exactly (avoid substring false positives).
$entries = $currentPath -split ";" | Where-Object { $_ -ne "" }
if ($entries -contains $binPath) {
    Write-Host "✓ Already in PATH: $binPath"
    return
}

$newPath = if ($currentPath -eq "") { $binPath } else { "$currentPath;$binPath" }
[Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
Write-Host "✓ Added $binPath to user PATH"
Write-Host "  Restart your terminal to use 'vortex' from anywhere."
