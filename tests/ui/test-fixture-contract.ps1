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
    if ($speedLines.Count -lt 1 -or $speedLines[0] -ne "@protocol`t4") {
        throw 'Fixture speed transcript did not use protocol v4.'
    }
    $actualHeaderBase64 = if ($speedLines.Count -ge 2) {
        [Convert]::ToBase64String(
            [Text.Encoding]::UTF8.GetBytes([string] $speedLines[1]))
    } else { '' }
    $expectedDownloadHeaderBase64 =
        '5bqP5Y+3CeiKgueCueWQjeensAnnsbvlnosJ5bu26L+fCeaKluWKqAlIVFRQIOaOoua1i+Wksei0peeOhwnkuIvovb3pgJ/luqY='
    if ($actualHeaderBase64 -ne $expectedDownloadHeaderBase64) {
        throw 'Fixture download transcript did not use the exact v4 header.'
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
        'jitter_nanoseconds', 'latency_nanoseconds', 'packet_loss_percent',
        'upload_bytes_per_second', 'upload_complete', 'upload_tested'
    )
    foreach ($result in $decodedResults) {
        $actualMetricKeys = @($result.metrics.PSObject.Properties.Name | Sort-Object)
        if ((Compare-Object $expectedMetricKeys $actualMetricKeys).Count -ne 0) {
            throw 'Fixture v4 metrics do not contain the exact expected key set.'
        }
    }
    if (-not $decodedResults[0].metrics.download_tested `
        -or -not $decodedResults[0].metrics.download_complete `
        -or $decodedResults[0].metrics.upload_tested `
        -or $decodedResults[0].metrics.upload_complete) {
        throw 'Fixture download-mode tested/complete semantics are incorrect.'
    }
    if ($decodedResults[2].metrics.download_tested `
        -or $decodedResults[2].metrics.download_complete `
        -or $decodedResults[2].metrics.upload_tested `
        -or $decodedResults[2].metrics.upload_complete) {
        throw 'Fixture failed-node tested/complete semantics are incorrect.'
    }

    $fullOutput = Join-Path $fixture.Sandbox 'work\full-output.yaml'
    $fullLog = Join-Path $fixture.Sandbox 'work\full.log'
    & $runner `
        -c $fixture.Input `
        -speed-mode full `
        -output $fullOutput *> $fullLog
    if ($LASTEXITCODE -ne 0) {
        throw "Fixture full speed test failed with exit code $LASTEXITCODE"
    }
    $fullLines = [IO.File]::ReadAllLines($fullLog, [Text.Encoding]::UTF8)
    $actualFullHeaderBase64 = if ($fullLines.Count -ge 2) {
        [Convert]::ToBase64String(
            [Text.Encoding]::UTF8.GetBytes([string] $fullLines[1]))
    } else { '' }
    $expectedFullHeaderBase64 =
        '5bqP5Y+3CeiKgueCueWQjeensAnnsbvlnosJ5bu26L+fCeaKluWKqAlIVFRQIOaOoua1i+Wksei0peeOhwnkuIvovb3pgJ/luqYJ5LiK5Lyg6YCf5bqm'
    if ($fullLines.Count -lt 2 -or $fullLines[0] -ne "@protocol`t4" `
        -or $actualFullHeaderBase64 -ne $expectedFullHeaderBase64) {
        throw 'Fixture full transcript did not use the exact v4 protocol and header.'
    }
    $firstFullLine = @($fullLines | Where-Object {
        $_.StartsWith("@resultjson`t", [StringComparison]::Ordinal)
    })[0]
    $fullEncoded = $firstFullLine.Substring($firstFullLine.IndexOf("`t") + 1)
    $fullEncoded = $fullEncoded.PadRight(
        $fullEncoded.Length + ((4 - ($fullEncoded.Length % 4)) % 4), '=')
    $fullResult = ([Text.Encoding]::UTF8.GetString(
        [Convert]::FromBase64String($fullEncoded))) | ConvertFrom-Json
    if (-not $fullResult.metrics.download_tested `
        -or -not $fullResult.metrics.download_complete `
        -or -not $fullResult.metrics.upload_tested `
        -or -not $fullResult.metrics.upload_complete) {
        throw 'Fixture full-mode tested/complete semantics are incorrect.'
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
