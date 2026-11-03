# 构建 Windows 服务端程序和 launcher，输出到 bin/。
$ErrorActionPreference = "Stop"

$version = Get-Date -Format "yyyyMMddHHmmss"
$ldflags = "-s -w -X 'rf4server/internal/buildinfo.Version=$version'"
New-Item -ItemType Directory -Force -Path "bin" | Out-Null

foreach ($cmd in @("gateway", "gameserver", "admin", "seed", "import-character", "local-membership")) {
    go build -trimpath -ldflags $ldflags -o "bin/$cmd.exe" "./cmd/$cmd"
    if ($LASTEXITCODE -ne 0) { throw "build $cmd failed" }
    Write-Host "build bin/$cmd.exe ok"
}

# launcher 是托盘程序，不显示控制台窗口。
go build -trimpath -ldflags "$ldflags -H=windowsgui" -o "bin/launcher.exe" "./cmd/launcher"
if ($LASTEXITCODE -ne 0) { throw "build launcher failed" }
Write-Host "build bin/launcher.exe ok"
go build -trimpath -ldflags "$ldflags -H=windowsgui" -o "启动游戏.exe" "./cmd/local-launcher"
if ($LASTEXITCODE -ne 0) { throw "build local-launcher failed" }
Write-Host "build 启动游戏.exe ok"
Write-Host "done -> bin/ 和 启动游戏.exe"
