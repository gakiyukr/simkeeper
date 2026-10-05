-- SimKeeper 数据库结构（SQLite）。
-- 与 PHP 版的差异：删除了从未被业务逻辑使用的死字段
-- （renewal_reminder_days / usage_reminder_days / last_usage_date），
-- 实际生效的提醒提前量是 renewal_days_before / usage_days_before。
-- 时间统一存 "YYYY-MM-DD HH:MM:SS" 文本，日期为 YYYY-MM-DD。
-- WAL 与 foreign_keys 由 db.Open 的 DSN 编译参数开启，这里不再写 PRAGMA。

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE,
    email         TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL DEFAULT 'user'   CHECK (role   IN ('user', 'admin')),
    totp_secret   TEXT,                          -- TOTP 两步验证密钥（Base32）；空 = 未设置
    totp_enabled  INTEGER NOT NULL DEFAULT 0,    -- 验证器确认后置 1
    status        TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'banned')),
    created_at    TEXT    NOT NULL DEFAULT (datetime('now', 'localtime')),
    updated_at    TEXT    NOT NULL DEFAULT (datetime('now', 'localtime'))
);
CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);

CREATE TABLE IF NOT EXISTS phone_numbers (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id               INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    phone_number          TEXT    NOT NULL,
    country_code          TEXT    NOT NULL,
    country_name          TEXT    NOT NULL,
    carrier               TEXT,
    expiry_date           TEXT    NOT NULL,              -- 到期日 YYYY-MM-DD
    recharge_amount       REAL,
    recharge_currency     TEXT    NOT NULL DEFAULT 'USD',
    renewal_days_before   INTEGER NOT NULL DEFAULT 7,    -- 续费提醒提前天数
    usage_days_before     INTEGER NOT NULL DEFAULT 3,    -- 使用提醒提前天数
    auto_expiry_enabled   INTEGER NOT NULL DEFAULT 0,    -- 自动续期开关
    auto_start_date       TEXT,                          -- 自动续期起始日
    auto_expiry_period    INTEGER CHECK (auto_expiry_period IS NULL OR auto_expiry_period BETWEEN 1 AND 3650),
    auto_calculated_expiry TEXT,                         -- 最近一次自动计算出的到期日
    no_keepalive          INTEGER NOT NULL DEFAULT 0,    -- 无需保号：不参与到期提醒
    plan_name             TEXT,                          -- 方案信息：套餐名称
    secondary_numbers     TEXT,                          -- 副卡号码，每行一个
    device_id             INTEGER,                       -- 安装设备（devices.id，NULL = 未指定；删除设备时应用层置空）
    sim_type              TEXT    NOT NULL DEFAULT 'physical' CHECK (sim_type IN ('physical', 'esim')),
    lpa_string            TEXT,                          -- eSIM LPA 激活码（enc:v1: 密文）
    confirm_code          TEXT,                          -- eSIM 确认码（enc:v1: 密文）
    status                TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    notes                 TEXT,
    created_at            TEXT    NOT NULL DEFAULT (datetime('now', 'localtime')),
    updated_at            TEXT    NOT NULL DEFAULT (datetime('now', 'localtime'))
);
CREATE INDEX IF NOT EXISTS idx_numbers_user   ON phone_numbers(user_id);
CREATE INDEX IF NOT EXISTS idx_numbers_expiry ON phone_numbers(expiry_date);
CREATE INDEX IF NOT EXISTS idx_numbers_status ON phone_numbers(status);

-- 设备：eSIM 安装在什么硬件上（单账号自用记录）
CREATE TABLE IF NOT EXISTS devices (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    device_type TEXT    NOT NULL DEFAULT 'phone',
    notes       TEXT,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now', 'localtime')),
    updated_at  TEXT    NOT NULL DEFAULT (datetime('now', 'localtime'))
);
CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id);

-- 每个用户一行通知渠道配置
CREATE TABLE IF NOT EXISTS notification_configs (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    email_enabled      INTEGER NOT NULL DEFAULT 0,
    email_smtp_host    TEXT,
    email_smtp_port    INTEGER NOT NULL DEFAULT 587,
    email_smtp_secure  TEXT    NOT NULL DEFAULT 'tls' CHECK (email_smtp_secure IN ('tls', 'ssl', 'none')),
    email_smtp_username TEXT,
    email_from_email   TEXT,
    email_from_name    TEXT,
    email_password     TEXT,
    email_to_email     TEXT,                          -- 收件地址；空则回退用户邮箱
    telegram_enabled   INTEGER NOT NULL DEFAULT 0,
    telegram_bot_token TEXT,
    telegram_chat_id   TEXT,
    wxpusher_enabled   INTEGER NOT NULL DEFAULT 0,
    wxpusher_app_token TEXT,
    wxpusher_uid       TEXT,
    feishu_enabled     INTEGER NOT NULL DEFAULT 0,     -- 飞书群自定义机器人
    feishu_webhook     TEXT,
    feishu_secret      TEXT,                           -- 加签密钥（机器人未开签名则留空）
    dingtalk_enabled   INTEGER NOT NULL DEFAULT 0,     -- 钉钉群自定义机器人
    dingtalk_webhook   TEXT,
    dingtalk_secret    TEXT,                           -- 加签密钥（关键词/IP 白名单模式则留空）
    tgcall_enabled     INTEGER NOT NULL DEFAULT 0,     -- TG 未接来电闹铃（MTProto 用户账号）
    tgcall_api_id      INTEGER,                        -- my.telegram.org 的 api_id
    tgcall_api_hash    TEXT,                           -- api_hash
    tgcall_phone       TEXT,                           -- 系统 TG 账号手机号
    tgcall_session     TEXT,                           -- 登录会话（base64，由 tg-login 写入）
    tgcall_target      TEXT,                           -- 被叫账号（@用户名 或手机号）
    created_at         TEXT    NOT NULL DEFAULT (datetime('now', 'localtime')),
    updated_at         TEXT    NOT NULL DEFAULT (datetime('now', 'localtime'))
);

CREATE TABLE IF NOT EXISTS notifications (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    phone_number_id INTEGER REFERENCES phone_numbers(id) ON DELETE SET NULL,
    type            TEXT    NOT NULL CHECK (type IN ('renewal', 'usage', 'test', 'system')),
    channel         TEXT    NOT NULL CHECK (channel IN ('email', 'telegram', 'wxpusher', 'feishu', 'dingtalk', 'tgcall', 'system')),
    subject         TEXT,
    message         TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    retry_count     INTEGER NOT NULL DEFAULT 0,   -- 定时任务重投次数（上限 3）
    error_message   TEXT,
    sent_at         TEXT,
    created_at      TEXT    NOT NULL DEFAULT (datetime('now', 'localtime'))
);
CREATE INDEX IF NOT EXISTS idx_notif_user      ON notifications(user_id);
CREATE INDEX IF NOT EXISTS idx_notif_number    ON notifications(phone_number_id);
CREATE INDEX IF NOT EXISTS idx_notif_status    ON notifications(status);
CREATE INDEX IF NOT EXISTS idx_notif_created   ON notifications(created_at);


-- 密码重置令牌：只存 SHA-256 哈希（原文仅存在于邮件链接），1 小时有效、单次使用
CREATE TABLE IF NOT EXISTS password_resets (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (datetime('now', 'localtime')),
    expires_at TEXT    NOT NULL,
    used_at    TEXT
);
CREATE INDEX IF NOT EXISTS idx_presets_token ON password_resets(token_hash);
CREATE INDEX IF NOT EXISTS idx_presets_user  ON password_resets(user_id);

CREATE TABLE IF NOT EXISTS system_settings (
    setting_key   TEXT PRIMARY KEY,
    setting_value TEXT,
    description   TEXT
);

-- 登录失败记录，用于 IP 维度限流
CREATE TABLE IF NOT EXISTS login_attempts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ip           TEXT NOT NULL,
    username     TEXT,
    attempted_at TEXT NOT NULL DEFAULT (datetime('now', 'localtime'))
);
CREATE INDEX IF NOT EXISTS idx_attempts_ip_time ON login_attempts(ip, attempted_at);

-- 服务器端会话；Cookie 只保存不透明 token。
-- user_id 为 NULL 表示匿名预会话：仅承载登录/注册/初始化页面的 CSRF 令牌，
-- 登录成功后建立全新会话并销毁预会话（等价于 PHP 版的 session_regenerate_id）。
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
    ip         TEXT,
    user_agent TEXT,
    csrf_token TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now', 'localtime')),
    expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_exp  ON sessions(expires_at);

-- schema 版本记录：此后每次不兼容结构变更递增版本号并配迁移代码
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now', 'localtime'))
);
INSERT OR IGNORE INTO schema_migrations (version) VALUES (1);
