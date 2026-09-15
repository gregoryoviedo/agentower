#requires -Version 5.1
<#
.SYNOPSIS
  Builds the Windows wrapper (Agentower.exe) including the embedded
  remote-bot.exe.

.DESCRIPTION
  1. Compiles cmd/remote-bot for windows/amd64 into
     windows/Agentower/Resources/remote-bot.exe.
  2. Publishes the WinForms wrapper as a single-file, self-contained
     Agentower.exe into dist/.

  Requires Go 1.22+ and the .NET 8 SDK on PATH (or in their default
  install locations).
#>
[CmdletBinding()]
param(
    [string]$Configuration = "Release",
    [string]$RuntimeIdentifier = "win-x64"
)

$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Split-Path -Parent $scriptDir
$project = Join-Path $scriptDir "Agentower\Agentower.csproj"
$resources = Join-Path $scriptDir "Agentower\Resources"
$dist = Join-Path $repoRoot "dist"

function Resolve-Tool {
    param([string]$Name, [string[]]$Fallbacks)
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    foreach ($candidate in $Fallbacks) {
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    throw "No se encontró '$Name'. Instálalo y vuelve a intentarlo."
}

$go = Resolve-Tool -Name "go" -Fallbacks @(
    "C:\Program Files\Go\bin\go.exe",
    "C:\Go\bin\go.exe"
)
$dotnet = Resolve-Tool -Name "dotnet" -Fallbacks @(
    "C:\Program Files\dotnet\dotnet.exe",
    (Join-Path $env:LOCALAPPDATA "Microsoft\dotnet\dotnet.exe")
)

Write-Host "==> Compilando remote-bot.exe (windows/amd64)…" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path $resources | Out-Null
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
& $go build -trimpath -ldflags="-s -w" -o (Join-Path $resources "remote-bot.exe") (Join-Path $repoRoot "cmd\remote-bot")
if ($LASTEXITCODE -ne 0) { throw "go build falló" }
Write-Host "    ok: $((Get-Item (Join-Path $resources 'remote-bot.exe')).Length) bytes"

Write-Host "==> Publicando Agentower.exe (single-file, self-contained)…" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path $dist | Out-Null
& $dotnet publish $project `
    -c $Configuration `
    -r $RuntimeIdentifier `
    --self-contained true `
    -p:PublishSingleFile=true `
    -p:IncludeAllContentForSelfExtract=true `
    -o $dist
if ($LASTEXITCODE -ne 0) { throw "dotnet publish falló" }

$exe = Join-Path $dist "Agentower.exe"
Write-Host ""
Write-Host "✓ Listo: $exe" -ForegroundColor Green
Write-Host "  $([math]::Round((Get-Item $exe).Length / 1MB, 1)) MB"
Write-Host ""
Write-Host "Para usarlo:"
Write-Host "  .\dist\Agentower.exe"
Write-Host "  Click izquierdo en el icono del área de notificación: toggle + acciones."
Write-Host "  Click derecho: menú contextual."
