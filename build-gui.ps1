$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$source = Join-Path $root 'src\ClashSpeedTestGUI.cs'
$output = Join-Path $root 'Clash-SpeedTest-GUI.exe'
$icon = Join-Path $root 'assets\app-icon.ico'
$manifest = Join-Path $root 'src\ClashSpeedTestGUI.manifest'
$parserSource = Join-Path $root 'tools\subscription-parser'
$parserOutput = Join-Path $root 'subscription-parser.exe'
$runnerSource = Join-Path $root 'tools\speedtest-runner'
$runnerOutput = Join-Path $root 'speedtest-runner.exe'
$compiler = 'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe'

if (-not (Test-Path -LiteralPath $compiler)) {
    $compiler = 'C:\Windows\Microsoft.NET\Framework\v4.0.30319\csc.exe'
}

if (-not (Test-Path -LiteralPath $compiler)) {
    throw 'The .NET Framework C# compiler was not found.'
}

& $compiler `
    /nologo `
    /target:winexe `
    /platform:x64 `
    /optimize+ `
    /win32icon:$icon `
    /win32manifest:$manifest `
    /codepage:65001 `
    /utf8output `
    /out:$output `
    /reference:System.dll `
    /reference:System.Core.dll `
    /reference:System.Drawing.dll `
    /reference:System.Net.Http.dll `
    /reference:System.Web.Extensions.dll `
    /reference:System.Windows.Forms.dll `
    $source

if ($LASTEXITCODE -ne 0) {
    throw "GUI build failed with exit code $LASTEXITCODE"
}

Push-Location $parserSource
try {
    & go build -buildvcs=false -trimpath -ldflags '-s -w' -o $parserOutput .
    if ($LASTEXITCODE -ne 0) {
        throw "Subscription parser build failed with exit code $LASTEXITCODE"
    }
}
finally {
    Pop-Location
}

Push-Location $runnerSource
try {
    & go build -buildvcs=false -trimpath -ldflags '-s -w' -o $runnerOutput .
    if ($LASTEXITCODE -ne 0) {
        throw "Speed test runner build failed with exit code $LASTEXITCODE"
    }
}
finally {
    Pop-Location
}

Write-Output "Built: $output"
Write-Output "Built: $parserOutput"
Write-Output "Built: $runnerOutput"
