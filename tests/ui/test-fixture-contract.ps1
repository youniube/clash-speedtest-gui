$ErrorActionPreference = 'Stop'

$fixture = & (Join-Path $PSScriptRoot 'prepare-ui-fixture.ps1')
$previousRoot = $env:CLASH_SPEEDTEST_GUI_UI_FIXTURE_ROOT

try {
    $env:CLASH_SPEEDTEST_GUI_UI_FIXTURE_ROOT = $fixture.Sandbox
    [IO.File]::WriteAllText(
        (Join-Path $fixture.Sandbox 'control\speed-mode.txt'),
        'success',
        [Text.UTF8Encoding]::new($false))

    $runner = Join-Path $fixture.Sandbox 'speedtest-runner.exe'
    $speedLog = Join-Path $fixture.Sandbox 'work\speed.log'
    & $runner `
        -c $fixture.Input `
        -speed-mode download `
        -output $fixture.Output *> $speedLog
    if ($LASTEXITCODE -ne 0) {
        throw "Fixture speed test failed with exit code $LASTEXITCODE"
    }

    $nodeA = [string]::new('a', 64)
    $nodeB = [string]::new('b', 64)
    $requestPath = Join-Path $fixture.Sandbox 'temp\manage.json'
    $managedPath = Join-Path $fixture.Sandbox 'work\managed.yaml'
    $request = @{
        renames = @{ $nodeA = 'renamed-fixture-a' }
        deletes = @($nodeB)
    } | ConvertTo-Json -Compress
    [IO.File]::WriteAllText(
        $requestPath,
        $request,
        [Text.UTF8Encoding]::new($false))

    $managedJson = & $runner `
        -manage-config $requestPath `
        -c $fixture.Output `
        -output $managedPath
    if ($LASTEXITCODE -ne 0) {
        throw "Fixture node management failed with exit code $LASTEXITCODE"
    }
    $managed = $managedJson | ConvertFrom-Json
    if ($managed.renamed -ne 1 -or $managed.deleted -ne 1 `
        -or $managed.nodes.Count -ne 1 `
        -or $managed.nodes[0].name -ne 'renamed-fixture-a') {
        throw 'Fixture node management returned an unexpected result.'
    }

    $listed = (& $runner -list-config $managedPath) | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0 -or $listed.nodes.Count -ne 1 `
        -or $listed.nodes[0].name -ne 'renamed-fixture-a') {
        throw 'Fixture managed configuration could not be read back.'
    }

    $yaml = [IO.File]::ReadAllText($managedPath, [Text.Encoding]::UTF8)
    if (-not $yaml.Contains('renamed-fixture-a') -or $yaml.Contains('port: 10002')) {
        throw 'Fixture managed YAML content is incorrect.'
    }

    Write-Output 'UI fixture node-management contract passed.'
}
finally {
    $env:CLASH_SPEEDTEST_GUI_UI_FIXTURE_ROOT = $previousRoot
    if ($fixture -and (Test-Path -LiteralPath $fixture.Sandbox)) {
        Remove-Item -LiteralPath $fixture.Sandbox -Recurse -Force
    }
}
