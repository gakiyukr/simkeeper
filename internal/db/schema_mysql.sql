-- SimKeeper 数据库结构（MySQL 8.0+ / MariaDB 10.5+）。
-- 与 SQLite 版的字段与约束一致，仅类型与 DDL 语法按 MySQL 方言：
--  - 时间列用 DATETIME；应用按 parseTime=false 连接，驱动以 "YYYY-MM-DD HH:MM:SS"
--    字符串返回，与 Go 侧解析约定一致。
--  - 索引写进 CREATE TABLE（MySQL 8 不支持 CREATE INDEX IF NOT EXISTS）。
--  - 行级外键必须显式声明 CONSTRAINT（列内联 REFERENCES 会被 MySQL 解析但忽略）。
--  - CHECK 约束 8.0.16 起强制执行，更早版本仅解析不执行（应用层有同等校验，无碍）。

CREATE TABLE IF NOT EXISTS users (
    id            INT UNSIGNED NOT NULL AUTO_INCREMENT,
    username      VARCHAR(50)  NOT NULL,
    email         VARCHAR(100) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role          VARCHAR(16)  NOT NULL DEFAULT 'user',
    totp_secret   VARCHAR(64)  NULL,            -- TOTP 两步验证密钥（Base32）；空 = 未设置
    totp_enabled  TINYINT(1)   NOT NULL DEFAULT 0, -- 验证器确认后置 1
    status        VARCHAR(16)  NOT NULL DEFAULT 'active',
    created_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uniq_users_username (username),
    UNIQUE KEY uniq_users_email (email),
    KEY idx_users_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS phone_numbers (
    id                    INT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id               INT UNSIGNED NOT NULL,
    phone_number          VARCHAR(20)  NOT NULL,
    country_code          VARCHAR(10)  NOT NULL,
    country_name          VARCHAR(50)  NOT NULL,
    carrier               VARCHAR(50)  NULL,
    expiry_date           DATE         NOT NULL,           -- 到期日 YYYY-MM-DD
    recharge_amount       DOUBLE       NULL,
    recharge_currency     VARCHAR(8)   NOT NULL DEFAULT 'USD',
    renewal_days_before   INT          NOT NULL DEFAULT 7, -- 续费提醒提前天数
    usage_days_before     INT          NOT NULL DEFAULT 3, -- 使用提醒提前天数
    auto_expiry_enabled   TINYINT(1)   NOT NULL DEFAULT 0, -- 自动续期开关
    auto_start_date       DATE         NULL,               -- 自动续期起始日
    auto_expiry_period    INT          NULL,
    auto_calculated_expiry DATE        NULL,               -- 最近一次自动计算出的到期日
    status                VARCHAR(16)  NOT NULL DEFAULT 'active',
    notes                 TEXT         NULL,
    created_at            DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_numbers_user (user_id),
    KEY idx_numbers_expiry (expiry_date),
    KEY idx_numbers_status (status),
    CONSTRAINT chk_numbers_period CHECK (auto_expiry_period IN (90, 180, 365)),
    CONSTRAINT chk_numbers_status CHECK (status IN ('active', 'inactive')),
    CONSTRAINT fk_numbers_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 每个用户一行通知渠道配置
CREATE TABLE IF NOT EXISTS notification_configs (
    id                 INT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id            INT UNSIGNED NOT NULL,
    email_enabled      TINYINT(1)   NOT NULL DEFAULT 0,
    email_smtp_host    VARCHAR(100) NULL,
    email_smtp_port    INT          NOT NULL DEFAULT 587,
    email_smtp_secure  VARCHAR(8)   NOT NULL DEFAULT 'tls',
    email_smtp_username VARCHAR(100) NULL,
    email_from_email   VARCHAR(100) NULL,
    email_from_name    VARCHAR(100) NULL,
    email_password     VARCHAR(255) NULL,
    email_to_email     VARCHAR(100) NULL,               -- 收件地址；空则回退用户邮箱
    telegram_enabled   TINYINT(1)   NOT NULL DEFAULT 0,
    telegram_bot_token VARCHAR(255) NULL,
    telegram_chat_id   VARCHAR(100) NULL,
    wxpusher_enabled   TINYINT(1)   NOT NULL DEFAULT 0,
    wxpusher_app_token VARCHAR(255) NULL,
    wxpusher_uid       VARCHAR(100) NULL,
    feishu_enabled     TINYINT(1)   NOT NULL DEFAULT 0, -- 飞书群自定义机器人
    feishu_webhook     VARCHAR(255) NULL,
    feishu_secret      VARCHAR(255) NULL,               -- 加签密钥（机器人未开签名则留空）
    dingtalk_enabled   TINYINT(1)   NOT NULL DEFAULT 0, -- 钉钉群自定义机器人
    dingtalk_webhook   VARCHAR(255) NULL,
    dingtalk_secret    VARCHAR(255) NULL,               -- 加签密钥（关键词/IP 白名单模式则留空）
    tgcall_enabled     TINYINT(1)   NOT NULL DEFAULT 0, -- TG 未接来电闹铃（MTProto 用户账号）
    tgcall_api_id      INT          NULL,               -- my.telegram.org 的 api_id
    tgcall_api_hash    VARCHAR(255) NULL,               -- api_hash
    tgcall_phone       VARCHAR(32)  NULL,               -- 系统 TG 账号手机号
    tgcall_session     TEXT         NULL,               -- 登录会话（base64，由 tg-login 写入）
    tgcall_target      VARCHAR(64)  NULL,               -- 被叫账号（@用户名 或手机号）
    created_at         DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uniq_configs_user (user_id),
    CONSTRAINT chk_configs_secure CHECK (email_smtp_secure IN ('tls', 'ssl', 'none')),
    CONSTRAINT fk_configs_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS notifications (
    id              INT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id         INT UNSIGNED NOT NULL,
    phone_number_id INT UNSIGNED NULL,
    type            VARCHAR(16)  NOT NULL,
    channel         VARCHAR(16)  NOT NULL,
    subject         VARCHAR(255) NULL,
    message         TEXT         NOT NULL,
    status          VARCHAR(16)  NOT NULL DEFAULT 'pending',
    retry_count     INT          NOT NULL DEFAULT 0, -- 定时任务重投次数（上限 3）
    error_message   TEXT         NULL,
    sent_at         DATETIME     NULL,
    created_at      DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_notif_user (user_id),
    KEY idx_notif_number (phone_number_id),
    KEY idx_notif_status (status),
    KEY idx_notif_created (created_at),
    CONSTRAINT chk_notif_type CHECK (type IN ('renewal', 'usage', 'test', 'system')),
    CONSTRAINT chk_notif_channel CHECK (channel IN ('email', 'telegram', 'wxpusher', 'feishu', 'dingtalk', 'tgcall', 'system')),
    CONSTRAINT chk_notif_status CHECK (status IN ('pending', 'sent', 'failed')),
    CONSTRAINT fk_notif_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT fk_notif_number FOREIGN KEY (phone_number_id) REFERENCES phone_numbers(id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;


-- 密码重置令牌：只存 SHA-256 哈希（原文仅存在于邮件链接），1 小时有效、单次使用
CREATE TABLE IF NOT EXISTS password_resets (
    id         INT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id    INT UNSIGNED NOT NULL,
    token_hash CHAR(64)     NOT NULL,
    created_at DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME     NOT NULL,
    used_at    DATETIME     NULL,
    PRIMARY KEY (id),
    KEY idx_presets_token (token_hash),
    KEY idx_presets_user (user_id),
    CONSTRAINT fk_presets_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS system_settings (
    setting_key   VARCHAR(100) NOT NULL,
    setting_value TEXT         NULL,
    description   VARCHAR(255) NULL,
    PRIMARY KEY (setting_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 登录失败记录，用于 IP 维度限流
CREATE TABLE IF NOT EXISTS login_attempts (
    id           INT UNSIGNED NOT NULL AUTO_INCREMENT,
    ip           VARCHAR(45)  NOT NULL,
    username     VARCHAR(50)  NULL,
    attempted_at DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_attempts_ip_time (ip, attempted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 服务器端会话；Cookie 只保存不透明 token。
-- user_id 为 NULL 表示匿名预会话：仅承载登录/注册/初始化页面的 CSRF 令牌，
-- 登录成功后建立全新会话并销毁预会话（等价于 PHP 版的 session_regenerate_id）。
CREATE TABLE IF NOT EXISTS sessions (
    token      CHAR(64)     NOT NULL,
    user_id    INT UNSIGNED NULL,
    ip         VARCHAR(45)  NULL,
    user_agent VARCHAR(255) NULL,
    csrf_token CHAR(64)     NOT NULL,
    created_at DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME     NOT NULL,
    PRIMARY KEY (token),
    KEY idx_sessions_user (user_id),
    KEY idx_sessions_exp (expires_at),
    CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- schema 版本记录：此后每次不兼容结构变更递增版本号并配迁移代码
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INT      NOT NULL,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
INSERT IGNORE INTO schema_migrations (version) VALUES (1);
