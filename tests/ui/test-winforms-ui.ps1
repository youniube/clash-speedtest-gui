param(
    [string] $GuiPath
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$compiler = 'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if (-not (Test-Path -LiteralPath $compiler)) {
    $compiler = 'C:\Windows\Microsoft.NET\Framework\v4.0.30319\csc.exe'
}
if (-not (Test-Path -LiteralPath $compiler)) {
    throw 'The .NET Framework C# compiler was not found.'
}
$wpfRoot = Join-Path (Split-Path -Parent $compiler) 'WPF'
foreach ($assembly in @('PresentationCore.dll', 'PresentationFramework.dll', 'WindowsBase.dll')) {
    if (-not (Test-Path -LiteralPath (Join-Path $wpfRoot $assembly))) {
        throw "Required .NET Framework assembly was not found: $assembly"
    }
}

$fixture = $null
$artifactRoot = Join-Path $PSScriptRoot 'artifacts'
New-Item -ItemType Directory -Force -Path $artifactRoot | Out-Null

try {
    $flaui = & (Join-Path $PSScriptRoot 'prepare-flaui.ps1')
    $fixture = & (Join-Path $PSScriptRoot 'prepare-ui-fixture.ps1') -GuiPath $GuiPath
    [IO.File]::WriteAllText(
        (Join-Path $fixture.Sandbox 'control\speed-mode.txt'),
        'success',
        [Text.UTF8Encoding]::new($false))

    $driver = Join-Path $fixture.Sandbox 'WinFormsUiDriver.exe'
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
        /reference:System.Drawing.dll `
        /reference:System.Management.dll `
        /reference:System.Net.Http.dll `
        /reference:System.Windows.Forms.dll `
        /reference:Accessibility.dll `
        /reference:$(Join-Path $wpfRoot 'PresentationCore.dll') `
        /reference:$(Join-Path $wpfRoot 'PresentationFramework.dll') `
        /reference:$(Join-Path $wpfRoot 'WindowsBase.dll') `
        /reference:$($flaui.Core) `
        /reference:$($flaui.UIA3) `
        /reference:$($flaui.Interop) `
        (Join-Path $PSScriptRoot 'WinFormsUiDriver.cs')
    if ($LASTEXITCODE -ne 0) {
        throw "FlaUI driver build failed with exit code $LASTEXITCODE"
    }

    Copy-Item -LiteralPath $flaui.Core -Destination $fixture.Sandbox
    Copy-Item -LiteralPath $flaui.UIA3 -Destination $fixture.Sandbox
    Copy-Item -LiteralPath $flaui.Interop -Destination $fixture.Sandbox

    $screenshot = Join-Path $fixture.Sandbox 'work\winforms-ui.png'
    $log = Join-Path $fixture.Sandbox 'work\winforms-ui.log'
    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $driverOutput = & $driver `
            --sandbox $fixture.Sandbox `
            --launcher $fixture.Launcher `
            --input $fixture.Input `
            --output $fixture.Output `
            --screenshot $screenshot 2>&1
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorAction
    }
    [IO.File]::WriteAllLines(
        $log,
        [string[]] $driverOutput,
        [Text.UTF8Encoding]::new($false))
    if ($exitCode -ne 0) {
        Copy-Item -LiteralPath $log -Destination `
            (Join-Path $artifactRoot 'last-failure.log') -Force
        if (Test-Path -LiteralPath $screenshot) {
            Copy-Item -LiteralPath $screenshot -Destination `
                (Join-Path $artifactRoot 'last-failure.png') -Force
        }
        $trace = Join-Path $fixture.Sandbox 'work\driver-trace.log'
        if (Test-Path -LiteralPath $trace) {
            Copy-Item -LiteralPath $trace -Destination `
                (Join-Path $artifactRoot 'last-failure-trace.log') -Force
        }
        $failureWindow = Join-Path $fixture.Sandbox 'work\failure-window.png'
        if (Test-Path -LiteralPath $failureWindow) {
            Copy-Item -LiteralPath $failureWindow -Destination `
                (Join-Path $artifactRoot 'last-failure-window.png') -Force
        }
        if (Test-Path -LiteralPath $fixture.Output) {
            Copy-Item -LiteralPath $fixture.Output -Destination `
                (Join-Path $artifactRoot 'last-failure-output.yaml') -Force
        }
        throw "FlaUI WinForms test failed with exit code $exitCode. See $log"
    }

    Copy-Item -LiteralPath $screenshot -Destination `
        (Join-Path $artifactRoot 'last-success.png') -Force
    Copy-Item -LiteralPath $log -Destination `
        (Join-Path $artifactRoot 'last-success.log') -Force
    Remove-Item -LiteralPath (Join-Path $artifactRoot 'last-failure.log') `
        -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $artifactRoot 'last-failure.png') `
        -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $artifactRoot 'last-failure-trace.log') `
        -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $artifactRoot 'last-failure-window.png') `
        -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $artifactRoot 'last-failure-output.yaml') `
        -Force -ErrorAction SilentlyContinue

    $driverOutput | Write-Output
    Write-Output 'FlaUI WinForms operation test passed.'
}
finally {
    if ($fixture) {
        $sandbox = [IO.Path]::GetFullPath($fixture.Sandbox)
        $expectedPrefix = Join-Path $env:TEMP 'ClashSpeedTestGUI-UiTest-'
        if (-not $sandbox.StartsWith(
            $expectedPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Unexpected UI fixture sandbox path: $sandbox"
        }
        foreach ($process in Get-CimInstance Win32_Process) {
            if ($process.ExecutablePath -and $process.ExecutablePath.StartsWith(
                $sandbox, [StringComparison]::OrdinalIgnoreCase)) {
                Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
            }
        }
        if (Test-Path -LiteralPath $sandbox) {
            Remove-Item -LiteralPath $sandbox -Recurse -Force
        }
    }
}
