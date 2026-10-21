param(
    [string]$Go = 'go',
    [string]$OutputDirectory = 'dist',
    [string]$WorkDirectory,
    [ValidateSet('linux/amd64', 'linux/arm64', 'windows/amd64')]
    [string[]]$Targets = @('linux/amd64', 'linux/arm64', 'windows/amd64'),
    [switch]$IncludeLinuxTests
)
$ErrorActionPreference = 'Stop'
$previousOS = $env:GOOS
$previousArch = $env:GOARCH
$previousCGO = $env:CGO_ENABLED
$previousTmp = $env:GOTMPDIR
Push-Location $PSScriptRoot
try {
    $outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
    New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null
    if ($WorkDirectory) {
        $env:GOTMPDIR = [IO.Path]::GetFullPath($WorkDirectory)
        New-Item -ItemType Directory -Path $env:GOTMPDIR -Force | Out-Null
    }
    $artifacts = @()
    $env:CGO_ENABLED = '0'
    foreach ($target in $Targets) {
        $env:GOOS, $env:GOARCH = $target.Split('/')
        $name = if ($env:GOOS -eq 'windows') { 'gost-windows-amd64.exe' } else { "gost-$($env:GOARCH)" }
        & $Go build -mod=readonly -trimpath -ldflags '-s -w' -o (Join-Path $outputRoot $name) .
        if ($LASTEXITCODE -ne 0) { throw "$target build failed" }
        $artifacts += $name
        if ($IncludeLinuxTests -and $env:GOOS -eq 'linux') {
            New-Item -ItemType Directory -Path (Join-Path $outputRoot 'tests') -Force | Out-Null
            $testName = "tests/wg-tests-$($env:GOARCH)"
            Push-Location 'x'
            try {
                & $Go test -mod=readonly -trimpath -c -o (Join-Path $outputRoot $testName) ./dialer/wg
                if ($LASTEXITCODE -ne 0) { throw "$target test compilation failed" }
            } finally { Pop-Location }
            $artifacts += $testName
        }
    }
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'verify-linux.sh') -Destination (Join-Path $outputRoot 'verify-linux.sh')
    $artifacts += 'verify-linux.sh'
    $checksums = foreach ($name in $artifacts) {
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $outputRoot $name)).Hash.ToLowerInvariant()
        "$hash  $name"
    }
    [IO.File]::WriteAllText((Join-Path $outputRoot 'SHA256SUMS'), (($checksums -join "`n") + "`n"), [Text.UTF8Encoding]::new($false))
    $checksums
} finally {
    $env:GOOS = $previousOS
    $env:GOARCH = $previousArch
    $env:CGO_ENABLED = $previousCGO
    $env:GOTMPDIR = $previousTmp
    Pop-Location
}
