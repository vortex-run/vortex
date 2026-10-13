#Requires -Version 7
<#
.SYNOPSIS
    Brings up a self-contained VORTEX demo environment for screenshots and screen recordings.

.DESCRIPTION
    Recording a demo against a normal install produces bad footage: the checked-in
    vortex.cue points at hosts that do not resolve, so every route renders red, and
    a real install's audit log, secrets, and API keys are all on camera.

    This script starts a demo that is isolated from the real installation. Every
    persistent store is redirected into -DataDir via the VORTEX_* environment
    overrides, so nothing here touches %LOCALAPPDATA%\vortex and the demo can be
    reset by deleting one folder.

.PARAMETER DataDir
    Where the demo's isolated state lives. Deleted and recreated unless -Keep is set.

.PARAMETER Keep
    Reuse existing demo state instead of starting clean. Useful for a second take.

.PARAMETER NoTraffic
    Skip the background traffic generator. Metrics and logs will be empty.

.EXAMPLE
    pwsh scripts/demo/demo.ps1
    Starts everything, then prints the shot list. Ctrl+C stops it all.
#>
[CmdletBinding()]
param(
    [string]$DataDir = (Join-Path $env:TEMP 'vortex-demo'),
    [switch]$Keep,
    [switch]$NoTraffic
)

$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..' '..')).Path
$bin = Join-Path $repo 'bin'
$vortex = Join-Path $bin 'vortex.exe'
$backends = Join-Path $bin 'demo-backends.exe'
$config = Join-Path $PSScriptRoot 'demo.cue'

function Write-Step { param($m) Write-Host "  $m" -ForegroundColor Cyan }
function Write-Ok { param($m) Write-Host "  $m" -ForegroundColor Green }

Write-Host ''
Write-Host 'VORTEX demo environment' -ForegroundColor White
Write-Host '-----------------------' -ForegroundColor DarkGray

Push-Location $repo
try {
    if (-not (Test-Path $vortex)) {
        Write-Step 'building vortex...'
        & go build -o $vortex ./cmd/vortex
        if ($LASTEXITCODE -ne 0) { throw 'vortex build failed' }
    }
    if (-not (Test-Path $backends)) {
        Write-Step 'building demo backends...'
        # The demo build tag keeps this throwaway main package out of
        # `go build ./...` and out of CI, so it cannot affect a real build.
        & go build -tags demo -o $backends ./scripts/demo/backends
        if ($LASTEXITCODE -ne 0) { throw 'demo backends build failed' }
    }
}
finally { Pop-Location }

if ((Test-Path $DataDir) -and -not $Keep) {
    Remove-Item -Recurse -Force $DataDir
}
New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

# Every store VORTEX persists is redirected here. Without these the demo would
# inherit the real installation's audit log, which was written under a different
# cluster name and therefore fails chain verification - printing a red ERROR on
# the first frame of the recording.
$env:VORTEX_AUDIT_LOG = Join-Path $DataDir 'audit.log'
$env:VORTEX_APIKEY_STORE = Join-Path $DataDir 'apikeys.json'
$env:VORTEX_SECRET_STORE = Join-Path $DataDir 'secrets'
$env:VORTEX_PLUGIN_DIR = Join-Path $DataDir 'plugins'
$env:VORTEX_NAMESPACE_STORE = Join-Path $DataDir 'namespaces.json'
$env:VORTEX_MTLS_STORE = Join-Path $DataDir 'mtls'
Write-Ok "isolated state -> $DataDir"

$procs = @()
try {
    Write-Step 'starting demo backends (127.0.0.1:3000/4000/4100, tcp 15433)...'
    $procs += Start-Process -FilePath $backends -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $DataDir 'backends.log') `
        -RedirectStandardError (Join-Path $DataDir 'backends.err')
    Start-Sleep -Seconds 2

    Write-Step 'starting vortex...'
    $procs += Start-Process -FilePath $vortex -PassThru -WindowStyle Hidden `
        -ArgumentList @('start', '--config', $config) `
        -RedirectStandardOutput (Join-Path $DataDir 'vortex.log') `
        -RedirectStandardError (Join-Path $DataDir 'vortex.err')

    # Poll the API rather than sleeping a fixed amount: startup time varies with
    # certificate generation on a cold data directory.
    $ready = $false
    foreach ($i in 1..40) {
        Start-Sleep -Milliseconds 500
        try {
            Invoke-WebRequest -Uri 'http://127.0.0.1:9090/health' -TimeoutSec 2 -UseBasicParsing | Out-Null
            $ready = $true; break
        }
        catch { }
    }
    if (-not $ready) {
        Write-Host '  vortex did not become ready; last log lines:' -ForegroundColor Red
        Get-Content (Join-Path $DataDir 'vortex.log') -Tail 20
        throw 'startup failed'
    }
    Write-Ok 'vortex ready on :9090'

    # Real encrypted entries, so the Secrets view is not an empty table on camera.
    foreach ($s in @(
            @{ k = 'db_password'; v = 'demo-not-a-real-password' },
            @{ k = 'jwt_secret'; v = 'demo-not-a-real-secret' })) {
        & $vortex secret set $s.k $s.v --config $config 2>&1 | Out-Null
    }
    Write-Ok 'seeded 2 encrypted secrets'

    if (-not $NoTraffic) {
        Write-Step 'generating background traffic (metrics + logs)...'
        Start-Job -ScriptBlock {
            # -SkipCertificateCheck: the demo uses the internal (self-signed) CA.
            while ($true) {
                foreach ($r in @(
                        @{ u = 'http://127.0.0.1:8080/'; h = 'staging.localhost' },
                        @{ u = 'https://127.0.0.1:8443/'; h = 'demo.localhost' },
                        @{ u = 'https://127.0.0.1:8444/api/health'; h = 'api.demo.localhost' })) {
                    try {
                        Invoke-WebRequest -Uri $r.u -Headers @{ Host = $r.h } `
                            -SkipCertificateCheck -TimeoutSec 3 -UseBasicParsing | Out-Null
                    }
                    catch { }
                }
                Start-Sleep -Milliseconds 700
            }
        } | Out-Null
        Write-Ok 'traffic running'
    }

    Write-Host ''
    Write-Host 'Ready to record.' -ForegroundColor White
    Write-Host ''
    Write-Host '  In a SECOND terminal (sized ~100x30, font 16pt+), run:' -ForegroundColor DarkGray
    Write-Host ''
    Write-Host ('    $env:VORTEX_APIKEY_STORE = "{0}"' -f (Join-Path $DataDir 'apikeys.json'))
    Write-Host '    .\bin\vortex.exe status'
    Write-Host '    .\bin\vortex.exe check --config scripts\demo\demo.cue'
    Write-Host '    .\bin\vortex.exe ui'
    Write-Host ''
    Write-Host '  Browser:  http://localhost:9090/dashboard/' -ForegroundColor DarkGray
    Write-Host '  Proxied:  curl -H "Host: staging.localhost" http://127.0.0.1:8080/' -ForegroundColor DarkGray
    Write-Host ''
    Write-Host '  Ctrl+C here stops everything.' -ForegroundColor DarkGray
    Write-Host ''

    while ($true) { Start-Sleep -Seconds 1 }
}
finally {
    Write-Host ''
    Write-Step 'stopping demo...'
    Get-Job | Remove-Job -Force -ErrorAction SilentlyContinue
    foreach ($p in $procs) {
        if ($p -and -not $p.HasExited) {
            # /T kills the whole tree: vortex spawns agent subprocesses that a
            # bare Stop-Process would orphan.
            & taskkill /T /F /PID $p.Id 2>&1 | Out-Null
        }
    }
    Write-Ok 'stopped'
}
