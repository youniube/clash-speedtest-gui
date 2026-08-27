param(
    [string] $GuiPath,
    [string] $ParserPath,
    [string] $RunnerPath
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$binaries = [ordered]@{
    'Clash-SpeedTest-GUI.exe' = $GuiPath
    'subscription-parser.exe' = $ParserPath
    'speedtest-runner.exe' = $RunnerPath
}
foreach ($name in @($binaries.Keys)) {
    if ([string]::IsNullOrWhiteSpace($binaries[$name])) {
        $binaries[$name] = Join-Path $root $name
    }
    $binaries[$name] = [IO.Path]::GetFullPath($binaries[$name])
    if (-not (Test-Path -LiteralPath $binaries[$name])) {
        throw "Production E2E binary was not found: $($binaries[$name])"
    }
}

$compiler = 'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if (-not (Test-Path -LiteralPath $compiler)) {
    $compiler = 'C:\Windows\Microsoft.NET\Framework\v4.0.30319\csc.exe'
}
if (-not (Test-Path -LiteralPath $compiler)) {
    throw 'The .NET Framework C# compiler was not found.'
}
$wpfRoot = Join-Path (Split-Path -Parent $compiler) 'WPF'

$sandbox = Join-Path $env:TEMP (
    'ClashSpeedTestGUI-ProductionE2E-' + [Guid]::NewGuid().ToString('N'))
$profile = Join-Path $sandbox 'profile'
$temporary = Join-Path $sandbox 'temp'
$work = Join-Path $sandbox 'work'
$artifactRoot = Join-Path $PSScriptRoot 'artifacts'
$driverOutput = @()
$sourceHashes = @{}

try {
    New-Item -ItemType Directory -Force -Path `
        $sandbox, $profile, $temporary, $work, $artifactRoot | Out-Null

    foreach ($name in $binaries.Keys) {
        $sourceHashes[$name] = (Get-FileHash -Algorithm SHA256 `
            -LiteralPath $binaries[$name]).Hash
        $destination = Join-Path $sandbox $name
        Copy-Item -LiteralPath $binaries[$name] -Destination $destination
        $copyHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $destination).Hash
        if ($copyHash -ne $sourceHashes[$name]) {
            throw "Production E2E copy hash mismatch: $name"
        }
    }

    $successInput = Join-Path $work 'provider-duplicates.yaml'
    $invalidInput = Join-Path $work 'mixed-invalid.txt'
    $regexInput = Join-Path $work 'provider-regexp-timeout.yaml'
    $successOutput = Join-Path $work 'success.yaml'
    $guardOutput = Join-Path $work 'guard.yaml'
    [IO.File]::WriteAllText($successInput, @'
proxy-providers:
  p:
    type: inline
    payload:
      - name: shared
        type: socks5
        server: 127.0.0.1
        port: __PORT1__
      - name: shared
        type: socks5
        server: 127.0.0.1
        port: __PORT2__
proxies: []
'@, [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText($invalidInput, @'
trojan://password@example.com:443?sni=example.com#valid
unsupported://partial-secret@example.org:443#invalid
'@, [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText($regexInput, @'
proxy-providers:
  p:
    type: inline
    filter: '(.+)*\?'
    payload:
      - name: Do you think you found the provider-timeout-node-secret problem string!
        type: socks5
        server: 127.0.0.1
        port: 9
proxies: []
'@, [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText(
        $guardOutput, "sentinel: keep`n", [Text.UTF8Encoding]::new($false))

    $settings = [ordered]@{
        ConfigSource = $successInput
        FilterRegex = ''
        MaxLatencyMs = 1000
        MinDownloadSpeed = 0
        OutputPath = $successOutput
        RenameNodes = $false
        GistEnabled = $false
        GistUsername = ''
        GistAddress = ''
        GistToken = ''
        AdvancedExpanded = $false
        SpeedMode = 'fast'
        BlockKeywords = ''
        ServerUrl = 'http://127.0.0.1:18080/probe'
        DownloadSizeMb = 1
        ProbeTimeoutSeconds = 0.5
        TimeoutSeconds = 1
        Concurrent = 2
        TransferConcurrent = 1
        MaxHTTPProbeFailure = 100
        UserAgent = ''
    }
    [IO.File]::WriteAllText(
        (Join-Path $profile 'settings.json'),
        ($settings | ConvertTo-Json -Compress),
        [Text.UTF8Encoding]::new($false))

    $launcher = Join-Path $sandbox 'ProductionE2eLauncher.exe'
    & $compiler `
        /nologo `
        /target:winexe `
        /platform:anycpu `
        /optimize+ `
        /out:$launcher `
        /reference:System.dll `
        (Join-Path $PSScriptRoot 'FixtureLauncher.cs')
    if ($LASTEXITCODE -ne 0) {
        throw "Production E2E launcher build failed with exit code $LASTEXITCODE"
    }

    $flaui = & (Join-Path $PSScriptRoot 'prepare-flaui.ps1')
    foreach ($assembly in @($flaui.Core, $flaui.UIA3, $flaui.Interop)) {
        Copy-Item -LiteralPath $assembly -Destination $sandbox
    }
    $driver = Join-Path $sandbox 'ProductionE2eUiDriver.exe'
    & $compiler `
        /nologo `
        /target:exe `
        /platform:x64 `
        /optimize+ `
        /codepage:65001 `
        /utf8output `
        /out:$driver `
        /reference:System.dll `
        /reference:System.Core.dll `
        /reference:System.Windows.Forms.dll `
        /reference:$(Join-Path $wpfRoot 'PresentationCore.dll') `
        /reference:$(Join-Path $wpfRoot 'PresentationFramework.dll') `
        /reference:$(Join-Path $wpfRoot 'WindowsBase.dll') `
        /reference:$($flaui.Core) `
        /reference:$($flaui.UIA3) `
        /reference:$($flaui.Interop) `
        (Join-Path $PSScriptRoot 'ProductionE2eUiDriver.cs')
    if ($LASTEXITCODE -ne 0) {
        throw "Production E2E driver build failed with exit code $LASTEXITCODE"
    }

    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $driverOutput = & $driver `
            --sandbox $sandbox `
            --launcher $launcher `
            --success-input $successInput `
            --invalid-input $invalidInput `
            --regex-input $regexInput `
            --success-output $successOutput `
            --guard-output $guardOutput 2>&1
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
    }

    $logPath = Join-Path $artifactRoot 'last-production-e2e.log'
    [IO.File]::WriteAllLines(
        $logPath, [string[]] $driverOutput, [Text.UTF8Encoding]::new($false))
    if ($exitCode -ne 0) {
        if (Test-Path -LiteralPath $successOutput) {
            Copy-Item -LiteralPath $successOutput -Destination `
                (Join-Path $artifactRoot 'last-production-e2e-output.yaml') -Force
        }
        throw "Production E2E driver failed with exit code $exitCode. See $logPath"
    }

    $taskRoot = Join-Path $temporary 'ClashSpeedTestGUI'
    if ((Test-Path -LiteralPath $taskRoot) `
        -and @(Get-ChildItem -LiteralPath $taskRoot -Force).Count -ne 0) {
        throw "Production E2E left task temporary entries: $taskRoot"
    }
    if (@(Get-ChildItem -LiteralPath $work -Filter '*.cstgui-*.tmp.yaml' -Force).Count -ne 0) {
        throw 'Production E2E left a core output temporary file.'
    }
    foreach ($process in Get-CimInstance Win32_Process) {
        if ($process.ExecutablePath -and $process.ExecutablePath.StartsWith(
            $sandbox, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Production E2E left a sandbox process: $($process.Name) ($($process.ProcessId))"
        }
    }
    foreach ($name in $binaries.Keys) {
        $after = (Get-FileHash -Algorithm SHA256 -LiteralPath $binaries[$name]).Hash
        if ($after -ne $sourceHashes[$name]) {
            throw "Production binary changed during E2E: $name"
        }
    }

    Remove-Item -LiteralPath `
        (Join-Path $artifactRoot 'last-production-e2e-output.yaml') `
        -Force -ErrorAction SilentlyContinue
    $driverOutput | Write-Output
    foreach ($name in $binaries.Keys) {
        Write-Output "$name SHA256 $($sourceHashes[$name])"
    }
    Write-Output 'Production three-process E2E test passed.'
}
finally {
    if (Test-Path -LiteralPath $sandbox) {
        $fullSandbox = [IO.Path]::GetFullPath($sandbox)
        $expectedPrefix = Join-Path $env:TEMP 'ClashSpeedTestGUI-ProductionE2E-'
        if (-not $fullSandbox.StartsWith(
            $expectedPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Unexpected production E2E sandbox path: $fullSandbox"
        }
        foreach ($process in Get-CimInstance Win32_Process) {
            if ($process.ExecutablePath -and $process.ExecutablePath.StartsWith(
                $fullSandbox, [StringComparison]::OrdinalIgnoreCase)) {
                Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
            }
        }
        Remove-Item -LiteralPath $fullSandbox -Recurse -Force
    }
}
