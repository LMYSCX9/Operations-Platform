# Local end-to-end self-test for the daemon starter. ASCII-only (PowerShell 5.1 safety).
# Builds the plugin into bin/ and a throwaway harness binary into TEMP, then runs the
# harness: it generates one-shot test certs, writes the per-host config, starts the
# plugin with base-injected flags, and drives Ready/GetVersion/Invoke/Shutdown over mTLS.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

go build -o bin/starter-daemon.exe .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$harness = Join-Path $env:TEMP "starter-daemon-selftest.exe"
go build -o $harness ./selftest
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& $harness "bin/starter-daemon.exe"
$code = $LASTEXITCODE
Remove-Item $harness -ErrorAction SilentlyContinue
exit $code
