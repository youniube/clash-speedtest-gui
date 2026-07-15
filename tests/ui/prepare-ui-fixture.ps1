param(
    [string] $GuiPath
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
if ([string]::IsNullOrWhiteSpace($GuiPath)) {
    $GuiPath = Join-Path $root 'Clash-SpeedTest-GUI.exe'
}
$GuiPath = [IO.Path]::GetFullPath($GuiPath)
if (-not (Test-Path -LiteralPath $GuiPath)) {
    throw "UI fixture GUI was not found: $GuiPath"
}
$compiler = 'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if (-not (Test-Path -LiteralPath $compiler)) {
    $compiler = 'C:\Windows\Microsoft.NET\Framework\v4.0.30319\csc.exe'
}
if (-not (Test-Path -LiteralPath $compiler)) {
    throw 'The .NET Framework C# compiler was not found.'
}

$sandbox = Join-Path $env:TEMP (
    'ClashSpeedTestGUI-UiTest-' + [Guid]::NewGuid().ToString('N'))
$control = Join-Path $sandbox 'control'
$signals = Join-Path $sandbox 'signals'
$work = Join-Path $sandbox 'work'
$profile = Join-Path $sandbox 'profile'
$temporary = Join-Path $sandbox 'temp'
New-Item -ItemType Directory -Force -Path `
    $sandbox, $control, $signals, $work, $profile, $temporary | Out-Null

Copy-Item -LiteralPath $GuiPath `
    -Destination (Join-Path $sandbox 'Clash-SpeedTest-GUI.exe')

$fixture = Join-Path $sandbox 'speedtest-runner.exe'
& $compiler `
    /nologo `
    /target:exe `
    /platform:anycpu `
    /optimize+ `
    /codepage:65001 `
    /utf8output `
    /out:$fixture `
    /reference:System.dll `
    /reference:System.Core.dll `
    /reference:System.Web.Extensions.dll `
    (Join-Path $PSScriptRoot 'FixtureTool.cs')
if ($LASTEXITCODE -ne 0) {
    throw "Fixture build failed with exit code $LASTEXITCODE"
}
Copy-Item -LiteralPath $fixture `
    -Destination (Join-Path $sandbox 'subscription-parser.exe')

$launcher = Join-Path $sandbox 'UiFixtureLauncher.exe'
& $compiler `
    /nologo `
    /target:winexe `
    /platform:anycpu `
    /optimize+ `
    /out:$launcher `
    /reference:System.dll `
    (Join-Path $PSScriptRoot 'FixtureLauncher.cs')
if ($LASTEXITCODE -ne 0) {
    throw "Fixture launcher build failed with exit code $LASTEXITCODE"
}

$inputPath = Join-Path $work 'input.yaml'
[IO.File]::WriteAllText(
    $inputPath,
    "proxies:`n  - name: fixture-input`n    type: direct`n",
    [Text.UTF8Encoding]::new($false))
[IO.File]::WriteAllText(
    (Join-Path $control 'speed-mode.txt'),
    'gated-success',
    [Text.UTF8Encoding]::new($false))
[IO.File]::WriteAllText(
    (Join-Path $control 'region-mode.txt'),
    'all-success',
    [Text.UTF8Encoding]::new($false))

[PSCustomObject]@{
    Sandbox = $sandbox
    Launcher = $launcher
    Input = $inputPath
    Output = (Join-Path $work 'output.yaml')
}
