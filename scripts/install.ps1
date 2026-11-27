#Requires -Version 5.1
[CmdletBinding()]
param(
    [string]$Version,
    [string]$BinDir,
    [switch]$Help
)

function Get-ZorveusArchitecture {
    # PROCESSOR_ARCHITEW6432 identifies the native CPU in an emulated shell.
    $architecture = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrWhiteSpace($architecture)) {
        $architecture = $env:PROCESSOR_ARCHITECTURE
    }
    switch ($architecture) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { throw 'Supported processors are x86-64 and ARM64.' }
    }
}

function Get-ZorveusDownload {
    param([uri]$Uri, [string]$Destination)

    Add-Type -AssemblyName System.Net.Http
    $handler = New-Object -TypeName System.Net.Http.HttpClientHandler
    # .NET Framework permits HTTPS-to-HTTP redirects. Check every hop ourselves.
    $handler.AllowAutoRedirect = $false
    $client = New-Object -TypeName System.Net.Http.HttpClient -ArgumentList $handler
    $client.Timeout = [TimeSpan]::FromSeconds(300)
    try {
        for ($redirect = 0; $redirect -le 10; $redirect++) {
            if (-not $Uri.IsAbsoluteUri -or $Uri.Scheme -ne 'https') {
                throw 'Release downloads and redirects must use HTTPS.'
            }
            $response = $client.GetAsync($Uri).GetAwaiter().GetResult()
            try {
                if ([int]$response.StatusCode -in @(301, 302, 303, 307, 308)) {
                    if ($null -eq $response.Headers.Location) {
                        throw 'The download redirect has no destination.'
                    }
                    $Uri = New-Object -TypeName System.Uri -ArgumentList $Uri, $response.Headers.Location
                    continue
                }
                $null = $response.EnsureSuccessStatusCode()
                $bytes = $response.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
                [IO.File]::WriteAllBytes($Destination, $bytes)
                return
            } finally {
                $response.Dispose()
            }
        }
        throw 'Too many download redirects.'
    } finally {
        $client.Dispose()
    }
}

function Add-ZorveusPathEntry {
    param([string]$Path, [string]$Directory)

    foreach ($entry in ($Path -split ';')) {
        $expanded = [Environment]::ExpandEnvironmentVariables($entry.Trim().Trim('"'))
        if ($expanded.TrimEnd('\', '/') -ieq $Directory.TrimEnd('\', '/')) {
            return $Path
        }
    }
    if ([string]::IsNullOrEmpty($Path)) { return $Directory }
    return "$Directory;$Path"
}

function Update-ZorveusPath {
    param([string]$Directory)

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $updatedPath = Add-ZorveusPathEntry -Path $userPath -Directory $Directory
    if ($updatedPath -cne $userPath) {
        [Environment]::SetEnvironmentVariable('Path', $updatedPath, 'User')
    }
    $env:Path = Add-ZorveusPathEntry -Path $env:Path -Directory $Directory
}

function Install-Zorveus {
    [CmdletBinding()]
    param([string]$Version, [string]$BinDir)

    $ErrorActionPreference = 'Stop'
    $architecture = Get-ZorveusArchitecture
    if ([string]::IsNullOrWhiteSpace($BinDir)) {
        if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
            throw 'Set LOCALAPPDATA or provide -BinDir.'
        }
        $BinDir = Join-Path $env:LOCALAPPDATA 'Zorveus\bin'
    }
    if (-not [IO.Path]::IsPathRooted($BinDir) -or
        ($BinDir -match '^[A-Za-z]:(?:$|[^\\/])') -or
        ([IO.Path]::DirectorySeparatorChar -eq '\' -and [IO.Path]::GetPathRoot($BinDir).Length -lt 3)) {
        throw '-BinDir must be an absolute path.'
    }
    $BinDir = [IO.Path]::GetFullPath($BinDir)

    $downloadDir = Join-Path ([IO.Path]::GetTempPath()) ('zorveus-install-' + [guid]::NewGuid())
    $stagedBinary = $null
    $previousTls = [Net.ServicePointManager]::SecurityProtocol
    try {
        [Net.ServicePointManager]::SecurityProtocol = $previousTls -bor [Net.SecurityProtocolType]::Tls12
        $null = New-Item -ItemType Directory -Path $downloadDir
        if ([string]::IsNullOrEmpty($Version)) {
            $versionFile = Join-Path $downloadDir 'latest'
            Get-ZorveusDownload -Uri 'https://zorveus.com/cli/latest' -Destination $versionFile
            $Version = [IO.File]::ReadAllText($versionFile).Trim()
        }
        if ($Version -cnotmatch '\Av[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?\z') {
            throw 'Expected a release version such as v1.2.3 or v1.2.3-rc.1.'
        }

        $artifact = "zorveus-windows-$architecture.exe"
        $releaseUrl = "https://zorveus.com/cli/$Version"
        $checksumsFile = Join-Path $downloadDir 'SHA256SUMS'
        $binaryFile = Join-Path $downloadDir $artifact
        Write-Host "Downloading Zorveus $Version for windows/$architecture..."
        Get-ZorveusDownload -Uri "$releaseUrl/SHA256SUMS" -Destination $checksumsFile
        Get-ZorveusDownload -Uri "$releaseUrl/$artifact" -Destination $binaryFile

        $checksumLines = @(Get-Content -LiteralPath $checksumsFile | Where-Object {
            ($_ -split '\s+')[1] -ceq $artifact
        })
        if ($checksumLines.Count -ne 1) {
            throw 'The release checksum is missing or duplicated.'
        }
        $expected = ($checksumLines[0] -split '\s+')[0]
        if ($expected -cnotmatch '\A[a-fA-F0-9]{64}\z') {
            throw 'The release checksum is invalid.'
        }
        $actual = (Get-FileHash -LiteralPath $binaryFile -Algorithm SHA256).Hash
        if ($actual -ine $expected) {
            throw 'Checksum verification failed. Your current installation was not changed.'
        }

        $null = New-Item -ItemType Directory -Force -Path $BinDir
        $destination = Join-Path $BinDir 'zorveus.exe'
        if (Test-Path -LiteralPath $destination) {
            $item = Get-Item -LiteralPath $destination -Force
            if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
                throw 'The installation destination must be a regular file.'
            }
        }
        # Stage on the same filesystem. A failed replacement preserves the old file.
        $stagedBinary = Join-Path $BinDir ('.zorveus-' + [guid]::NewGuid() + '.exe')
        Copy-Item -LiteralPath $binaryFile -Destination $stagedBinary
        if ([IO.File]::Exists($destination)) {
            [IO.File]::Replace($stagedBinary, $destination, [System.Management.Automation.Language.NullString]::Value)
        } else {
            [IO.File]::Move($stagedBinary, $destination)
        }
        $stagedBinary = $null
        Write-Host "Installed Zorveus $Version at $destination"
        try {
            Update-ZorveusPath -Directory $BinDir
        } catch {
            Write-Warning "Zorveus is installed, but PATH setup failed. Add $BinDir to your user Path in Windows Environment Variables."
        }
        Write-Host "Open a new terminal, then run zorveus --help. You can also run: & '$destination' --help"
    } finally {
        [Net.ServicePointManager]::SecurityProtocol = $previousTls
        if ($stagedBinary -and (Test-Path -LiteralPath $stagedBinary)) {
            Remove-Item -LiteralPath $stagedBinary -Force
        }
        if (Test-Path -LiteralPath $downloadDir) {
            Remove-Item -LiteralPath $downloadDir -Recurse -Force
        }
    }
}

& {
    $ErrorActionPreference = 'Stop'
    if ($Help) {
        Write-Host @'
Install Zorveus for Windows on x86-64 or ARM64.

Usage: .\install.ps1 [-Version v1.2.3] [-BinDir C:\absolute\path] [-Help]

By default, install the latest release into %LOCALAPPDATA%\Zorveus\bin.
No administrator access is required for the default directory.
The installer adds the directory to your user Path. Open a new terminal afterward.
'@
        return
    }
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw 'This installer supports Windows. Use install.sh on macOS or Linux.'
    }
    Install-Zorveus -Version $Version -BinDir $BinDir
}
