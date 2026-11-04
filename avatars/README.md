# 头像图片目录

把头像图片放在此目录中。例如将图片保存为 `avatar.png`，启动头像服务后可通过以下地址访问：

`http://<服务器地址>:8090/avatars/avatar.png`

在游戏服务器的 `BuildProfile307` 中，将头像字段设置为客户端可访问的完整 URL。客户端与头像服务不在同一台电脑时，不要使用 `localhost`，请填写运行头像服务的服务器 IP 或域名。

从仓库根目录启动服务：

```powershell
go run ./cmd/avatarserver
```

可通过 `-addr` 指定监听地址、通过 `-dir` 指定图片目录：

```powershell
go run ./cmd/avatarserver -addr :8090 -dir .\avatars
```
