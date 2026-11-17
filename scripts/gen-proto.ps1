# Regenerate gRPC code from sdk/plugin/v1/plugin.proto with pinned tool versions.
# Note: this file is ASCII-only on purpose: Windows PowerShell 5.1 misreads UTF-8 without BOM.
#
# Pinned toolchain (ADR-0024; closes OQ-045):
#   protoc             36.2.0   distributed via the npm package "protoc", invoked through npx
#   protoc-gen-go      v1.36.6
#   protoc-gen-go-grpc v1.5.1
#
# Known quirk: `protoc --version` prints "libprotoc 36.2", but protoc-gen-go records the
# compiler's internal version "v7.36.2" in generated headers. Both refer to the same
# release; the mismatch is expected and documented in ADR-0024.
#
# Generated files are committed (plugin-spec/03 section 9). The script never installs
# or upgrades tools silently: on a missing or wrong version it prints the exact fix
# and exits non-zero.

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$ProtocVersion        = "36.2.0"
$ProtocHeaderVersion  = "v7.36.2"   # version string recorded in generated headers
$ProtocGenGoVersion   = "v1.36.6"
$ProtocGenGoGrpcVersion = "v1.5.1"
$Proto = "sdk/plugin/v1/plugin.proto"

function Fail([string]$Message) {
    Write-Host "gen-proto: FAIL" -ForegroundColor Red
    Write-Host $Message
    exit 1
}

# --- 1. protoc via npx (pinned version; cached after first download) ---
if (-not (Get-Command npx -ErrorAction SilentlyContinue)) {
    Fail "npx not found. Install Node.js (provides npx), or install protoc $ProtocVersion from https://github.com/protocolbuffers/protobuf/releases and replace the npx call below."
}

# --- 2. protoc plugins from Go (pinned) ---
$gobin = go env GOBIN
if (-not $gobin) { $gobin = Join-Path (go env GOPATH) "bin" }
$genGo     = Join-Path $gobin "protoc-gen-go.exe"
$genGoGrpc = Join-Path $gobin "protoc-gen-go-grpc.exe"
if (-not (Test-Path $genGo)) {
    Fail "missing $genGo`nInstall exactly:`n  go install google.golang.org/protobuf/cmd/protoc-gen-go@$ProtocGenGoVersion"
}
if (-not (Test-Path $genGoGrpc)) {
    Fail "missing $genGoGrpc`nInstall exactly:`n  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$ProtocGenGoGrpcVersion"
}
$verGo = (& $genGo --version)
if ($verGo -notmatch [regex]::Escape($ProtocGenGoVersion)) {
    Fail "protoc-gen-go version mismatch: got '$verGo', expected $ProtocGenGoVersion. Do NOT use another version; see ADR-0024 for the upgrade procedure."
}
$verGoGrpc = (& $genGoGrpc --version)
if ($verGoGrpc -notmatch [regex]::Escape($ProtocGenGoGrpcVersion.TrimStart("v"))) {
    Fail "protoc-gen-go-grpc version mismatch: got '$verGoGrpc', expected $ProtocGenGoGrpcVersion. Do NOT use another version; see ADR-0024 for the upgrade procedure."
}

# --- 3. generate (byte-identical with committed artifacts; ADR-0024) ---
# -I . keeps the source path "sdk/plugin/v1/plugin.proto" inside descriptors,
# which is what the committed plugin.pb.go was generated with.
npx -y protoc@$ProtocVersion -I . `
    --plugin=protoc-gen-go="$genGo" `
    --plugin=protoc-gen-go-grpc="$genGoGrpc" `
    --go_out=. --go_opt=paths=source_relative `
    --go-grpc_out=. --go-grpc_opt=paths=source_relative `
    $Proto
if ($LASTEXITCODE -ne 0) { Fail "protoc failed with exit code $LASTEXITCODE" }

# --- 4. verify generated headers match the pinned versions ---
foreach ($f in @("sdk/plugin/v1/plugin.pb.go", "sdk/plugin/v1/plugin_grpc.pb.go")) {
    if (-not (Test-Path $f)) { Fail "expected output missing: $f" }
    $head = (Get-Content $f -TotalCount 12) -join "`n"
    if ($head -notmatch [regex]::Escape("protoc-gen-go $ProtocGenGoVersion") -and
        $head -notmatch [regex]::Escape("protoc-gen-go-grpc $ProtocGenGoGrpcVersion")) {
        Fail "$f header does not record the pinned generator versions; regenerate with the pinned toolchain only."
    }
    if ($head -notmatch [regex]::Escape($ProtocHeaderVersion)) {
        Fail "$f header protoc version '$ProtocHeaderVersion' not found; the protoc package may have changed. Update the pinned versions together (ADR-0024)."
    }
}

Write-Host "gen-proto: ok (protoc $ProtocVersion / protoc-gen-go $ProtocGenGoVersion / protoc-gen-go-grpc $ProtocGenGoGrpcVersion)"
Write-Host "generated: sdk/plugin/v1/plugin.pb.go, sdk/plugin/v1/plugin_grpc.pb.go (committed; review the diff before committing)"
