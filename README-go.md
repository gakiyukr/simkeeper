# 保号通 Go 版

PHP 版（[baohaotong](../baohaotong)）的 Go 重写：**单个静态二进制**，内嵌模板与数据库 schema，支持 **SQLite / MySQL / PostgreSQL** 三种数据库（默认 SQLite，纯 Go 驱动无 CGO），不再依赖 crontab 和任何外部 CDN 资源。

```
go build -o baohaotong .
./baohaotong
# 首次启动访问 http://127.0.0.1:8080/setup 创建管理员（创建后入口永久关闭）
```

## 数据库

| 驱动 | 连接方式 | 说明 |
| --- | --- | --- |
| `sqlite`（默认） | `-db data/baohaotong.db` | 零配置，数据即一个文件；WAL + 外键由连接参数开启 |
| `mysql` | `-dsn 'user:pass@tcp(127.0.0.1:3306)/baohaotong?charset=utf8mb4'` | 建议 MySQL 8.0+ / MariaDB 10.5+ |
| `postgres` | `-dsn 'postgres://user:pass@127.0.0.1:5432/baohaotong?TimeZone=Asia/Shanghai'` | 建议 PG 12+；时间列存 TEXT，请把会话时区设为业务时区 |

环境变量等价写法：`BHT_DRIVER` / `BHT_DB` / `BHT_DSN`。schema 按驱动自动选择（`internal/db/schema_*.sql`），全为幂等建表，可安全重复执行。方言差异集中在 `internal/db/dialect.go`（UPSERT、当日去重谓词、`?`→`$n` 占位符重绑、PostgreSQL 的 `RETURNING id`），并有单元测试覆盖生成的 SQL 形态。

三方言统一约定：**时间戳一律由 Go 侧生成为 "YYYY-MM-DD HH:MM:SS" 文本写入**（SQLite 用 TEXT 列、MySQL 用 DATETIME 列且连接不开 parseTime、PG 用 TEXT 列），保证排序、比较与 Go 侧解析行为完全一致。

> 实测覆盖：SQLite 在 Windows（Go 1.27）上完成过全流程端到端冒烟测试；MySQL / PostgreSQL 因开发环境无实例，仅通过编译、方言单元测试与连接失败路径验证，**未对真实服务端做过完整回归**——上线前建议先跑一遍安装初始化与一次手动 cron 触发。

## 运行配置

命令行参数与环境变量等价（环境变量名：`BHT_DRIVER` / `BHT_DB` / `BHT_DSN` / `BHT_ADDR` / `BHT_TRUST_PROXY` / `BHT_CRON_INTERVAL`）：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-driver` | `sqlite` | 数据库驱动：sqlite / mysql / postgres |
| `-db` | `data/baohaotong.db` | SQLite 数据库文件路径（目录自动创建） |
| `-dsn` | — | MySQL/PostgreSQL 连接串（该两种驱动必填） |
| `-addr` | `127.0.0.1:8080` | HTTP 监听地址 |
| `-trust-proxy` | `false` | 部署在可信反向代理后时开启，登录限流才按 `X-Forwarded-For` 取真实 IP |
| `-cron-interval` | `1h` | 定时任务间隔（`30m`/`1h`）；`0` 禁用进程内调度，仅保留管理后台手动触发 |

## 目录结构

```
main.go                    入口：配置（驱动/DSN）、迁移、启动、优雅关闭
internal/db/               连接包装（*DB：Rebind / InsertID）、Dialect 方言层、三份内嵌 schema
internal/auth/             bcrypt 密码、服务器端会话、CSRF
internal/store/            数据访问层（users / numbers / notify / settings），全部参数化查询
internal/notify/           三个渠道：Email（标准库 SMTP，含 AUTH LOGIN）、Telegram、WxPusher
internal/cronjob/          定时任务：自动续期滚动 → 到期提醒 → 数据清理 → 统计
internal/web/              路由、中间件、处理器、内嵌模板
```

## 与 PHP 版的差异

**架构**

- 单二进制 + SQLite（默认）：零配置开箱即用，无需 crontab（定时任务由进程内调度器执行，管理后台仍可手动触发）；也可选 MySQL / PostgreSQL（见上）。
- 模板与 schema 用 `embed` 打进二进制；CSS 内嵌，不依赖 Bootstrap/jQuery 等 CDN。

**安全加固（修复 PHP 版已知问题）**

- 服务器端会话表：Cookie 只存不透明 token；登录成功后销毁旧会话再发新 token（会话固定防护）；改密后撤销该用户全部会话。
- 登出、手动触发 cron、全部状态变更一律 POST + CSRF；不存在 GET 触发写操作的路径。
- SMTP 密码 / Bot Token 保存后**不回显**页面（PHP 版把 smtp_password 回显进 HTML）。
- 测试发送接口不接受任何 host/port 参数（PHP 版 send_test 存在 SSRF 面），发送目标只来自已保存的渠道配置。
- 登录限流的 IP 归属默认只认 `RemoteAddr`，`X-Forwarded-For` 须显式开启 `-trust-proxy` 才信任。
- 用户管理增加保护：不能操作当前登录账号，不允许封禁/删除/降级最后一名 active 管理员。
- Cookie 传递的 flash 提示做 URL 编码，避免 Go `http.SetCookie` 静默丢弃非 ASCII 值。

**行为对齐**

- 两类提醒（续费/使用）、每号码独立提前天数、当天去重、自动续期 90/180/365 天滚动、过期号码不提醒等业务语义与 PHP 版一致。
- 邮件正文沿用 multipart/alternative（纯文本 + 品牌样式 HTML），主题按 RFC 2047 编码；Telegram 发纯文本不带 parse_mode。

**通知渠道（共 6 个）**

邮件（SMTP）、Telegram Bot、WxPusher 之外，Go 版新增三个渠道：

- **飞书**：群自定义机器人 webhook，支持官方加签（HMAC-SHA256，密钥=时间戳+换行+secret）。
- **钉钉**：群自定义机器人 webhook，支持官方加签；安全设置选「自定义关键词」时建议把关键词设为「保号通」。
- **TG 电话（未接来电闹铃）**：提醒触发时用系统持有的 Telegram **用户账号**（MTProto）呼叫你的 TG 账号，铃响后变未接来电——比消息更难错过。Bot API 无法发起通话，此功能基于 gotd/td（纯 Go MTProto 客户端）实现「响铃即走」。账号接入二选一：
  - **上传 tdata**（推荐，无需验证码）：把已登录的 Telegram Desktop 的 `tdata` 文件夹压缩成 zip，在「通知配置」页上传即可。解析在内存中完成（不落盘），支持桌面端本地密码。注意：tdata 等同账号完全访问权，请务必经 HTTPS 上传；导入的会话与桌面端共享，**在 Telegram 里「终止」那个桌面会话会一并失效**。
  - **tg-login 验证码登录**：先到 my.telegram.org 申请 api_id/api_hash，页面保存配置后在服务器运行 `baohaotong tg-login -username 用户名`，按提示输入验证码（两步验证密码会提示输入），会话存入数据库。

  两种方式都需要填写自己的 api_id/api_hash（tdata 不包含它们）。**呼叫功能未在真实账号上联调**，上线后先用「测试发送」验证一次。

所有渠道的密码/令牌/webhook URL 保存后一律不回显页面（两家 webhook 内嵌 token，与密码同等对待），表单留空表示保持不变；测试发送会把每个渠道的成功/失败（含服务端错误原文）写进通知历史。

**已省略**

- 手机号国家码候选精简为常见 eSIM 来源地（`handlers_numbers.go` 里的 `Countries`，可自行扩充）。
- 管理后台的统计图表页与逐条通知详情页：概览统计并入后台首页，失败原因直接在列表展示。
- eSIM 安装助手（`esim/`）：纯前端小工具，与主应用无关，未移植。

## 已验证

- `go build` / `go vet` / `gofmt` 全绿，Go 1.27；`go test` 覆盖数据库方言生成器与飞书/钉钉签名算法（参考向量由独立 Python 实现按官方文档算法计算）。
- SQLite 端到端冒烟测试（真实 HTTP 交互）：初始化管理员 → `/setup` 锁定 → 登录（按角色跳转）→ 添加号码（手动 + 自动续期两种模式）→ 配置全部 6 个渠道（凭据不回显、留空保持不变、渠道启用完整性校验）→ 手动触发定时任务（仅窗口内号码触发，各渠道失败原因留档）→ 通知历史 / CSV 导出 / 测试发送 API → 注册第二用户 → 越权隔离 → CSRF 403 → 未登录/非管理员被拦截。
- 飞书/钉钉渠道对真实服务端发过请求：假 webhook 分别收到飞书 `code=19001` 与钉钉 `errcode=300005` 的服务端拒绝——请求格式、加签、响应解析全链路正确，换真实 webhook 即可工作。
- 老库增量迁移：旧结构（16 列配置表）启动后自动补齐 12 个新列，原有数据保留。
- TG 电话：代码结构完整、错误路径已测（未配置/未登录时如实报错并留档），但**未在真实 Telegram 账号上联调**——需要真实 api_id/api_hash 与一次交互式登录，上线后先跑「测试发送」验证。
- MySQL / PostgreSQL：编译与方言单元测试就绪，连接失败路径验证过；**未对真实服务端做过完整回归**，上线前请先初始化并手动触发一轮 cron。
