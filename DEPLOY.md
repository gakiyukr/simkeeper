# SimKeeper 部署指南（VPS）

从零到能在 HTTPS 域名上访问的完整流程。以 Ubuntu/Debian 为例，其他发行版只差包管理器命令。

## 0. 前置

- 一台 VPS（1 核 512MB 起步足够）+ 一个解析到它的域名
- 本机装有 Go 1.27+（编译产物是静态二进制，服务器上**不需要装 Go**）

## 1. 构建

在本机项目根目录：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o simkeeper .
```

`CGO_ENABLED=0` 保证纯静态（SQLite 用的是纯 Go 驱动），产物约 20MB。

## 2. 服务器目录与账号

```bash
sudo useradd -r -s /usr/sbin/nologin simkeeper
sudo mkdir -p /opt/simkeeper /var/lib/simkeeper
sudo scp simkeeper user@vps:/tmp/   # 从本机上传
sudo mv /tmp/simkeeper /opt/simkeeper/
sudo chmod 755 /opt/simkeeper/simkeeper
sudo chown -R simkeeper:simkeeper /var/lib/simkeeper
```

- `/opt/simkeeper` — 二进制（对服务账号只读）
- `/var/lib/simkeeper` — 数据库目录（唯一可写位置）

## 3. 时区（重要，先做）

提醒的「当天」判断依赖**进程的本地时区**。时区不对，提醒窗口会错位（最多一整天），当天去重也会跟着错。

```bash
sudo timedatectl set-timezone Asia/Shanghai   # 换成你的时区
```

或在 systemd unit 里显式设置 `Environment=TZ=Asia/Shanghai`（unit 文件已预置）。**启动日志会打印当前时区**，部署后看一眼：

```
[main] 本地时区: CST (UTC+08:00) — 与预期不符请设置 TZ 环境变量
```

## 4. systemd

```bash
sudo cp deploy/simkeeper.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now simkeeper
sudo systemctl status simkeeper        # 应为 active (running)
journalctl -u simkeeper -f             # 看启动日志（含时区自检）
```

unit 已默认开启 `-trust-proxy` 与 `-secure-cookies`（配合下一步的反代），并带基础沙箱（ProtectSystem=strict 等）。

## 5. HTTPS 反向代理（Caddy）

Caddy 自动申请并续期证书，两行配置：

```bash
sudo cp deploy/Caddyfile.example /etc/caddy/Caddyfile
# 编辑域名，然后：
sudo systemctl reload caddy
```

打开 `https://sim.example.com`，首次访问 `/setup` 创建管理员（创建后入口永久关闭）。

**Nginx 替代**（自行处理证书，如 certbot）：

```nginx
server {
    listen 443 ssl http2;
    server_name sim.example.com;
    ssl_certificate     /etc/letsencrypt/live/sim.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/sim.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Host $host;
    }
}
```

要点：`X-Forwarded-For` 必须转发（限流按真实 IP 计数，程序取链**最右值**，客户端伪造的前缀无效）。不要把 `/setup` 之外的路径做特殊处理——未登录一律被程序自己重定向。

> 没有反代直接裸跑 HTTP？密码和会话就是明文传输。**务必上 HTTPS**。

所有命令行参数都有环境变量等价（`SK_TRUST_PROXY` / `SK_SECURE_COOKIES` / `SK_ADDR` / `SK_CRON_INTERVAL` / `SK_DRIVER` / `SK_DB` / `SK_DSN`），想在 systemd 里统一用环境变量也可以（`Environment=` 行），两种写法等价。

## 6. 数据库选择

- 默认 SQLite（零配置）。备份 = 备份 `/var/lib/simkeeper/simkeeper.db` 一个文件（WAL 模式下用 `sqlite3 simkeeper.db ".backup '/backup/simkeeper.db'"` 更安全）。
- 要用 MySQL/PostgreSQL：先建库建用户，然后在 unit 的 `ExecStart` 加 `-driver mysql -dsn '...'`（格式见 README）。**数据库不要对公网开放**；如果数据库在本机之外，用内网/专线/VPN，不要裸公网。

## 7. 通知渠道配置

登录 → 「通知配置」：6 个渠道任意组合。TG 电话需要 api_id/api_hash（my.telegram.org 免费申请）+ 上传 tdata 或 `simkeeper tg-login` 登录一次。配完点「发送测试通知」验证。

## 8. 升级

```bash
# 本机重新编译 → 上传 → 替换 → 重启
sudo systemctl restart simkeeper
```

schema 迁移在启动时自动执行（幂等，含老库补列），无需手工干预。降级前先看 release 说明是否含不兼容迁移。

## 9. 安全清单

- [ ] HTTPS 已启用（-secure-cookies 与 -trust-proxy 已随 unit 开启）
- [ ] 时区已确认（启动日志）
- [ ] SQLite 用户：数据库文件在 `/var/lib/simkeeper`，权限 750（对 simkeeper 组外不可读——里面是明文凭据和手机号）
- [ ] MySQL/PG 用户：数据库端口不对公网
- [ ] 备份已设置（含数据库的备份要加密存放——渠道凭据是明文的）
- [ ] `cron-interval` 保持默认 1h；不要完全禁用进程内调度（提醒会停）
- [ ] 定期升级（`systemctl restart` 即可，迁移自动）
