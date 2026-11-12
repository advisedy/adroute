[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\AdRoute'),
    [switch]$NoModifyPath,
    [string]$ReleaseBaseURL = 'https://github.com/advisedy/adroute/releases/download/v0.1.0'
)

$ErrorActionPreference = 'Stop'
$version = '0.1.0'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
# PROCESSOR_ARCHITEW6432 identifies the OS architecture under an emulated shell.
$architecture = $env:PROCESSOR_ARCHITEW6432
if (-not $architecture) { $architecture = $env:PROCESSOR_ARCHITECTURE }
switch ($architecture.ToUpperInvariant()) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { throw "Unsupported Windows architecture: $architecture" }
}
$asset = "adroute_${version}_windows_${arch}.exe"
$work = Join-Path ([IO.Path]::GetTempPath()) ('adroute-download-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseBaseURL/SHA256SUMS" -OutFile (Join-Path $work 'SHA256SUMS')
    $checksums = @{}
    foreach ($line in Get-Content -LiteralPath (Join-Path $work 'SHA256SUMS')) {
        if ($line -match '^([0-9a-fA-F]{64})  (\S+)$') { $checksums[$Matches[2]] = $Matches[1] }
    }
    foreach ($name in @($asset, 'install.ps1')) {
        if (-not $checksums.ContainsKey($name)) { throw "Missing checksum: $name" }
        $download = Join-Path $work $name
        Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseBaseURL/$name" -OutFile $download
        # Use .NET directly; module discovery can differ when Windows
        # PowerShell is launched from PowerShell 7 or another host.
        $algorithm = [Security.Cryptography.SHA256]::Create()
        $stream = [IO.File]::OpenRead($download)
        try { $actualHash = [BitConverter]::ToString($algorithm.ComputeHash($stream)).Replace('-', '') }
        finally { $stream.Dispose(); $algorithm.Dispose() }
        if ($actualHash -ine $checksums[$name]) {
            throw "Checksum mismatch: $name"
        }
    }
    $binary = Join-Path $work $asset
    $actualVersion = & $binary --version
    if ($LASTEXITCODE -ne 0 -or $actualVersion -ne $version) { throw 'Release version mismatch.' }
    & (Join-Path $work 'install.ps1') -Binary $binary -InstallDir $InstallDir -NoModifyPath:$NoModifyPath
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force
}
