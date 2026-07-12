$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path

function Invoke-Step {
    param(
        [Parameter(Mandatory = $true)]
        [string] $Name,
        [Parameter(Mandatory = $true)]
        [scriptblock] $Action
    )

    Write-Output "==> $Name"
    & $Action
    Write-Output "OK: $Name"
}

Invoke-Step "GUI self-test" {
    $process = Start-Process `
        -FilePath (Join-Path $root 'Clash-SpeedTest-GUI.exe') `
        -ArgumentList '--self-test' `
        -Wait `
        -PassThru `
        -WindowStyle Hidden
    if ($process.ExitCode -ne 0) {
        throw "GUI self-test failed with exit code $($process.ExitCode)"
    }
}

Invoke-Step "UI fixture node-management contract" {
    & (Join-Path $root 'tests\ui\test-fixture-contract.ps1')
}

Invoke-Step "subscription-parser go test" {
    Push-Location (Join-Path $root 'tools\subscription-parser')
    try {
        & go test -count=1 ./...
        if ($LASTEXITCODE -ne 0) {
            throw "subscription-parser tests failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}

Invoke-Step "subscription-parser go vet" {
    Push-Location (Join-Path $root 'tools\subscription-parser')
    try {
        & go vet ./...
        if ($LASTEXITCODE -ne 0) {
            throw "subscription-parser vet failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}

Invoke-Step "speedtest-runner go test" {
    Push-Location (Join-Path $root 'tools\speedtest-runner')
    try {
        & go test -count=1 ./...
        if ($LASTEXITCODE -ne 0) {
            throw "speedtest-runner tests failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}

Invoke-Step "speedtest-runner go vet" {
    Push-Location (Join-Path $root 'tools\speedtest-runner')
    try {
        & go vet ./...
        if ($LASTEXITCODE -ne 0) {
            throw "speedtest-runner vet failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}

Write-Output "All tests passed."
