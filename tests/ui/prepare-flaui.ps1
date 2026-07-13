$ErrorActionPreference = 'Stop'

$version = '5.0.0'
$cacheRoot = Join-Path $env:LOCALAPPDATA `
    ('ClashSpeedTestGUI\test-tools\FlaUI\' + $version)
$packageRoot = Join-Path $cacheRoot 'packages'
$binRoot = Join-Path $cacheRoot 'bin'
New-Item -ItemType Directory -Force -Path $packageRoot, $binRoot | Out-Null

$packages = @(
    [PSCustomObject]@{
        Id = 'FlaUI.Core'
        Version = '5.0.0'
        Hash = '191CC65EA82036B77F1872E6D4EBF743D3D120895BBA2FA5248D126CD6F568A7'
        Dll = 'lib\net48\FlaUI.Core.dll'
    },
    [PSCustomObject]@{
        Id = 'FlaUI.UIA3'
        Version = '5.0.0'
        Hash = 'D5D2E083539A04BF6C9053781DFD47332854A76FAEB8E9C3C68CC82109F711F2'
        Dll = 'lib\net48\FlaUI.UIA3.dll'
    },
    [PSCustomObject]@{
        Id = 'Interop.UIAutomationClient'
        Version = '10.19041.0'
        Hash = '0D2ED17DB2CB13A262F0580AB992DB87577651C25A9899053DF4114B4D9FF3B1'
        Dll = 'lib\net45\Interop.UIAutomationClient.dll'
    }
)

foreach ($package in $packages) {
    $packageName = $package.Id.ToLowerInvariant()
    $packagePath = Join-Path $packageRoot ($package.Id + '.nupkg')
    $validPackage = Test-Path -LiteralPath $packagePath
    if ($validPackage) {
        $validPackage = (Get-FileHash -LiteralPath $packagePath -Algorithm SHA256).Hash `
            -eq $package.Hash
    }

    if (-not $validPackage) {
        $temporaryPackage = $packagePath + '.' + [Guid]::NewGuid().ToString('N') + '.tmp'
        $url = 'https://api.nuget.org/v3-flatcontainer/' + $packageName + '/' `
            + $package.Version + '/' + $packageName + '.' + $package.Version + '.nupkg'
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $temporaryPackage
            $downloadedHash = (Get-FileHash -LiteralPath $temporaryPackage `
                -Algorithm SHA256).Hash
            if ($downloadedHash -ne $package.Hash) {
                throw 'Downloaded package hash mismatch: ' + $package.Id
            }
            Move-Item -LiteralPath $temporaryPackage -Destination $packagePath -Force
        }
        finally {
            if (Test-Path -LiteralPath $temporaryPackage) {
                Remove-Item -LiteralPath $temporaryPackage -Force
            }
        }
    }

    $extractRoot = Join-Path $packageRoot ($package.Id + '-' + $package.Version)
    $sourceDll = Join-Path $extractRoot $package.Dll
    if (-not (Test-Path -LiteralPath $sourceDll)) {
        $temporaryExtract = $extractRoot + '.' + [Guid]::NewGuid().ToString('N') + '.tmp'
        $temporaryZip = $temporaryExtract + '.zip'
        try {
            Copy-Item -LiteralPath $packagePath -Destination $temporaryZip
            Expand-Archive -LiteralPath $temporaryZip -DestinationPath $temporaryExtract
            if (-not (Test-Path -LiteralPath (Join-Path $temporaryExtract $package.Dll))) {
                throw 'Package does not contain the expected assembly: ' + $package.Id
            }
            if (Test-Path -LiteralPath $extractRoot) {
                Remove-Item -LiteralPath $extractRoot -Recurse -Force
            }
            Move-Item -LiteralPath $temporaryExtract -Destination $extractRoot
        }
        finally {
            if (Test-Path -LiteralPath $temporaryExtract) {
                Remove-Item -LiteralPath $temporaryExtract -Recurse -Force
            }
            if (Test-Path -LiteralPath $temporaryZip) {
                Remove-Item -LiteralPath $temporaryZip -Force
            }
        }
    }

    Copy-Item -LiteralPath $sourceDll -Destination `
        (Join-Path $binRoot ([IO.Path]::GetFileName($sourceDll))) -Force
}

[PSCustomObject]@{
    Version = $version
    Cache = $cacheRoot
    Core = (Join-Path $binRoot 'FlaUI.Core.dll')
    UIA3 = (Join-Path $binRoot 'FlaUI.UIA3.dll')
    Interop = (Join-Path $binRoot 'Interop.UIAutomationClient.dll')
}
