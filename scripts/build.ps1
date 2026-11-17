# Build the single binary. CGO is always disabled so the artifact has no cgo dependency (ADR-0007).
# Note: this file is ASCII-only on purpose: Windows PowerShell 5.1 misreads UTF-8 without BOM.
#
# Usage:
#   .\scripts\build.ps1
#   .\scripts\build.ps1 -GOOS linux -GOARCH amd64 -Output bin/linux
#   .\scripts\build.ps1 -Version v0.1.0
param(
    [string]$Output = "bin",
    [string]$Version = "",
    [string]$GOOS = "",
    [string]$GOARCH = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

# Runs git and returns the first output line, or $null when git is unavailable or
# the command fails (e.g. the directory is not a git repository).
function Get-GitOutput([string[]]$GitArgs) {
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { return $null }
    $previous = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $output = & git @GitArgs 2>$null
        if ($LASTEXITCODE -ne 0) { return $null }
        return ($output | Select-Object -First 1)
    } finally {
        $ErrorActionPreference = $previous
    }
}

# prototype/index.html is the single source of truth for the page. go:embed cannot
# reference files outside the package directory, so the build syncs a copy first.
# If the file is missing, go build fails instead of shipping an empty UI (ADR-0001).
Copy-Item -Path (Join-Path $root "prototype/index.html") -Destination (Join-Path $root "web/dist/index.html") -Force

if (-not $Version) {
    $Version = "dev"
    $described = Get-GitOutput @("describe", "--tags", "--always", "--dirty")
    if ($described) { $Version = $described }
}

$commit = "none"
$currentCommit = Get-GitOutput @("rev-parse", "--short", "HEAD")
if ($currentCommit) { $commit = $currentCommit }

$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

$env:CGO_ENABLED = "0"
if ($GOOS) { $env:GOOS = $GOOS }
if ($GOARCH) { $env:GOARCH = $GOARCH }

$targetOS = if ($GOOS) { $GOOS } else { (go env GOOS).Trim() }
$ext = if ($targetOS -eq "windows") { ".exe" } else { "" }

New-Item -ItemType Directory -Force -Path $Output | Out-Null
$target = Join-Path $Output "operations-platform$ext"

go build -trimpath -ldflags "-s -w -X main.version=$Version -X main.commit=$commit -X main.buildTime=$buildTime" -o $target ./cmd/ops-platform
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

# Example plugin: the executable must live inside the plugin directory because the
# manifest entry path is validated against real files. Build it there, then copy it
# next to the main artifact.
$demoBinDir = Join-Path $root "examples/plugins/demo-echo/bin"
New-Item -ItemType Directory -Force -Path $demoBinDir | Out-Null
$demoTarget = Join-Path $demoBinDir "demo-echo$ext"
go build -trimpath -ldflags "-s -w" -o $demoTarget ./examples/plugins/demo-echo
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Copy-Item -Path $demoTarget -Destination (Join-Path $Output "demo-echo$ext") -Force

Write-Host "build ok: $target (CGO_ENABLED=0)"
Write-Host "plugin ok: $demoTarget"
