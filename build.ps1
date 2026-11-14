param([switch]$OfflineTools)
# 构建 Windows 服务端程序和 launcher，输出到 bin/。
$ErrorActionPreference = "Stop"

$version = Get-Date -Format "yyyyMMddHHmmss"
$buildTs = (Get-Date).ToUniversalTime().ToString("yyyyMMddHHmmss")
# 检出目录也编进二进制：多个 worktree 共用同一个 bin/，只靠时间戳版本号
# 无法回答"这份 exe 是哪个检出构建的、是不是我部署的那份"。
$sourceDir = $PSScriptRoot -replace '\\', '/'
# 提交短码也编进二进制：同一检出目录在不同提交上构建的 exe 要靠 commit 区分。
$commit = 'unknown'
try {
    $commitSha = (& git rev-parse --short HEAD 2>$null | Select-Object -First 1)
    if ($LASTEXITCODE -eq 0 -and $commitSha) { $commit = ([string]$commitSha).Trim() }
} catch { }
$ldflags = "-s -w -X 'rf4server/internal/buildinfo.Version=$version' -X 'rf4server/internal/buildinfo.BuildTs=$buildTs' -X 'rf4server/internal/buildinfo.GitCommit=$commit' -X 'rf4server/internal/buildinfo.SourceDir=$sourceDir'"
New-Item -ItemType Directory -Force -Path "bin" | Out-Null

$commands = @("gateway", "gameserver", "seed")
if ($OfflineTools) { $commands += @("import-character", "local-membership") }
foreach ($cmd in $commands) {
    go build -trimpath -ldflags $ldflags -o "bin/$cmd.exe" "./cmd/$cmd"
    if ($LASTEXITCODE -ne 0) { throw "build $cmd failed" }
    Write-Host "build bin/$cmd.exe ok"
}

# launcher 是托盘程序，不显示控制台窗口。
go build -trimpath -ldflags "$ldflags -H=windowsgui" -o "bin/launcher.exe" "./cmd/launcher"
if ($LASTEXITCODE -ne 0) { throw "build launcher failed" }
Write-Host "build bin/launcher.exe ok"

# 新架构不再生成 admin.exe；清理旧版本遗留，避免误启动独立管理端口。
Remove-Item -LiteralPath "bin/admin.exe" -Force -ErrorAction SilentlyContinue
Write-Host "done -> bin/"

if ($OfflineTools) {
    go build -trimpath -ldflags "$ldflags -H=windowsgui" -o "启动游戏.exe" ./cmd/local-launcher
    if ($LASTEXITCODE -ne 0) { throw "build local-launcher failed" }
}
