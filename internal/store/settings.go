// Package store 集中存放数据访问层；全部使用参数化查询。
// 时间戳统一由 Go 侧生成 "YYYY-MM-DD HH:MM:SS" 文本写入（db.Touch），三方言一致。
package store

import (
	"database/sql"
	"time"

	"simkeeper/internal/db"
)

// SettingRepo 读写 system_settings 键值表。
type SettingRepo struct {
	DB *db.DB
}

// 预置系统设置；与业务逻辑的回退默认值保持一致。
var DefaultSettings = []struct {
	Key, Value, Description string
}{
	{"site_name", "SimKeeper", "站点名称"},
	{"site_description", "eSIM / 手机号保号到期提醒系统", "站点描述"},
	{"log_retention_days", "90", "通知记录保留天数"},
	{"last_cron_run", "", "定时任务最后执行时间"},
	{"total_sent", "0", "累计发送通知数"},
}

// SeedDefaults 按「存在即跳过」写入默认设置，重复执行安全。
func (r *SettingRepo) SeedDefaults() error {
	for _, s := range DefaultSettings {
		q := r.DB.Dialect.UpsertIgnore("system_settings", "setting_key",
			[]string{"setting_key", "setting_value", "description"})
		if _, err := r.DB.Exec(q, s.Key, s.Value, s.Description); err != nil {
			return err
		}
	}
	return nil
}

// Get 读取设置值；键不存在返回空串。
func (r *SettingRepo) Get(key string) (string, error) {
	var v string
	err := r.DB.QueryRow(`SELECT setting_value FROM system_settings WHERE setting_key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// GetInt 读取整型设置，解析失败或不存在时返回 fallback。
func (r *SettingRepo) GetInt(key string, fallback int) int {
	v, err := r.Get(key)
	if err != nil || v == "" {
		return fallback
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return fallback
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Set 写入设置值（键不存在则插入，存在则覆盖）。
func (r *SettingRepo) Set(key, value string) error {
	q := r.DB.Dialect.UpsertUpdate("system_settings", "setting_key",
		[]string{"setting_key", "setting_value"}, []string{"setting_value"})
	_, err := r.DB.Exec(q, key, value)
	return err
}

// LoginAttemptRepo 记录登录失败，用于 IP 维度限流。
type LoginAttemptRepo struct {
	DB *db.DB
}

// Record 记录一次失败尝试。
func (r *LoginAttemptRepo) Record(ip, username string) error {
	_, err := r.DB.Exec(
		`INSERT INTO login_attempts (ip, username) VALUES (?, ?)`, ip, username,
	)
	return err
}

// RecentCount 统计窗口期内的失败次数。
func (r *LoginAttemptRepo) RecentCount(ip string, window time.Duration) (int, error) {
	since := time.Now().Add(-window).Format(time.DateTime)
	var n int
	err := r.DB.QueryRow(
		`SELECT COUNT(*) FROM login_attempts WHERE ip = ? AND attempted_at >= ?`, ip, since,
	).Scan(&n)
	return n, err
}

// Clear 登录成功后清空该 IP 的记录。
func (r *LoginAttemptRepo) Clear(ip string) error {
	_, err := r.DB.Exec(`DELETE FROM login_attempts WHERE ip = ?`, ip)
	return err
}

// GC 清理超过 7 天的失败记录，由定时任务调用。
func (r *LoginAttemptRepo) GC() error {
	since := time.Now().Add(-7 * 24 * time.Hour).Format(time.DateTime)
	_, err := r.DB.Exec(`DELETE FROM login_attempts WHERE attempted_at < ?`, since)
	return err
}
