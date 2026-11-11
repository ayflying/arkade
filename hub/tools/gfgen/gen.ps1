# GoFrame DAO 代码生成（隔离环境版，Windows）。
#
# 为什么不能在 hub/ 直接跑 gf gen dao，见同目录 README.md。
#
# 用法：
#   pwsh ./tools/gfgen/gen.ps1
#
# 环境变量：
#   GF_BIN  gf CLI 可执行文件路径（默认 D:\gopath\gf296\gf.exe）
$ErrorActionPreference = 'Stop'

$HubDir = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
Push-Location $HubDir
try {
    # ---------- 定位 gf CLI ----------
    $GfBin = $env:GF_BIN
    if (-not $GfBin) {
        foreach ($cand in @('D:\gopath\gf296\gf.exe')) {
            if (Test-Path $cand) { $GfBin = $cand; break }
        }
    }
    if (-not $GfBin -or -not (Test-Path $GfBin)) {
        throw '找不到 gf CLI（v2.9.6）。用 GF_BIN 环境变量指定路径。'
    }
    Write-Host "使用 gf CLI: $GfBin"

    # ---------- 建临时生成环境 ----------
    $Work = Join-Path ([System.IO.Path]::GetTempPath()) ("gfgen_" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force -Path (Join-Path $Work 'manifest\config'), (Join-Path $Work 'internal') | Out-Null
    $DbPath = Join-Path $Work 'gen.db'

    try {
        # ---------- 1. 用 hub 的真实迁移建库 ----------
        Write-Host '==> 建库（走 hub 的真实迁移）'
        $env:CGO_ENABLED = '0'
        & go run ./tools/gfgen/mkgen $DbPath
        if ($LASTEXITCODE -ne 0) { throw "建库失败（exit $LASTEXITCODE）" }

        # ---------- 2. 生成环境配置 ----------
        # module 名与 hub 相同：生成物里会出现 "arkade/hub/internal/dao/internal"，
        # 这样原样拷贝进 hub 就是正确 import，无需事后改写。
        $goMod = @"
module arkade/hub

go 1.24.0

require (
	github.com/gogf/gf/contrib/drivers/sqlite/v2 v2.9.6
	github.com/gogf/gf/v2 v2.9.6
)
"@
        $utf8 = New-Object System.Text.UTF8Encoding $false
        [System.IO.File]::WriteAllText((Join-Path $Work 'go.mod'), $goMod.Replace("`r`n", "`n"), $utf8)
        Copy-Item (Join-Path $HubDir 'go.sum') (Join-Path $Work 'go.sum')

        # 正斜杠：YAML 里反斜杠会被当转义符。
        $DbFwd = $DbPath.Replace('\', '/')
        $cfg = @"
database:
  default:
    type: sqlite
    name: $DbFwd
"@
        [System.IO.File]::WriteAllText((Join-Path $Work 'manifest\config\config.yaml'), $cfg.Replace("`r`n", "`n"), $utf8)

        # ---------- 3. 读出业务表名 ----------
        Write-Host '==> 读取业务表'
        $tables = (& go run ./tools/gfgen/mktables $DbPath) -join ''
        if (-not $tables) { throw '没有找到任何业务表' }
        Write-Host "==> 生成表的 DAO: $tables"

        # ---------- 4. 调用 gf CLI ----------
        Write-Host '==> gf gen dao'
        Push-Location $Work
        try {
            & $GfBin gen dao -p internal -t $tables
            if ($LASTEXITCODE -ne 0) { throw "gf gen dao 失败（exit $LASTEXITCODE）" }
        } finally { Pop-Location }

        # ---------- 5. 覆盖回 hub ----------
        #
        # ============================ 只覆盖本次生成的文件 ============================
        # **绝不能删整个目录再拷**：dao/ 下可能有人手写的扩展文件
        # （惯例命名 custom_*.go，也可能有其他名字），它们不属于生成物，
        # 一旦被删就是不可逆的代码丢失。
        #
        # 所以这里的策略是"按本次生成列表逐个覆盖"：
        #   - 只碰 gf 本次真实产出的文件名；
        #   - 目录里别的东西（手写文件、.gitkeep 等）一律不动；
        #   - 生成物中已被删除的表不会自动清理（这是刻意的：
        #     我们无法区分"表被删了"和"文件是手写的"，
        #     宁可留一个孤儿文件让人看见，也不要误删）。
        Write-Host '==> 覆盖到 hub/internal（只覆盖生成物，保留手写文件）'
        $copied = 0
        foreach ($sub in @('dao', 'model\do', 'model\entity')) {
            $src = Join-Path $Work "internal\$sub"
            if (-not (Test-Path $src)) { continue }
            $dst = Join-Path $HubDir "internal\$sub"
            New-Item -ItemType Directory -Force -Path $dst | Out-Null

            # -Recurse 以覆盖 dao/internal/ 子目录；只枚举 .go 文件。
            foreach ($f in (Get-ChildItem $src -Recurse -Filter *.go -File)) {
                $rel = $f.FullName.Substring($src.Length).TrimStart('\')
                $target = Join-Path $dst $rel
                $targetDir = Split-Path $target -Parent
                if (-not (Test-Path $targetDir)) {
                    New-Item -ItemType Directory -Force -Path $targetDir | Out-Null
                }
                Copy-Item -Force $f.FullName $target
                $copied++
            }
        }
        Write-Host "==> 覆盖 $copied 个生成文件（其余文件保持原样）"

        # 同一颗检查：清点生成的标识文件是否齐全（gf 会固定产出这四类）。
        Write-Host "==> 完成。本次覆盖 $copied 个生成文件"
    } finally {
        Remove-Item -Recurse -Force $Work -ErrorAction SilentlyContinue
    }
} finally {
    Pop-Location
}
