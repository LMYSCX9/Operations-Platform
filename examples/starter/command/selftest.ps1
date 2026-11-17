# Local self-test for the command starter. ASCII-only (PowerShell 5.1 safety).
# Builds the plugin into bin/ (the manifest entry points there) and checks the
# success path and the structured-error paths over the stdio protocol.
# stdin is fed via a temp file through cmd redirection: PowerShell 5.1 native
# piping may inject a UTF-8 BOM into the stream, which would corrupt the request.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

go build -o bin/starter-command.exe .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$reqFile = Join-Path $env:TEMP "starter-command-selftest.json"

function Invoke-Plugin([string]$RequestJson) {
    # UTF-8 without BOM; plugin reads one JSON line from stdin.
    [IO.File]::WriteAllText($reqFile, $RequestJson + "`n", (New-Object System.Text.UTF8Encoding $false))
    $out = & cmd /c ".\bin\starter-command.exe < `"$reqFile`""
    return ($out -join "`n")
}

# 1) success path: status=ok, message contains the name
$resp = Invoke-Plugin '{"contract_version":"1.2","capability":"starter-command.greet","input":{"name":"ops"},"context":{"invocation_id":"t1","deadline_ms":3000}}' | ConvertFrom-Json
if ($resp.status -ne "ok" -or $resp.output.message -notmatch "ops") {
    Write-Host "selftest FAIL: unexpected ok-path response: $($resp | ConvertTo-Json -Compress)" -ForegroundColor Red
    exit 1
}

# 2) error path: missing required field -> input_invalid (exit code 2)
$raw = Invoke-Plugin '{"contract_version":"1.2","capability":"starter-command.greet","input":{},"context":{"invocation_id":"t2","deadline_ms":3000}}'
$code = $LASTEXITCODE
$errCode = ($raw | ConvertFrom-Json).error.code
if ($errCode -ne "input_invalid" -or $code -ne 2) {
    Write-Host "selftest FAIL: expected input_invalid with exit 2, got code=$errCode exit=$code" -ForegroundColor Red
    exit 1
}

# 3) unknown capability -> capability_unknown (exit code 3)
$raw = Invoke-Plugin '{"contract_version":"1.2","capability":"starter-command.nope","input":{},"context":{"invocation_id":"t3","deadline_ms":3000}}'
$code = $LASTEXITCODE
$errCode = ($raw | ConvertFrom-Json).error.code
if ($errCode -ne "capability_unknown" -or $code -ne 3) {
    Write-Host "selftest FAIL: expected capability_unknown with exit 3, got code=$errCode exit=$code" -ForegroundColor Red
    exit 1
}

Remove-Item $reqFile -ErrorAction SilentlyContinue
Write-Host "selftest ok: ok-path / input_invalid(2) / capability_unknown(3)"
