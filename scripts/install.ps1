<#
.SYNOPSIS
  Installs the swarm desktop app (GUI) on Windows.

.DESCRIPTION
  Downloads the latest NSIS installer (unsigned) from GitHub Releases and
  runs it. The installer bootstraps the WebView2 runtime if missing, drops Start
  Menu + Desktop shortcuts, and registers an uninstaller.

  Usage:  irm https://raw.githubusercontent.com/corpeningc/swarm/main/scripts/install.ps1 | iex
#>
[CmdletBinding()]
param(
    # Show the install wizard instead of installing silently.
    [switch]$Interactive,
    # Install a specific version, e.g. "0.1.0". Defaults to the latest release.
    [string]$Version
)

$ErrorActionPreference = 'Stop'
$repo = 'corpeningc/swarm'

function Get-Asset {
    $url = if ($Version) {
        "https://api.github.com/repos/$repo/releases/tags/v$Version"
    } else {
        "https://api.github.com/repos/$repo/releases/latest"
    }
    $release = Invoke-RestMethod -Uri $url -Headers @{ 'User-Agent' = 'swarm-install' }
    $asset = $release.assets | Where-Object { $_.name -like '*windows-amd64-setup.exe' } | Select-Object -First 1
    if (-not $asset) {
        throw "No Windows installer found in release $($release.tag_name)."
    }
    [pscustomobject]@{ Tag = $release.tag_name; Name = $asset.name; Url = $asset.browser_download_url }
}

$asset = Get-Asset
Write-Host "Downloading swarm desktop $($asset.Tag)..." -ForegroundColor Cyan

$out = Join-Path ([IO.Path]::GetTempPath()) $asset.Name
Invoke-WebRequest -Uri $asset.Url -OutFile $out -UseBasicParsing

Write-Host "Running installer (accept the UAC prompt)..." -ForegroundColor Cyan
# /S is NSIS's silent flag; the wizard adds a directory-choice page.
$start = @{ FilePath = $out; Wait = $true; PassThru = $true }
if (-not $Interactive) { $start.ArgumentList = '/S' }
$proc = Start-Process @start
Remove-Item $out -ErrorAction SilentlyContinue

if ($proc.ExitCode -ne 0) {
    throw "Installer exited with code $($proc.ExitCode)."
}

Write-Host "swarm is installed. Launch it from the Start Menu." -ForegroundColor Green
