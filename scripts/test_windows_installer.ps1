#Requires -Version 5.1
# Run without Pester or network access. All install paths and downloads are fixtures.
$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot 'install.ps1'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($installer, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw ($parseErrors | Out-String) }

# Load the real functions without running the installer entry point.
foreach ($statement in $ast.EndBlock.Statements) {
    if ($statement -is [Management.Automation.Language.FunctionDefinitionAst]) {
        . ([scriptblock]::Create($statement.Extent.Text))
    }
}
$realDownloader = ${function:Get-ZorveusDownload}

function Assert-Equal {
    param($Actual, $Expected)
    if ($Actual -cne $Expected) { throw "Expected '$Expected', got '$Actual'." }
}

function Assert-Fails {
    param([scriptblock]$Action, [string]$Message)
    try { & $Action } catch {
        if ($_.ToString() -notlike "*$Message*") { throw }
        return
    }
    throw "Expected failure containing '$Message'."
}

# The only environment-dependent side effect is replaced, leaving the real PATH alone.
function Update-ZorveusPath {
    param([string]$Directory)
    if ($script:FailPath) { throw 'Simulated PATH write failure' }
    $script:PathDirectory = $Directory
}

function Get-ZorveusDownload {
    param([uri]$Uri, [string]$Destination)
    if ($Uri.Scheme -cne 'https' -or $Uri.Host -cne 'zorveus.com') {
        throw "Unexpected URL: $Uri"
    }
    $script:Requests += $Uri.AbsolutePath
    if ($Uri.AbsolutePath -eq '/cli/latest') {
        [IO.File]::WriteAllText($Destination, $script:Latest)
        return
    }
    $name = $Uri.Segments[-1]
    if ($name -eq $script:FailDownload) { throw 'Simulated download failure' }
    if ($name -eq 'SHA256SUMS') {
        [IO.File]::WriteAllText($Destination, $script:Checksums)
        return
    }
    if ($name -notin @('zorveus-windows-amd64.exe', 'zorveus-windows-arm64.exe')) {
        throw "Unexpected artifact: $name"
    }
    [IO.File]::WriteAllText($Destination, $script:Payload)
}

$originalPath = $env:Path
$originalArchitecture = $env:PROCESSOR_ARCHITECTURE
$originalNativeArchitecture = $env:PROCESSOR_ARCHITEW6432
$originalLocalAppData = $env:LOCALAPPDATA
$originalTemp = $env:TEMP
$originalTmp = $env:TMP
$originalTmpDir = $env:TMPDIR
$originalTls = [Net.ServicePointManager]::SecurityProtocol
$root = Join-Path ([IO.Path]::GetTempPath()) ('zorveus-installer-tests-' + [guid]::NewGuid())
$null = New-Item -ItemType Directory -Path $root
$downloads = Join-Path $root 'downloads'
$null = New-Item -ItemType Directory -Path $downloads
$env:TEMP = $downloads
$env:TMP = $downloads
$env:TMPDIR = $downloads
$env:LOCALAPPDATA = Join-Path $root 'local app data'
$env:PROCESSOR_ARCHITECTURE = 'AMD64'
$env:PROCESSOR_ARCHITEW6432 = ''
$script:Payload = 'fixture binary that must never be executed'
$payloadFile = Join-Path $root 'payload'
[IO.File]::WriteAllText($payloadFile, $script:Payload)
$checksum = (Get-FileHash -LiteralPath $payloadFile -Algorithm SHA256).Hash.ToLowerInvariant()
$validChecksums = "$checksum  zorveus-windows-amd64.exe`n$checksum  zorveus-windows-arm64.exe`n"
$script:Checksums = $validChecksums
$script:Latest = "v1.2.3`r`n"
$script:FailDownload = ''
$script:FailPath = $false
$script:Requests = @()
$script:PathDirectory = ''

try {
    # Exercise the real HttpClient download loop with an in-memory HTTP handler.
    Add-Type -AssemblyName System.Net.Http
    $references = @('System.Net.Http')
    if ($PSVersionTable.PSVersion.Major -ge 6) {
        $references += @('System.Collections', 'System.Net.Primitives', 'System.Runtime')
    }
    Add-Type -ReferencedAssemblies $references -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
public class ZorveusFixtureHandler : HttpClientHandler {
    public static string Mode = "success";
    public static int RedirectStatus = 307;
    public static List<string> Requests = new List<string>();
    protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token) {
        if (AllowAutoRedirect) throw new Exception("Automatic redirects must be disabled");
        Requests.Add(request.RequestUri.AbsoluteUri);
        var response = new HttpResponseMessage(HttpStatusCode.OK);
        if (Mode == "error") response.StatusCode = HttpStatusCode.NotFound;
        else if (request.RequestUri.AbsolutePath == "/start" || Mode == "loop") {
            response.StatusCode = (HttpStatusCode)RedirectStatus;
            if (Mode != "missing") {
                string location = "/payload";
                if (Mode == "downgrade") location = "http://cdn.example.test/payload";
                if (Mode == "cdn") location = "https://cdn.example.test/payload";
                response.Headers.Location = new Uri(location, UriKind.RelativeOrAbsolute);
            }
        }
        response.Content = new StringContent("download fixture");
        return Task.FromResult(response);
    }
}
'@
    & {
        function New-Object {
            param([string]$TypeName, [object[]]$ArgumentList)
            if ($TypeName -eq 'System.Net.Http.HttpClientHandler') {
                return Microsoft.PowerShell.Utility\New-Object -TypeName ZorveusFixtureHandler
            }
            return Microsoft.PowerShell.Utility\New-Object -TypeName $TypeName -ArgumentList $ArgumentList
        }
        $downloadFile = Join-Path $root 'downloaded'
        foreach ($mode in @('success', 'cdn')) {
            foreach ($status in @(301, 302, 303, 307, 308)) {
                [ZorveusFixtureHandler]::Mode = $mode
                [ZorveusFixtureHandler]::RedirectStatus = $status
                [ZorveusFixtureHandler]::Requests.Clear()
                & $realDownloader -Uri 'https://download.example.test/start' -Destination $downloadFile
                Assert-Equal ([IO.File]::ReadAllText($downloadFile)) 'download fixture'
                Assert-Equal ([ZorveusFixtureHandler]::Requests.Count) 2
                Remove-Item -LiteralPath $downloadFile
            }
        }
        foreach ($mode in @('downgrade', 'missing', 'loop', 'error')) {
            [ZorveusFixtureHandler]::Mode = $mode
            Assert-Fails { & $realDownloader -Uri 'https://download.example.test/start' -Destination $downloadFile } ''
            Assert-Equal (Test-Path -LiteralPath $downloadFile) $false
        }
        [ZorveusFixtureHandler]::Requests.Clear()
        Assert-Fails { & $realDownloader -Uri 'http://download.example.test/start' -Destination $downloadFile } 'HTTPS'
        Assert-Equal ([ZorveusFixtureHandler]::Requests.Count) 0
    }

    Assert-Equal (Get-ZorveusArchitecture) 'amd64'
    $env:PROCESSOR_ARCHITECTURE = 'ARM64'
    Assert-Equal (Get-ZorveusArchitecture) 'arm64'
    $env:PROCESSOR_ARCHITECTURE = 'x86'
    $env:PROCESSOR_ARCHITEW6432 = 'ARM64'
    Assert-Equal (Get-ZorveusArchitecture) 'arm64'
    $env:PROCESSOR_ARCHITEW6432 = 'AMD64'
    Assert-Equal (Get-ZorveusArchitecture) 'amd64'
    $env:PROCESSOR_ARCHITEW6432 = ''
    Assert-Fails { Install-Zorveus } 'Supported processors'
    Assert-Equal $script:Requests.Count 0
    $env:PROCESSOR_ARCHITECTURE = 'AMD64'

    Assert-Equal (Add-ZorveusPathEntry '' 'C:\Zorveus\bin') 'C:\Zorveus\bin'
    Assert-Equal (Add-ZorveusPathEntry 'C:\Tools' 'C:\Zorveus\bin') 'C:\Zorveus\bin;C:\Tools'
    Assert-Equal (Add-ZorveusPathEntry 'C:\ZORVEUS\bin\;C:\Tools' 'C:\Zorveus\bin') 'C:\ZORVEUS\bin\;C:\Tools'
    Assert-Equal (Add-ZorveusPathEntry '"%LOCALAPPDATA%\Zorveus\bin";C:\Tools' "$env:LOCALAPPDATA\Zorveus\bin") '"%LOCALAPPDATA%\Zorveus\bin";C:\Tools'
    Assert-Equal (Add-ZorveusPathEntry 'C:\Zorveus\binary' 'C:\Zorveus\bin') 'C:\Zorveus\bin;C:\Zorveus\binary'

    Install-Zorveus
    $defaultDir = Join-Path $env:LOCALAPPDATA 'Zorveus\bin'
    Assert-Equal ([IO.File]::ReadAllText((Join-Path $defaultDir 'zorveus.exe'))) $script:Payload
    Assert-Equal $script:PathDirectory $defaultDir
    Assert-Equal ($script:Requests -join ',') '/cli/latest,/cli/v1.2.3/SHA256SUMS,/cli/v1.2.3/zorveus-windows-amd64.exe'

    $installDir = Join-Path $root 'custom tools'
    $installed = Join-Path $installDir 'zorveus.exe'
    $env:PROCESSOR_ARCHITECTURE = 'ARM64'
    $script:Requests = @()
    Install-Zorveus -Version 'v2.0.0-rc.1' -BinDir $installDir
    Assert-Equal ($script:Requests -join ',') '/cli/v2.0.0-rc.1/SHA256SUMS,/cli/v2.0.0-rc.1/zorveus-windows-arm64.exe'
    Assert-Equal ([IO.File]::ReadAllText($installed)) $script:Payload
    [IO.File]::WriteAllText($installed, 'previous executable')

    foreach ($invalid in @('latest', '../v1.2.3', 'v1.2.3/other', 'v1.2.3?token=x', "v1.2.3`n", 'V1.2.3')) {
        $script:Requests = @()
        Assert-Fails { Install-Zorveus -Version $invalid -BinDir $installDir } 'Expected a release version'
        Assert-Equal $script:Requests.Count 0
    }
    $script:Latest = '<html>not a release</html>'
    Assert-Fails { Install-Zorveus -BinDir $installDir } 'Expected a release version'
    $script:Latest = 'v1.2.3'
    Assert-Fails { Install-Zorveus -BinDir 'relative\directory' } 'absolute path'
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        foreach ($relative in @('C:', 'C:tools', '\tools', '/tools')) {
            Assert-Fails { Install-Zorveus -BinDir $relative } 'absolute path'
        }
    }

    foreach ($badChecksums in @('', "$checksum  other.exe", "$validChecksums$validChecksums", "invalid  zorveus-windows-arm64.exe", ((('0' * 64) -join '') + '  zorveus-windows-arm64.exe'))) {
        $script:Checksums = $badChecksums
        Assert-Fails { Install-Zorveus -Version 'v1.2.3' -BinDir $installDir } 'checksum'
        Assert-Equal ([IO.File]::ReadAllText($installed)) 'previous executable'
    }
    $script:Checksums = $validChecksums
    foreach ($name in @('SHA256SUMS', 'zorveus-windows-arm64.exe')) {
        $script:FailDownload = $name
        Assert-Fails { Install-Zorveus -Version 'v1.2.3' -BinDir $installDir } 'download failure'
        Assert-Equal ([IO.File]::ReadAllText($installed)) 'previous executable'
    }
    $script:FailDownload = ''

    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        # Simulate an executable locked by another process. Replacement must fail safely.
        $lockedFile = [IO.File]::Open($installed, 'Open', 'Read', 'None')
        try {
            Assert-Fails { Install-Zorveus -Version 'v1.2.3' -BinDir $installDir } ''
        } finally { $lockedFile.Dispose() }
        Assert-Equal ([IO.File]::ReadAllText($installed)) 'previous executable'
    }

    Install-Zorveus -Version 'v1.2.3' -BinDir $installDir
    Assert-Equal ([IO.File]::ReadAllText($installed)) $script:Payload
    $script:FailPath = $true
    $pathWarnings = @()
    Install-Zorveus -Version 'v1.2.3' -BinDir $installDir -WarningVariable pathWarnings
    Assert-Equal $pathWarnings.Count 1
    Assert-Equal ([IO.File]::ReadAllText($installed)) $script:Payload

    $blockedDir = Join-Path $root 'blocked'
    $null = New-Item -ItemType Directory -Path (Join-Path $blockedDir 'zorveus.exe') -Force
    Assert-Fails { Install-Zorveus -Version 'v1.2.3' -BinDir $blockedDir } 'regular file'
    Assert-Equal @(Get-ChildItem -LiteralPath $downloads -Force).Count 0
    Assert-Equal @(Get-ChildItem -LiteralPath $root -Recurse -Force -Filter '.zorveus-*').Count 0
    Assert-Equal $env:Path $originalPath
    Assert-Equal ([Net.ServicePointManager]::SecurityProtocol) $originalTls
    & $installer -Help
    Write-Host 'Windows installer tests passed.'
} finally {
    $env:Path = $originalPath
    $env:PROCESSOR_ARCHITECTURE = $originalArchitecture
    $env:PROCESSOR_ARCHITEW6432 = $originalNativeArchitecture
    $env:LOCALAPPDATA = $originalLocalAppData
    $env:TEMP = $originalTemp
    $env:TMP = $originalTmp
    $env:TMPDIR = $originalTmpDir
    Remove-Item -LiteralPath $root -Recurse -Force
}
