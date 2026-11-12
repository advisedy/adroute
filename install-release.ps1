[CmdletBinding()]
param(
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\AdRoute'),
    [switch]$NoModifyPath,
    [string]$ReleaseBaseURL = 'https://github.com/advisedy/adroute/releases/download/v0.1.0',
    [int]$MaxAttempts = 3,
    [int]$RetryDelaySeconds = 2,
    [string]$Proxy
)

$ErrorActionPreference = 'Stop'
$version = '0.1.0'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$proxyUrl = if ($Proxy) { $Proxy } elseif ($env:ADROUTE_INSTALL_PROXY) { $env:ADROUTE_INSTALL_PROXY } else { $null }

# Release assets are several megabytes and are served from a different host
# than github.com, so a single dropped connection otherwise fails the whole
# installation. Retry with bounded backoff and resume from the bytes already
# on disk; a server that ignores the Range header restarts the file instead.
function Save-RemoteFile {
    param(
        [string]$Uri,
        [string]$OutFile
    )
    $lastError = $null
    $permanent = $false
    for ($attempt = 1; $attempt -le $MaxAttempts; $attempt++) {
        $received = 0
        if (Test-Path -LiteralPath $OutFile) { $received = (Get-Item -LiteralPath $OutFile).Length }
        $current = $Uri
        try {
            for ($hop = 0; $hop -lt 5; $hop++) {
                $request = [Net.WebRequest]::Create($current)
                $request.Method = 'GET'
                # Follow redirects by hand so the Range header is repeated on
                # the release CDN host and a truncated transfer can resume.
                $request.AllowAutoRedirect = $false
                $request.Timeout = 20000
                $request.ReadWriteTimeout = 120000
                $request.UserAgent = "AdRoute/$version"
                if ($proxyUrl) { $request.Proxy = [Net.WebProxy]::new($proxyUrl) }
                if ($received -gt 0) { [void]$request.AddRange($received) }

                $response = $null
                try {
                    $response = $request.GetResponse()
                } catch [Net.WebException] {
                    $failed = $_.Exception.Response
                    if (-not $failed) { throw }
                    $failedStatus = [int]$failed.StatusCode
                    $failed.Close()
                    # Another attempt cannot fix a missing or forbidden asset.
                    if ($failedStatus -ne 408 -and $failedStatus -ne 429 -and $failedStatus -lt 500) {
                        $permanent = $true
                        throw "HTTP $failedStatus from $current"
                    }
                    throw
                }

                $status = [int]$response.StatusCode
                if ($status -eq 301 -or $status -eq 302 -or $status -eq 303 -or $status -eq 307 -or $status -eq 308) {
                    $location = $response.Headers.Get('Location')
                    $response.Close()
                    if (-not $location) { throw "Redirect without Location from $current" }
                    $current = [Uri]::new([Uri]::new($current), $location).AbsoluteUri
                    continue
                }
                $contentRange = $response.Headers.Get('Content-Range')
                $rangeStart = -1
                if ($contentRange -match '^bytes (\d+)-') { $rangeStart = [int64]$Matches[1] }
                # Append only when the server proves it continues exactly where
                # the file ends; anything else restarts so bytes cannot shift.
                if ($status -eq 206 -and $received -gt 0 -and $rangeStart -eq $received) {
                    $mode = [IO.FileMode]::Append
                } elseif ($status -eq 200 -or $status -eq 206) {
                    # Range was ignored or not honoured: start over rather than
                    # append onto bytes we cannot line up with this response.
                    $received = 0
                    $mode = [IO.FileMode]::Create
                } else {
                    $response.Close()
                    throw "Unexpected HTTP $status from $current"
                }
                # .NET Core sometimes ends a truncated stream without throwing,
                # so compare against the announced length and retry on a shortfall.
                $expectedTotal = $null
                $contentRange = $response.Headers.Get('Content-Range')
                if ($contentRange -match '/(\d+)$') {
                    $expectedTotal = [int64]$Matches[1]
                } elseif ($response.ContentLength -ge 0) {
                    $expectedTotal = $received + $response.ContentLength
                }

                $stream = $response.GetResponseStream()
                $file = [IO.File]::Open($OutFile, $mode, [IO.FileAccess]::Write, [IO.FileShare]::None)
                try {
                    $buffer = New-Object byte[] 131072
                    while (($read = $stream.Read($buffer, 0, $buffer.Length)) -gt 0) {
                        $file.Write($buffer, 0, $read)
                        $received += $read
                    }
                } finally {
                    $file.Dispose()
                    $stream.Dispose()
                    $response.Close()
                }
                if ($null -ne $expectedTotal -and $received -ne $expectedTotal) {
                    throw "Transfer ended early with $received of $expectedTotal bytes"
                }
                return
            }
            throw "Too many redirects from $Uri"
        } catch {
            $lastError = $_
            if ($permanent -or $attempt -ge $MaxAttempts) { break }
            $have = 0
            if (Test-Path -LiteralPath $OutFile) { $have = (Get-Item -LiteralPath $OutFile).Length }
            $delay = $RetryDelaySeconds * $attempt
            Write-Warning "Download interrupted (attempt $attempt/$MaxAttempts, $have bytes kept): $current. Retrying in $delay s."
            Start-Sleep -Seconds $delay
        }
    }

    if ($permanent) {
        throw "Download failed: $Uri`n  reason: $($lastError.Exception.Message)`nCheck the version and asset name; retrying will not help."
    }
    $asset = Split-Path -Leaf $Uri
    throw @"
Download failed after $MaxAttempts attempts: $Uri
  reason: $($lastError.Exception.Message)

Check the network connection, then retry. If this machine cannot reach
GitHub release downloads, enable a proxy for this session and retry:
  `$env:ADROUTE_INSTALL_PROXY = 'http://127.0.0.1:7890'
or download the assets manually from:
  $($Uri.Substring(0, $Uri.LastIndexOf('/')))
and install the executable without downloading anything else:
  powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Binary .\$asset
"@
}

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
    Save-RemoteFile "$ReleaseBaseURL/SHA256SUMS" (Join-Path $work 'SHA256SUMS')
    $checksums = @{}
    foreach ($line in Get-Content -LiteralPath (Join-Path $work 'SHA256SUMS')) {
        if ($line -match '^([0-9a-fA-F]{64})  (\S+)$') { $checksums[$Matches[2]] = $Matches[1] }
    }
    foreach ($name in @($asset, 'install.ps1')) {
        if (-not $checksums.ContainsKey($name)) { throw "Missing checksum: $name" }
        $download = Join-Path $work $name
        Save-RemoteFile "$ReleaseBaseURL/$name" $download
        # Use .NET directly; module discovery can differ when Windows
        # PowerShell is launched from PowerShell 7 or another host.
        $algorithm = [Security.Cryptography.SHA256]::Create()
        $stream = [IO.File]::OpenRead($download)
        try { $actualHash = [BitConverter]::ToString($algorithm.ComputeHash($stream)).Replace('-', '') }
        finally { $stream.Dispose(); $algorithm.Dispose() }
        if ($actualHash -ine $checksums[$name]) {
            $size = (Get-Item -LiteralPath $download).Length
            throw "Checksum mismatch: $name ($size bytes downloaded). Delete the file and retry; if it keeps happening, the network is altering the download."
        }
    }
    $binary = Join-Path $work $asset
    $actualVersion = & $binary --version
    if ($LASTEXITCODE -ne 0 -or $actualVersion -ne $version) { throw 'Release version mismatch.' }
    & (Join-Path $work 'install.ps1') -Binary $binary -InstallDir $InstallDir -NoModifyPath:$NoModifyPath
} finally {
    Remove-Item -LiteralPath $work -Recurse -Force
}
