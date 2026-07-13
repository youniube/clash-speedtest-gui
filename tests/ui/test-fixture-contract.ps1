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

    $speedLines = [IO.File]::ReadAllLines($speedLog, [Text.Encoding]::UTF8)
    if ($speedLines.Count -lt 1 -or $speedLines[0] -ne "@protocol`t5") {
        throw 'Fixture speed transcript did not use protocol v5.'
    }
    $actualHeaderBase64 = if ($speedLines.Count -ge 2) {
        [Convert]::ToBase64String(
            [Text.Encoding]::UTF8.GetBytes([string] $speedLines[1]))
    } else { '' }
    $expectedDownloadHeaderBase64 =
        '5bqP5Y+3CeiKgueCueWQjeensAnnsbvlnosJSFRUUCDlu7bov58J5oqW5YqoCUhUVFAg5o6i5rWL5aSx6LSl546HCeS4i+i9vemAn+W6pg=='
    if ($actualHeaderBase64 -ne $expectedDownloadHeaderBase64) {
        throw 'Fixture download transcript did not use the exact v5 header.'
    }
    $speedResults = @($speedLines | Where-Object {
        $_.StartsWith("@resultjson`t", [StringComparison]::Ordinal)
    })
    if ($speedResults.Count -ne 3) {
        throw "Fixture speed transcript returned $($speedResults.Count) results instead of 3."
    }
    $decodedResults = @($speedResults | ForEach-Object {
        $encoded = $_.Substring($_.IndexOf("`t") + 1)
        $encoded = $encoded.PadRight(
            $encoded.Length + ((4 - ($encoded.Length % 4)) % 4), '=')
        $json = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($encoded))
        $json | ConvertFrom-Json
    })
    $expectedMetricKeys = @(
        'download_bytes_per_second', 'download_complete', 'download_tested',
        'http_probe_failure_percent', 'jitter_nanoseconds', 'latency_nanoseconds'
    )
    foreach ($result in $decodedResults) {
        $actualMetricKeys = @($result.metrics.PSObject.Properties.Name | Sort-Object)
        if ((Compare-Object $expectedMetricKeys $actualMetricKeys).Count -ne 0) {
            throw 'Fixture v5 metrics do not contain the exact expected key set.'
        }
    }
    $successful = @($decodedResults | Where-Object { $_.usable })
    $failed = @($decodedResults | Where-Object { -not $_.usable })
    if ($successful.Count -ne 2 -or -not $successful[0].metrics.download_tested `
        -or -not $successful[0].metrics.download_complete) {
        throw 'Fixture download-mode tested/complete semantics are incorrect.'
    }
    if ($failed.Count -ne 1 -or $failed[0].metrics.download_tested `
        -or $failed[0].metrics.download_complete) {
        throw 'Fixture failed-node tested/complete semantics are incorrect.'
    }
    $progressLines = @($speedLines | Where-Object {
        $_.StartsWith("@progressjson`t", [StringComparison]::Ordinal)
    })
    $progress = @($progressLines | ForEach-Object {
        $encoded = $_.Substring($_.IndexOf("`t") + 1)
        $encoded = $encoded.PadRight(
            $encoded.Length + ((4 - ($encoded.Length % 4)) % 4), '=')
        ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($encoded))) |
            ConvertFrom-Json
    })
    if (@($progress | Where-Object { $_.stage -eq 'probe_completed' }).Count -ne 3 `
        -or @($progress | Where-Object { $_.stage -eq 'download_started' }).Count -ne 2) {
        throw 'Fixture v5 progress counts are incorrect.'
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
