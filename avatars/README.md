# 头像图片目录

在 launcher 控制面板的“上传玩家头像”区域选择图片并上传。支持 PNG、JPEG、GIF，上传内容会转换并保存为 `avatar.png`；玩家档案的 `game.avatar_url` 会同步更新。

launcher 默认在 `:8090` 提供头像图片。客户端地址应指向可访问该服务的主机，例如 `http://192.168.1.10:8090/avatars/avatar.png`。上传区域里的 URL 可以编辑；若游戏客户端运行在其他电脑上，请填写服务器的局域网 IP 或域名，不能使用 `localhost`，并确保防火墙放行该端口。

上传后重启 gameserver，新头像才会写入玩家档案。若更改监听端口，可启动 launcher 时传入 `-avatar-addr`，并在上传 URL 中填写对应端口。
