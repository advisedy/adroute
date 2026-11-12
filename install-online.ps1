# Fixed entry point for: irm .../install-online.ps1 | iex
& {
    $ErrorActionPreference = 'Stop'
    if ($PSVersionTable.PSVersion.Major -lt 7) {
        throw 'AdRoute online installation requires PowerShell 7+. Open PowerShell 7 (pwsh) and try again: https://aka.ms/powershell-release?tag=stable'
    }
    if (-not $IsWindows) { throw 'Use the Bash installer on Linux / WSL. Only Windows is supported by this installer.' }
    $repository = if ($env:ADROUTE_INSTALL_REPOSITORY_URL) { $env:ADROUTE_INSTALL_REPOSITORY_URL.TrimEnd('/') } else { 'https://github.com/advisedy/adroute' }
    $latest = Invoke-WebRequest -Uri "$repository/releases/latest"
    $release = $latest.BaseResponse.RequestMessage.RequestUri.AbsoluteUri
    $version = ($release -split '/')[-1]
    if ($version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$' -or $release -ne "$repository/releases/tag/$version") {
        throw 'Cannot determine the latest stable AdRoute release.'
    }
    $base = "$repository/releases/download/$version"
    $work = Join-Path ([IO.Path]::GetTempPath()) ('adroute-online-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work | Out-Null
    try {
        foreach ($name in @('SHA256SUMS', 'install-release.ps1')) {
            Invoke-WebRequest -Uri "$base/$name" -OutFile (Join-Path $work $name)
        }
        $lines = @(Get-Content -LiteralPath (Join-Path $work 'SHA256SUMS') | Where-Object { $_ -match '^([0-9a-fA-F]{64})  install-release\.ps1$' })
        if ($lines.Count -ne 1) { throw 'Missing or invalid installer checksum.' }
        $expected = ($lines[0] -split '  ')[0]
        $installer = Join-Path $work 'install-release.ps1'
        if ((Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash -ine $expected) {
            throw 'Installer checksum mismatch.'
        }
        $installDir = if ($env:ADROUTE_INSTALL_DIR) { $env:ADROUTE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\AdRoute' }
        Write-Host "Installing AdRoute $version"
        # Permit this downloaded installer in its own process without changing
        # the user's execution policy or loading their PowerShell profile.
        & (Join-Path $PSHOME 'pwsh.exe') -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $installer -ReleaseBaseURL $base -InstallDir $installDir
        if ($LASTEXITCODE -ne 0) { throw 'AdRoute installation failed. See the error above.' }
        if (-not (($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $installDir.TrimEnd('\') })) {
            $env:Path = "$installDir;$env:Path"
        }
    } finally {
        Remove-Item -LiteralPath $work -Recurse -Force
    }
}
