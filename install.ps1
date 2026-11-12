[CmdletBinding()]
param(
    [string]$Binary,
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\AdRoute'),
    [switch]$NoModifyPath
)

$ErrorActionPreference = 'Stop'
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$destination = Join-Path $InstallDir 'adroute.exe'
$staged = $null
$buildDir = $null

try {
    # Refuse links/junctions so installation cannot redirect into another folder.
    $ancestor = $InstallDir
    while ($ancestor) {
        if ((Test-Path -LiteralPath $ancestor) -and
            ((Get-Item -LiteralPath $ancestor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Installation path contains a link or junction: $ancestor"
        }
        $ancestor = Split-Path -Parent $ancestor
    }
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $staged = Join-Path $InstallDir ('.adroute-' + [Guid]::NewGuid().ToString('N') + '.exe')
    if ($Binary) {
        Copy-Item -LiteralPath $Binary -Destination $staged
    } else {
        if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
            throw 'Go 1.23+ is required to build from source. Use -Binary with a prebuilt Windows executable.'
        }
        if (-not $PSScriptRoot -or -not (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'go.mod'))) {
            throw 'Run install.ps1 from the downloaded source tree, or use -Binary. Online release downloads are not configured.'
        }
        $buildDir = Join-Path ([IO.Path]::GetTempPath()) ('adroute-build-' + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $buildDir | Out-Null
        Push-Location $PSScriptRoot
        try {
            & go build -trimpath -o (Join-Path $buildDir 'adroute.exe') ./cmd/adroute
            if ($LASTEXITCODE -ne 0) { throw 'Go build failed; existing installation was left unchanged.' }
        } finally { Pop-Location }
        Copy-Item -LiteralPath (Join-Path $buildDir 'adroute.exe') -Destination $staged
    }
    & $staged --version
    if ($LASTEXITCODE -ne 0) { throw 'The supplied executable cannot run on this Windows system.' }
    # Windows locks a running executable. Do not stop the user's service or
    # remove the old binary on an unsuccessful update.
    if (Test-Path -LiteralPath $destination) {
        if ((Get-Item -LiteralPath $destination -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw 'Refusing to replace a linked executable.'
        }
        try { [IO.File]::Replace($staged, $destination, $null) }
        catch { throw "Cannot replace adroute.exe. Stop AdRoute with adroute stop before updating, then retry. $($_.Exception.Message)" }
    } else { [IO.File]::Move($staged, $destination) }
    $staged = $null

    if (-not $NoModifyPath) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $entries = @($userPath -split ';' | Where-Object { $_ })
        if (-not ($entries | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') })) {
            [Environment]::SetEnvironmentVariable('Path', (($entries + $InstallDir) -join ';'), 'User')
        }
        if (-not (($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') })) {
            $env:Path = "$InstallDir;$env:Path"
        }
    }
    Write-Host "Installed: $destination"
    Write-Host 'Start with: adroute start'
    Write-Host "Or run directly: & '$destination' start"
    Write-Host 'Open a new terminal if this script was run in a child PowerShell process.'
} finally {
    if ($staged -and (Test-Path -LiteralPath $staged)) { Remove-Item -LiteralPath $staged -Force }
    if ($buildDir -and (Test-Path -LiteralPath $buildDir)) { Remove-Item -LiteralPath $buildDir -Recurse -Force }
}
