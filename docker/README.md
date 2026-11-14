# Docker Compose 部署

Windows 和 macOS 使用 Docker Desktop，Windows 需启用 Linux 容器。Linux 使用 Docker Engine 与 Compose v2 插件。镜像在本机从源码构建，支持 AMD64 和 ARM64，宿主机无需安装 Go。

进入仓库的 `docker/` 目录后执行：

```sh
docker compose up -d --build --wait
docker compose ps -a
```

Compose 先运行 `seed` 初始化 SQLite，成功后启动 `gameserver`，再启动 `gateway` 和独立 Web 管理后台。正常状态下 `seed` 显示退出码 0，其余三个服务显示 healthy。

| 宿主机地址 | 用途 |
| --- | --- |
| `http://127.0.0.1:8080/` | Web 管理后台，默认账号 `admin`，密码 `admin123` |
| `https://127.0.0.1:443/` | 客户端 HTTPS 登录 |
| `127.0.0.1:9341` | 客户端实时连接 |
| `127.0.0.1:7515` | 旧版 CN 客户端 HTTP 登录兼容口 |

端口默认只绑定宿主机的回环地址。内部隧道与观测端口由 Compose 网络承载。Linux 使用 rootless Docker 时，如遇到绑定 443 的权限限制，可通过环境变量 `RF4_HTTPS_PORT` 选择高位端口做启动验证，固定访问 443 的客户端仍需要使用 443。管理端口可通过 `RF4_ADMIN_PORT` 更改。

局域网部署时，在本目录创建 `.env`。将 `RF4_BIND_IP` 和 `RF4_GATEWAY_ADVERTISE_HOST` 都设为宿主机的局域网 IPv4，并为 `RF4_ADMIN_PASSWORD`、`RF4_ADMIN_JWT_SECRET`、`RF4_DOCKER_TUNNEL_PSK` 分别设置随机值，再执行启动命令。管理后台通过该 IPv4 的 8080 端口访问。网关登录服务面向信任的局域网，避免在路由器上转发这些端口。

RF4 客户端需单独启动。按[客户端接入说明](../docs/deploy.md#客户端接入)设置运行客户端的系统的 hosts 和证书信任。网关生成证书后，复制公钥证书到当前目录：

```sh
docker compose cp gateway:/app/certs/server.crt ./server.crt
```

```sh
docker compose logs -f
docker compose down
```

`docker compose down` 停止并删除容器。玩家存档和 HTTPS 证书分别保存在 `player-data` 与 `gateway-certs` 命名卷中，重新执行启动命令即可继续使用。只有需要清空这两项数据时才执行 `docker compose down -v`。更新代码后重新执行 `docker compose up -d --build --wait`。从仓库根目录操作时，使用 `docker compose -f docker/compose.yaml` 加对应子命令。
