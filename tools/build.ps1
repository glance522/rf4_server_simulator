# build.ps1 - rebuild one service (default gameserver) with the repo's standard flags
#
# Usage:
#   .\tools\build.ps1              # gameserver
#   .\tools\build.ps1 gateway      # any of gameserver/gateway/seed/launcher
#   .\tools\build.ps1 all
#
# Important: keep -s -w. An unstripped 26MB build was blocked by Windows
# Smart App Control ("An Application Control policy has blocked this file");
# the stripped build runs normally.
param([string]$Target = 'gameserver', [switch]$OfflineTools)

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
Set-Location $root

$go = $null
$cmd = Get-Command go -ErrorAction SilentlyContinue
if ($cmd) { $go = $cmd.Source }
if (-not $go) {
    foreach ($p in @('C:\Program Files\Go\bin\go.exe', 'C:\Go\bin\go.exe')) {
        if (Test-Path $p) { $go = $p; break }
    }
}
if (-not $go) { throw 'go not found (install Go or add it to PATH)' }

$version = Get-Date -Format 'yyyyMMddHHmmss'
# 检出目录也编进二进制：多个 worktree 共用同一个 bin/，只靠时间戳版本号
# 无法回答"这份 exe 是哪个检出构建的、是不是我部署的那份"。
$sourceDir = $root -replace '\\', '/'
$buildTs = (Get-Date).ToUniversalTime().ToString('yyyyMMddHHmmss')
# 提交短码也编进二进制：同一检出目录在不同提交上构建的 exe 要靠 commit 区分。
$commit = 'unknown'
try {
    $commitSha = (& git rev-parse --short HEAD 2>$null | Select-Object -First 1)
    if ($LASTEXITCODE -eq 0 -and $commitSha) { $commit = ([string]$commitSha).Trim() }
} catch { }
$ld = "-s -w -X 'rf4server/internal/buildinfo.Version=$version' -X 'rf4server/internal/buildinfo.BuildTs=$buildTs' -X 'rf4server/internal/buildinfo.GitCommit=$commit' -X 'rf4server/internal/buildinfo.SourceDir=$sourceDir'"
$targets = if ($Target -eq 'all') { @('gateway', 'gameserver', 'seed', 'launcher') } else { @($Target) }

if ($Target -eq 'all' -and (Test-Path 'bin/admin.exe')) {
    Remove-Item -LiteralPath 'bin/admin.exe' -Force
}

if ($OfflineTools -and $Target -eq 'all') { $targets += @('import-character', 'local-membership', 'local-launcher') }
foreach ($t in $targets) {
    $out = "bin/$t.exe"
    $flags = $ld
    if ($t -in @('launcher', 'local-launcher')) { $flags += ' -H=windowsgui' }
    if ($t -eq 'local-launcher') { $out = '启动游戏.exe' }
    & $go build -trimpath -ldflags $flags -o $out "./cmd/$t"
    if ($LASTEXITCODE -ne 0) { throw "build $t failed" }
    Write-Host ("built {0}  ({1:N1} MB)" -f $out, ((Get-Item $out).Length / 1MB)) -ForegroundColor Green
}
