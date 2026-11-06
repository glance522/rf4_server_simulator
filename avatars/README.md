# 头像图片目录

在 launcher 控制面板的“上传玩家头像”区域填写玩家用户名并选择图片。支持 PNG、JPEG、GIF，上传内容会转换为 PNG，并按玩家 UID 和图片内容哈希分别保存。用户名必须唯一；若存在重名玩家，上传会被拒绝。

launcher 默认在 `:8090` 提供头像图片。客户端地址应指向可访问该服务的主机，例如 `http://192.168.1.10:8090`。若游戏客户端运行在其他电脑上，请填写服务器的局域网 IP 或域名，不能使用 `localhost`，并确保防火墙放行该端口。

玩家头像 URL 按 UID 保存在 gameserver 和 launcher 共用的 SQLite 数据库（通常为 `data/rf4server.db`）的 `player_avatar` 表中。上传后 launcher 会通过共享数据库向 gameserver 投递该玩家的档案刷新命令；在线玩家无需重启即可看到新头像。未单独设置头像的玩家仍可使用 `config.yaml` 中的 `game.avatar_url` 作为默认头像。若更改监听端口，可启动 launcher 时传入 `-avatar-addr`，并在上传表单中填写对应的图片服务地址。
