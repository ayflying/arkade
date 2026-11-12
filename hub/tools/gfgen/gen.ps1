# Generate only with official gf v2.9.6. GFGEN_DSN must target a dedicated PG database.
$ErrorActionPreference = 'Stop'
if (-not $env:GFGEN_DSN) { throw 'Set GFGEN_DSN to a dedicated PostgreSQL database URL.' }
$Dsn = [uri]$env:GFGEN_DSN
if ($Dsn.Scheme -notin @('postgres','postgresql')) { throw 'GFGEN_DSN must be a PostgreSQL URL.' }
$GfBin = $env:GF_BIN
if (-not $GfBin) { $GfBin = 'D:\gopath\gf296\gf.exe' }
if (-not (Test-Path $GfBin)) { throw 'Set GF_BIN to the official gf v2.9.6 CLI.' }
$HubDir = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$Work = Join-Path ([System.IO.Path]::GetTempPath()) ('arkade_gfgen_' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Work | Out-Null
Push-Location $HubDir
try {
    & go run ./tools/gfgen/mkgen $env:GFGEN_DSN
    if ($LASTEXITCODE -ne 0) { throw 'Migration failed' }
    $Tables = (& go run ./tools/gfgen/mktables $env:GFGEN_DSN) -join ''
    if ($LASTEXITCODE -ne 0 -or -not $Tables) { throw 'Business table discovery failed' }
    $GoMod = "module arkade/hub`n`ngo 1.24.0`n`nrequire (`n github.com/gogf/gf/contrib/drivers/pgsql/v2 v2.9.6`n github.com/gogf/gf/v2 v2.9.6`n)`n"
    [System.IO.File]::WriteAllText((Join-Path $Work 'go.mod'), $GoMod)
    Copy-Item ./go.sum (Join-Path $Work 'go.sum')
    New-Item -ItemType Directory -Force -Path (Join-Path $Work 'manifest\config'),(Join-Path $Work 'internal') | Out-Null
    $Config = (& go run ./tools/gfgen/mkconfig $env:GFGEN_DSN) -join "`n"
    if ($LASTEXITCODE -ne 0) { throw 'GF configuration rendering failed' }
    [System.IO.File]::WriteAllText((Join-Path $Work 'manifest\config\config.json'), $Config)
    Push-Location $Work
    try {
        & $GfBin gen dao -p internal -t $Tables
        if ($LASTEXITCODE -ne 0) { throw 'Official gf generation failed' }
    } finally { Pop-Location }
    foreach ($Sub in @('dao','model\do','model\entity')) {
        $Source = Join-Path $Work "internal\$Sub"
        if (-not (Test-Path $Source)) { continue }
        $Destination = Join-Path $HubDir "internal\$Sub"
        foreach ($File in (Get-ChildItem $Source -File -Recurse -Filter *.go)) {
            $Relative = $File.FullName.Substring($Source.Length).TrimStart('\')
            $Target = Join-Path $Destination $Relative
            New-Item -ItemType Directory -Force -Path (Split-Path $Target -Parent) | Out-Null
            Copy-Item -Force $File.FullName $Target
        }
    }
} finally {
    Pop-Location
    # Confirm the exact generated temporary directory before removing it.
    if ((Resolve-Path $Work).Path -eq $Work -and (Split-Path $Work -Leaf).StartsWith('arkade_gfgen_')) { Remove-Item -LiteralPath $Work -Recurse -Force }
}
