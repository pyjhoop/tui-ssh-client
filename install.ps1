# Install ssh-client from a GitHub release.
#
#   irm https://raw.githubusercontent.com/pyjhoop/tui-ssh-client/main/install.ps1 | iex
#
# Environment:
#   $env:VERSION      tag to install (default: the latest release)
#   $env:INSTALL_DIR  where to put the binary
#                     (default: %LOCALAPPDATA%\Programs\ssh-client)
#
# Like install.sh, this verifies the checksum and refuses to continue without
# it, and it never edits the registry: if the install directory is not on PATH
# it prints the command to add it and leaves that to you.

$ErrorActionPreference = 'Stop'

$Repo = 'pyjhoop/tui-ssh-client'
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR }
              else { Join-Path $env:LOCALAPPDATA 'Programs\ssh-client' }

function Die($msg) { Write-Error "install.ps1: $msg"; exit 1 }

# ── platform ───────────────────────────────────────────────────────────────
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    'x86'   { Die 'ssh-client is not built for 32-bit Windows. Build it with: go install github.com/pyjhoop/tui-ssh-client@latest' }
    default { Die "unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)'" }
}

# ── version ────────────────────────────────────────────────────────────────
$version = $env:VERSION
if (-not $version) {
    $latest = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
    $version = $latest.tag_name
}
if (-not $version) { Die 'could not determine the latest version; set $env:VERSION' }

$num = $version -replace '^v', ''
$archive = "ssh-client_${num}_windows_${arch}.zip"
$base = "https://github.com/$Repo/releases/download/$version"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("ssh-client-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

try {
    Write-Host "downloading ssh-client $version (windows/$arch)"
    $zip = Join-Path $tmp $archive
    $sums = Join-Path $tmp 'checksums.txt'
    Invoke-WebRequest "$base/$archive" -OutFile $zip -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile $sums -UseBasicParsing

    # A download nobody checked is a download you have to trust blindly; there
    # is no switch to skip this.
    Write-Host 'verifying checksum'
    $line = Select-String -Path $sums -Pattern ([Regex]::Escape($archive) + '$') | Select-Object -First 1
    if (-not $line) { Die "$archive is not listed in checksums.txt" }
    $want = ($line.Line -split '\s+')[0]
    $got = (Get-FileHash -Algorithm SHA256 $zip).Hash
    if ($got -ine $want) { Die "checksum mismatch for $archive — not installing" }

    Expand-Archive -Path $zip -DestinationPath (Join-Path $tmp 'x') -Force
    $bin = Join-Path $tmp 'x\ssh-client.exe'
    if (-not (Test-Path $bin)) { Die "no ssh-client.exe inside $archive" }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    Copy-Item $bin (Join-Path $InstallDir 'ssh-client.exe') -Force
    Write-Host "installed $InstallDir\ssh-client.exe"
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($userPath -split ';' -contains $InstallDir) {
    & (Join-Path $InstallDir 'ssh-client.exe') --version
} else {
    Write-Host ''
    Write-Host "$InstallDir is not on your PATH. Add it with:"
    Write-Host ''
    Write-Host "    [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path','User') + ';$InstallDir', 'User')"
    Write-Host ''
    Write-Host "then open a new terminal, or run it directly: $InstallDir\ssh-client.exe"
}
