package store

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"simkeeper/internal/db"
	"simkeeper/internal/secret"
)

// NotifyConfig 对应 notification_configs 表，一行 = 一个用户的全部渠道配置。
type NotifyConfig struct {
	UserID            int64
	EmailEnabled      bool
	EmailSMTPHost     string
	EmailSMTPPort     int
	EmailSMTPSecure   string // tls | ssl | none
	EmailSMTPUsername string
	EmailFromEmail    string
	EmailFromName     string
	EmailPassword     string
	EmailToEmail      string // 空则回退用户注册邮箱
	TelegramEnabled   bool
	TelegramBotToken  string
	TelegramChatID    string
	WxPusherEnabled   bool
	WxPusherAppToken  string
	WxPusherUID       string
	FeishuEnabled     bool
	FeishuWebhook     string // 飞书群自定义机器人 webhook 地址
	FeishuSecret      string // 加签密钥，未开签名留空
	DingTalkEnabled   bool
	DingTalkWebhook   string
	DingTalkSecret    string // 加签密钥，关键词/IP 白名单模式留空
	TGCallEnabled     bool   // TG 未接来电闹铃（MTProto 用户账号呼叫）
	TGCallAPIID       int
	TGCallAPIHash     string
	TGCallPhone       string // 系统 TG 账号手机号
	TGCallSession     string // 登录会话（base64，由 tg-login 写入）
	TGCallTarget      string // 被叫账号（@用户名 或手机号）
}

// IsZero 用户尚未建立配置行。
func (c *NotifyConfig) IsZero() bool { return c == nil || c.UserID == 0 }

// ChannelEnabled 报告渠道是否已启用。
func (c *NotifyConfig) ChannelEnabled(channel string) bool {
	switch channel {
	case "email":
		return c.EmailEnabled
	case "telegram":
		return c.TelegramEnabled
	case "wxpusher":
		return c.WxPusherEnabled
	case "feishu":
		return c.FeishuEnabled
	case "dingtalk":
		return c.DingTalkEnabled
	case "tgcall":
		return c.TGCallEnabled
	}
	return false
}

// NotifyRepo 通知配置与通知记录数据访问。
type NotifyRepo struct {
	DB *db.DB
}

// ConfigForUser 读取用户配置；未配置返回 nil。
func (r *NotifyRepo) ConfigForUser(userID int64) (*NotifyConfig, error) {
	row := r.DB.QueryRow(`SELECT user_id,
		email_enabled, COALESCE(email_smtp_host,''), COALESCE(email_smtp_port,587),
		COALESCE(email_smtp_secure,'tls'), COALESCE(email_smtp_username,''),
		COALESCE(email_from_email,''), COALESCE(email_from_name,''),
		COALESCE(email_password,''), COALESCE(email_to_email,''),
		telegram_enabled, COALESCE(telegram_bot_token,''), COALESCE(telegram_chat_id,''),
		wxpusher_enabled, COALESCE(wxpusher_app_token,''), COALESCE(wxpusher_uid,''),
		feishu_enabled, COALESCE(feishu_webhook,''), COALESCE(feishu_secret,''),
		dingtalk_enabled, COALESCE(dingtalk_webhook,''), COALESCE(dingtalk_secret,''),
		tgcall_enabled, COALESCE(tgcall_api_id,0), COALESCE(tgcall_api_hash,''),
		COALESCE(tgcall_phone,''), COALESCE(tgcall_session,''), COALESCE(tgcall_target,'')
		FROM notification_configs WHERE user_id = ?`, userID)
	var c NotifyConfig
	var emailPort sql.NullInt64
	var emailSecure sql.NullString
	var tgAPIID sql.NullInt64
	err := row.Scan(&c.UserID,
		&c.EmailEnabled, &c.EmailSMTPHost, &emailPort,
		&emailSecure, &c.EmailSMTPUsername,
		&c.EmailFromEmail, &c.EmailFromName,
		&c.EmailPassword, &c.EmailToEmail,
		&c.TelegramEnabled, &c.TelegramBotToken, &c.TelegramChatID,
		&c.WxPusherEnabled, &c.WxPusherAppToken, &c.WxPusherUID,
		&c.FeishuEnabled, &c.FeishuWebhook, &c.FeishuSecret,
		&c.DingTalkEnabled, &c.DingTalkWebhook, &c.DingTalkSecret,
		&c.TGCallEnabled, &tgAPIID, &c.TGCallAPIHash,
		&c.TGCallPhone, &c.TGCallSession, &c.TGCallTarget,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if tgAPIID.Valid {
		c.TGCallAPIID = int(tgAPIID.Int64)
	}
	c.EmailSMTPPort = int(emailPort.Int64)
	if !emailPort.Valid || c.EmailSMTPPort == 0 {
		c.EmailSMTPPort = 587
	}
	c.EmailSMTPSecure = emailSecure.String
	if c.EmailSMTPSecure == "" {
		c.EmailSMTPSecure = "tls"
	}
	c.decryptSensitive()
	return &c, nil
}

// sensitiveValues 返回需要加密存储的字段值（顺序与 setSensitive 一致）。
func sensitiveValues(c *NotifyConfig) []string {
	return []string{
		c.EmailPassword, c.TelegramBotToken, c.WxPusherAppToken,
		c.FeishuWebhook, c.FeishuSecret, c.DingTalkWebhook, c.DingTalkSecret,
		c.TGCallAPIHash, c.TGCallSession,
	}
}

func (c *NotifyConfig) setSensitive(i int, v string) {
	switch i {
	case 0:
		c.EmailPassword = v
	case 1:
		c.TelegramBotToken = v
	case 2:
		c.WxPusherAppToken = v
	case 3:
		c.FeishuWebhook = v
	case 4:
		c.FeishuSecret = v
	case 5:
		c.DingTalkWebhook = v
	case 6:
		c.DingTalkSecret = v
	case 7:
		c.TGCallAPIHash = v
	case 8:
		c.TGCallSession = v
	}
}

// encryptSensitive 就地加密敏感字段；任一失败都返回错误（不降级明文）。
func encryptSensitive(c *NotifyConfig) error {
	for i, v := range sensitiveValues(c) {
		out, err := secret.Encrypt(v)
		if err != nil {
			return err
		}
		c.setSensitive(i, out)
	}
	return nil
}

// decryptSensitive 就地解密敏感字段。无前缀的历史明文原样放行；
// 解密失败（密钥更换等）时字段置空——渠道会失效，重新保存配置即可修复。
func (c *NotifyConfig) decryptSensitive() {
	for i, v := range sensitiveValues(c) {
		out, err := secret.Decrypt(v)
		if err != nil {
			log.Printf("[store] 解密凭据字段 %d 失败（已置空，重新保存渠道配置即可修复）: %v", i, err)
			c.setSensitive(i, "")
			continue
		}
		c.setSensitive(i, out)
	}
}

// SaveConfig 插入或更新用户的通知配置（UPSERT，方言由 Dialect 生成）。
// 敏感字段先加密再落库；updated_at 一并放进插入列，冲突更新时取同一新值。
func (r *NotifyRepo) SaveConfig(c *NotifyConfig) error {
	stored := *c
	if err := encryptSensitive(&stored); err != nil {
		return fmt.Errorf("加密凭据失败: %w", err)
	}
	cols := []string{
		"user_id", "email_enabled", "email_smtp_host", "email_smtp_port", "email_smtp_secure",
		"email_smtp_username", "email_from_email", "email_from_name", "email_password", "email_to_email",
		"telegram_enabled", "telegram_bot_token", "telegram_chat_id",
		"wxpusher_enabled", "wxpusher_app_token", "wxpusher_uid",
		"feishu_enabled", "feishu_webhook", "feishu_secret",
		"dingtalk_enabled", "dingtalk_webhook", "dingtalk_secret",
		"tgcall_enabled", "tgcall_api_id", "tgcall_api_hash", "tgcall_phone", "tgcall_session", "tgcall_target",
		"updated_at",
	}
	q := r.DB.Dialect.UpsertUpdate("notification_configs", "user_id", cols, cols[1:])
	_, err := r.DB.Exec(q,
		stored.UserID, boolInt(stored.EmailEnabled), nullStr(stored.EmailSMTPHost), stored.EmailSMTPPort, stored.EmailSMTPSecure,
		nullStr(stored.EmailSMTPUsername), nullStr(stored.EmailFromEmail), nullStr(stored.EmailFromName),
		nullStr(stored.EmailPassword), nullStr(stored.EmailToEmail),
		boolInt(stored.TelegramEnabled), nullStr(stored.TelegramBotToken), nullStr(stored.TelegramChatID),
		boolInt(stored.WxPusherEnabled), nullStr(stored.WxPusherAppToken), nullStr(stored.WxPusherUID),
		boolInt(stored.FeishuEnabled), nullStr(stored.FeishuWebhook), nullStr(stored.FeishuSecret),
		boolInt(stored.DingTalkEnabled), nullStr(stored.DingTalkWebhook), nullStr(stored.DingTalkSecret),
		boolInt(stored.TGCallEnabled), nullInt(stored.TGCallAPIID), nullStr(stored.TGCallAPIHash),
		nullStr(stored.TGCallPhone), nullStr(stored.TGCallSession), nullStr(stored.TGCallTarget),
		db.Touch(time.Now()),
	)
	return err
}

// EncryptPlainConfigs 把历史明文凭据一次性转为密文（启动时调用，幂等）。
// 解密层对无前缀值原样放行，读出后原样重存即完成加密；已全为密文的行跳过。
// 返回改写的行数。
func (r *NotifyRepo) EncryptPlainConfigs() (int, error) {
	rows, err := r.DB.Query(`SELECT user_id FROM notification_configs`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, uid := range ids {
		cfg, err := r.ConfigForUser(uid)
		if err != nil {
			return n, err
		}
		if cfg == nil || !hasPlain(cfg) {
			continue
		}
		if err := r.SaveConfig(cfg); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func hasPlain(c *NotifyConfig) bool {
	for _, v := range sensitiveValues(c) {
		if v != "" && !secret.IsEncrypted(v) {
			return true
		}
	}
	return false
}

// Notification 对应 notifications 表。
type Notification struct {
	ID            int64
	UserID        int64
	PhoneNumberID sql.NullInt64
	Type          string // renewal | usage | test | system
	Channel       string // email | telegram | wxpusher | system
	Subject       string
	Message       string
	Status        string // pending | sent | failed
	ErrorMessage  string
	SentAt        string
	CreatedAt     string
	RetryCount    int // 定时任务重投次数
	// 联表展示字段（仅 List 时填充）
	Username    string
	PhoneNumber string
}

// SaveTGCallSession 回存 MTProto 会话（tg-login 登录或会话轮换后调用）。
// 会话等同账号完全访问权，与其它凭据一样加密落库。
func (r *NotifyRepo) SaveTGCallSession(userID int64, session string) error {
	enc, err := secret.Encrypt(session)
	if err != nil {
		return err
	}
	_, err = r.DB.Exec(
		`UPDATE notification_configs SET tgcall_session = ?, updated_at = ? WHERE user_id = ?`,
		nullStr(enc), db.Touch(time.Now()), userID,
	)
	return err
}

// Record 插入一条 pending 通知记录，返回其 ID。
func (r *NotifyRepo) Record(userID, numberID int64, typ, channel, subject, message string) (int64, error) {
	return r.DB.InsertID(
		`INSERT INTO notifications (user_id, phone_number_id, type, channel, subject, message)
		 VALUES (?,?,?,?,?,?)`,
		userID, nullInt64(numberID), typ, channel, nullStr(subject), message,
	)
}

// MarkSent 标记发送成功。
func (r *NotifyRepo) MarkSent(id int64) error {
	_, err := r.DB.Exec(
		`UPDATE notifications SET status='sent', sent_at=? WHERE id=?`,
		db.Touch(time.Now()), id,
	)
	return err
}

// MarkFailed 标记发送失败并记录原因。
func (r *NotifyRepo) MarkFailed(id int64, reason string) error {
	_, err := r.DB.Exec(
		`UPDATE notifications SET status='failed', error_message=? WHERE id=?`,
		reason, id,
	)
	return err
}

// FailedForRetry 列出可重投的失败提醒：24 小时窗口内、类型为 renewal/usage、
// 重试次数未达上限的记录（test 类型不重试）。
func (r *NotifyRepo) FailedForRetry(now time.Time, window time.Duration, maxRetry int) ([]Notification, error) {
	cutoff := now.Add(-window).Format(time.DateTime)
	rows, err := r.DB.Query(
		`SELECT id, user_id, phone_number_id, type, channel, COALESCE(message,''), COALESCE(retry_count,0)
		 FROM notifications
		 WHERE status = 'failed' AND type IN ('renewal','usage')
		   AND retry_count < ? AND created_at >= ?`,
		maxRetry, cutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.PhoneNumberID, &n.Type, &n.Channel, &n.Message, &n.RetryCount); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkFailedWithRetry 标记重投失败：记录原因并递增重试计数。
func (r *NotifyRepo) MarkFailedWithRetry(id int64, reason string) error {
	_, err := r.DB.Exec(
		`UPDATE notifications SET status='failed', error_message=?, retry_count=COALESCE(retry_count,0)+1 WHERE id=?`,
		reason, id,
	)
	return err
}

// LastTestWithin 用户最近是否发送过测试通知（节流：默认 60 秒一次，
// 避免连点测试把 TG 电话等真实外呼渠道打爆）。
func (r *NotifyRepo) LastTestWithin(userID int64, now time.Time, d time.Duration) (bool, error) {
	var n int
	err := r.DB.QueryRow(
		"SELECT COUNT(*) FROM notifications WHERE user_id = ? AND type = 'test' AND created_at > ?",
		userID, now.Add(-d).Format(time.DateTime),
	).Scan(&n)
	return n > 0, err
}

// HasNotificationToday 当天是否已给该号码发过指定类型的通知（幂等去重，防重复轰炸）。
// 同日判定谓词方言化（见 Dialect.SameDay），参数传 "2006-01-02"。
func (r *NotifyRepo) HasNotificationToday(numberID int64, typ string, now time.Time) (bool, error) {
	var n int
	err := r.DB.QueryRow(
		`SELECT COUNT(*) FROM notifications
		 WHERE phone_number_id = ? AND type = ? AND `+r.DB.Dialect.SameDay("created_at"),
		numberID, typ, now.Format("2006-01-02"),
	).Scan(&n)
	return n > 0, err
}

// ListForUser 用户的通知历史。
func (r *NotifyRepo) ListForUser(userID int64, page, limit int) ([]Notification, int, error) {
	return r.list(` WHERE n.user_id = ?`, []any{userID}, page, limit)
}

// ListForAdmin 管理端通知记录，可按内容/用户名/号码模糊搜索。
func (r *NotifyRepo) ListForAdmin(page, limit int, search string) ([]Notification, int, error) {
	if search != "" {
		like := `%` + search + `%`
		return r.list(
			` WHERE LOWER(n.message) LIKE LOWER(?) OR LOWER(u.username) LIKE LOWER(?) OR LOWER(pn.phone_number) LIKE LOWER(?)`,
			[]any{like, like, like}, page, limit,
		)
	}
	return r.list(``, nil, page, limit)
}

func (r *NotifyRepo) list(where string, args []any, page, limit int) ([]Notification, int, error) {
	var total int
	if err := r.DB.QueryRow(
		`SELECT COUNT(*) FROM notifications n
		 LEFT JOIN users u ON u.id = n.user_id
		 LEFT JOIN phone_numbers pn ON pn.id = n.phone_number_id`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	q := `SELECT n.id, n.user_id, n.phone_number_id, n.type, n.channel, COALESCE(n.subject,''),
		n.message, n.status, COALESCE(n.error_message,''), COALESCE(n.sent_at,''), n.created_at,
		COALESCE(u.username,''), COALESCE(pn.phone_number,'')
		FROM notifications n
		LEFT JOIN users u ON u.id = n.user_id
		LEFT JOIN phone_numbers pn ON pn.id = n.phone_number_id` +
		where + ` ORDER BY n.id DESC LIMIT ? OFFSET ?`
	rows, err := r.DB.Query(q, append(args, limit, (page-1)*limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.UserID, &n.PhoneNumberID, &n.Type, &n.Channel, &n.Subject,
			&n.Message, &n.Status, &n.ErrorMessage, &n.SentAt, &n.CreatedAt,
			&n.Username, &n.PhoneNumber); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, total, rows.Err()
}

// CleanupExpired 删除超过保留期的通知记录，返回删除行数。
func (r *NotifyRepo) CleanupExpired(retentionDays int) (int64, error) {
	if retentionDays < 1 {
		retentionDays = 90
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format(time.DateTime)
	res, err := r.DB.Exec(`DELETE FROM notifications WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
