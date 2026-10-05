-- SimKeeper 数据库结构（PostgreSQL 12+）。
-- 与 SQLite 版的字段与约束一致，仅类型与 DDL 语法按 PostgreSQL 方言：
--  - 主键用 BIGSERIAL，与另两种方言一样从 1 起自增；插入取 ID 走 RETURNING id
--    （见 db.InsertID，PostgreSQL 不支持 LastInsertId）。
--  - 时间列存 TEXT（格式 "YYYY-MM-DD HH:MM:SS"，与另两种方言的存取约定一致），
--    默认值用 to_char(NOW(), …) 生成同格式文本。请保证数据库会话时区为业务本地时区
--    （如 postgresql://…?TimeZone=Asia/Shanghai），否则默认时间会按库端时区写入。

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'user'   CHECK (role   IN ('user', 'admin')),
    totp_secret   TEXT,                          -- TOTP 两步验证密钥（Base32）；空 = 未设置
    totp_enabled  INT    NOT NULL DEFAULT 0,     -- 验证器确认后置 1
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'banned')),
    created_at    TEXT NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS'),
    updated_at    TEXT NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS')
);
CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);

CREATE TABLE IF NOT EXISTS phone_numbers (
    id                    BIGSERIAL PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    phone_number          TEXT   NOT NULL,
    country_code          TEXT   NOT NULL,
    country_name          TEXT   NOT NULL,
    carrier               TEXT,
    expiry_date           TEXT   NOT NULL,               -- 到期日 YYYY-MM-DD
    recharge_amount       DOUBLE PRECISION,
    recharge_currency     TEXT   NOT NULL DEFAULT 'USD',
    renewal_days_before   INT    NOT NULL DEFAULT 7,     -- 续费提醒提前天数
    usage_days_before     INT    NOT NULL DEFAULT 3,     -- 使用提醒提前天数
    auto_expiry_enabled   INT    NOT NULL DEFAULT 0,     -- 自动续期开关
    auto_start_date       TEXT,                          -- 自动续期起始日
    auto_expiry_period    INT    CHECK (auto_expiry_period IS NULL OR auto_expiry_period BETWEEN 1 AND 3650),
    auto_calculated_expiry TEXT,                         -- 最近一次自动计算出的到期日
    no_keepalive          INT    NOT NULL DEFAULT 0,    -- 无需保号：不参与到期提醒
    status                TEXT   NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    notes                 TEXT,
    created_at            TEXT   NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS'),
    updated_at            TEXT   NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS')
);
CREATE INDEX IF NOT EXISTS idx_numbers_user   ON phone_numbers(user_id);
CREATE INDEX IF NOT EXISTS idx_numbers_expiry ON phone_numbers(expiry_date);
CREATE INDEX IF NOT EXISTS idx_numbers_status ON phone_numbers(status);

-- 每个用户一行通知渠道配置
CREATE TABLE IF NOT EXISTS notification_configs (
    id                 BIGSERIAL PRIMARY KEY,
    user_id            BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    email_enabled      INT    NOT NULL DEFAULT 0,
    email_smtp_host    TEXT,
    email_smtp_port    INT    NOT NULL DEFAULT 587,
    email_smtp_secure  TEXT   NOT NULL DEFAULT 'tls' CHECK (email_smtp_secure IN ('tls', 'ssl', 'none')),
    email_smtp_username TEXT,
    email_from_email   TEXT,
    email_from_name    TEXT,
    email_password     TEXT,
    email_to_email     TEXT,                          -- 收件地址；空则回退用户邮箱
    telegram_enabled   INT    NOT NULL DEFAULT 0,
    telegram_bot_token TEXT,
    telegram_chat_id   TEXT,
    wxpusher_enabled   INT    NOT NULL DEFAULT 0,
    wxpusher_app_token TEXT,
    wxpusher_uid       TEXT,
    feishu_enabled     INT    NOT NULL DEFAULT 0,     -- 飞书群自定义机器人
    feishu_webhook     TEXT,
    feishu_secret      TEXT,                          -- 加签密钥（机器人未开签名则留空）
    dingtalk_enabled   INT    NOT NULL DEFAULT 0,     -- 钉钉群自定义机器人
    dingtalk_webhook   TEXT,
    dingtalk_secret    TEXT,                          -- 加签密钥（关键词/IP 白名单模式则留空）
    tgcall_enabled     INT    NOT NULL DEFAULT 0,     -- TG 未接来电闹铃（MTProto 用户账号）
    tgcall_api_id      INT,                           -- my.telegram.org 的 api_id
    tgcall_api_hash    TEXT,                          -- api_hash
    tgcall_phone       TEXT,                          -- 系统 TG 账号手机号
    tgcall_session     TEXT,                          -- 登录会话（base64，由 tg-login 写入）
    tgcall_target      TEXT,                          -- 被叫账号（@用户名 或手机号）
    created_at         TEXT   NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS'),
    updated_at         TEXT   NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS')
);

CREATE TABLE IF NOT EXISTS notifications (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    phone_number_id BIGINT REFERENCES phone_numbers(id) ON DELETE SET NULL,
    type            TEXT   NOT NULL CHECK (type IN ('renewal', 'usage', 'test', 'system')),
    channel         TEXT   NOT NULL CHECK (channel IN ('email', 'telegram', 'wxpusher', 'feishu', 'dingtalk', 'tgcall', 'system')),
    subject         TEXT,
    message         TEXT   NOT NULL,
    status          TEXT   NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    retry_count     INT    NOT NULL DEFAULT 0,   -- 定时任务重投次数（上限 3）
    error_message   TEXT,
    sent_at         TEXT,
    created_at      TEXT   NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS')
);
CREATE INDEX IF NOT EXISTS idx_notif_user      ON notifications(user_id);
CREATE INDEX IF NOT EXISTS idx_notif_number    ON notifications(phone_number_id);
CREATE INDEX IF NOT EXISTS idx_notif_status    ON notifications(status);
CREATE INDEX IF NOT EXISTS idx_notif_created   ON notifications(created_at);


-- 密码重置令牌：只存 SHA-256 哈希（原文仅存在于邮件链接），1 小时有效、单次使用
CREATE TABLE IF NOT EXISTS password_resets (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT   NOT NULL,
    created_at TEXT   NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS'),
    expires_at TEXT   NOT NULL,
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
    id           BIGSERIAL PRIMARY KEY,
    ip           TEXT NOT NULL,
    username     TEXT,
    attempted_at TEXT NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS')
);
CREATE INDEX IF NOT EXISTS idx_attempts_ip_time ON login_attempts(ip, attempted_at);

-- 服务器端会话；Cookie 只保存不透明 token。
-- user_id 为 NULL 表示匿名预会话：仅承载登录/注册/初始化页面的 CSRF 令牌，
-- 登录成功后建立全新会话并销毁预会话（等价于 PHP 版的 session_regenerate_id）。
CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    user_id    BIGINT REFERENCES users(id) ON DELETE CASCADE,
    ip         TEXT,
    user_agent TEXT,
    csrf_token TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS'),
    expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_exp  ON sessions(expires_at);

-- schema 版本记录：此后每次不兼容结构变更递增版本号并配迁移代码
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INT  PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT to_char(NOW(), 'YYYY-MM-DD HH24:MI:SS')
);
INSERT INTO schema_migrations (version) VALUES (1) ON CONFLICT (version) DO NOTHING;
