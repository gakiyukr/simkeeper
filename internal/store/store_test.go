package store

import (
	"path/filepath"
	"testing"
	"time"

	"simkeeper/internal/auth"
	"simkeeper/internal/db"
	"simkeeper/internal/secret"
)

// 数据层测试：临时文件型 SQLite（与生产 DSN 参数一致），覆盖
// 配额/查重/归属隔离/加密往返/重投查询等此前只靠手工冒烟的路径。

func testDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	handle, err := db.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := handle.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := (&SettingRepo{DB: handle}).SeedDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := secret.Init(filepath.Join(dir, "secret.key")); err != nil {
		t.Fatal(err)
	}
	return handle
}

func mustUser(t *testing.T, r *UserRepo, name, role string) int64 {
	t.Helper()
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Create(name, name+"@test.local", hash, role)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUserAuthenticateAndStatus(t *testing.T) {
	h := testDB(t)
	r := &UserRepo{DB: h}
	id := mustUser(t, r, "alice", "admin")

	if u, ok, err := r.Authenticate("alice", "password123"); err != nil || !ok || u.ID != id {
		t.Fatalf("正确密码应通过: %v %v", u, err)
	}
	if _, ok, _ := r.Authenticate("alice", "wrong-password"); ok {
		t.Error("错误密码不应通过")
	}
	if _, ok, _ := r.Authenticate("nobody", "password123"); ok {
		t.Error("不存在的用户不应通过")
	}
	if err := r.SetStatus(id, "banned"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r.Authenticate("alice", "password123"); ok {
		t.Error("被封禁后不应通过")
	}
	if n, _ := r.CountAdmins(); n != 0 {
		t.Errorf("被封禁的管理员不应计入, got %d", n)
	}
	if err := r.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ByID(id); err != ErrNotFound {
		t.Errorf("删除后应返回 ErrNotFound, got %v", err)
	}
}

func TestUserListSearch(t *testing.T) {
	h := testDB(t)
	r := &UserRepo{DB: h}
	mustUser(t, r, "alpha", "user")
	mustUser(t, r, "beta", "user")
	users, total, err := r.List(1, 20, "alp")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(users) != 1 || users[0].Username != "alpha" {
		t.Errorf("搜索 alpha 应命中 1 条, got %d/%v", total, users)
	}
}

func TestNumbersQuotaDuplicateAndScope(t *testing.T) {
	h := testDB(t)
	nr := &NumberRepo{DB: h}
	ur := &UserRepo{DB: h}
	uid := mustUser(t, ur, "owner", "user")
	other := mustUser(t, ur, "intruder", "user")

	n := &PhoneNumber{
		UserID: uid, PhoneNumber: "+85291234567", CountryCode: "HK", CountryName: "香港",
		ExpiryDate: "2099-01-01", Status: "active",
		RenewalDaysBefore: 7, UsageDaysBefore: 3,
	}
	id, err := nr.Create(n)
	if err != nil {
		t.Fatal(err)
	}
	n.ID = id // Create 返回 ID，Update 需要

	// 归属隔离：他人查不到
	if _, err := nr.ByID(id, other); err != ErrNotFound {
		t.Errorf("他人访问应 ErrNotFound, got %v", err)
	}
	if _, err := nr.ByID(id, uid); err != nil {
		t.Errorf("本人访问应成功: %v", err)
	}

	// 重复号码：active 计 1
	if cnt, _ := nr.CountDuplicate(uid, "+85291234567", 0); cnt != 1 {
		t.Errorf("同号应计 1, got %d", cnt)
	}
	// 排除自身后为 0（编辑场景）
	if cnt, _ := nr.CountDuplicate(uid, "+85291234567", id); cnt != 0 {
		t.Errorf("排除自身后应计 0, got %d", cnt)
	}

	// 配额只计 active：停用后计数为 0
	if cnt, _ := nr.CountForUser(uid); cnt != 1 {
		t.Errorf("active 配额应计 1, got %d", cnt)
	}
	n.Status = "inactive"
	if err := nr.Update(n); err != nil {
		t.Fatal(err)
	}
	if cnt, _ := nr.CountForUser(uid); cnt != 0 {
		t.Errorf("停用后配额应计 0, got %d", cnt)
	}
}

func TestNumberAutoExpiryRoll(t *testing.T) {
	h := testDB(t)
	nr := &NumberRepo{DB: h}
	ur := &UserRepo{DB: h}
	uid := mustUser(t, ur, "alice", "user")

	now := time.Now()
	today := now.Format("2006-01-02")
	n := &PhoneNumber{
		UserID: uid, PhoneNumber: "+4412345678", CountryCode: "GB", CountryName: "英国",
		ExpiryDate: today, Status: "active",
		AutoExpiryEnabled: true, AutoExpiryPeriod: 90,
	}
	id, err := nr.Create(n)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := nr.UpdateAutoExpiry(now)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 1 {
		t.Fatalf("应滚动 1 条, got %d", updated)
	}
	got, err := nr.ByID(id, uid)
	if err != nil {
		t.Fatal(err)
	}
	want := now.AddDate(0, 0, 90).Format("2006-01-02")
	if got.ExpiryDate != want {
		t.Errorf("到期日 = %s, want %s", got.ExpiryDate, want)
	}
	if got.AutoCalculatedExpiry != want {
		t.Errorf("auto_calculated_expiry = %s, want %s", got.AutoCalculatedExpiry, want)
	}
}

func TestNumberDisableAutoExpiry(t *testing.T) {
	h := testDB(t)
	nr := &NumberRepo{DB: h}
	ur := &UserRepo{DB: h}
	uid := mustUser(t, ur, "alice", "user")
	n := &PhoneNumber{
		UserID: uid, PhoneNumber: "+3312345678", CountryCode: "FR", CountryName: "法国",
		ExpiryDate: "2099-01-01", Status: "active",
		AutoExpiryEnabled: true, AutoExpiryPeriod: 90, AutoCalculatedExpiry: "2099-01-01",
	}
	id, err := nr.Create(n)
	if err != nil {
		t.Fatal(err)
	}
	if err := nr.DisableAutoExpiry(id, uid); err != nil {
		t.Fatal(err)
	}
	got, _ := nr.ByID(id, uid)
	if got.AutoExpiryEnabled {
		t.Error("关闭后 auto_expiry_enabled 应为 false")
	}
	if got.AutoCalculatedExpiry != "" {
		t.Error("关闭后应清空 auto_calculated_expiry")
	}
}

func TestNotifyConfigEncryptionRoundTrip(t *testing.T) {
	h := testDB(t)
	r := &NotifyRepo{DB: h}
	uid := mustUser(t, &UserRepo{DB: h}, "alice", "user")

	cfg := &NotifyConfig{
		UserID: uid, EmailEnabled: true, EmailSMTPHost: "smtp.test", EmailSMTPPort: 465,
		EmailFromEmail: "from@test", EmailPassword: "super-secret-password",
		TelegramEnabled: true, TelegramBotToken: "123:bot-token",
		TGCallAPIHash: "api-hash-value", TGCallSession: "mtproto-session-blob",
	}
	if err := r.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	// 库里不应有明文
	var raw string
	if err := h.QueryRow(
		`SELECT email_password FROM notification_configs WHERE user_id = ?`, uid,
	).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "super-secret-password" {
		t.Fatal("密码以明文落库")
	}
	if !secret.IsEncrypted(raw) {
		t.Errorf("密码应为 enc:v1: 密文, got %q", raw)
	}

	// 读回应解密还原
	got, err := r.ConfigForUser(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.EmailPassword != "super-secret-password" || got.TelegramBotToken != "123:bot-token" ||
		got.TGCallAPIHash != "api-hash-value" || got.TGCallSession != "mtproto-session-blob" {
		t.Errorf("解密往返不一致: %+v", got)
	}

	// EncryptPlainConfigs 幂等：全密文时不再改写
	n, err := r.EncryptPlainConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("全密文行不应被重写, got %d", n)
	}
}

func TestNotifyPlainConfigMigration(t *testing.T) {
	h := testDB(t)
	r := &NotifyRepo{DB: h}
	ur := &UserRepo{DB: h}
	uid := mustUser(t, ur, "legacy", "user")

	// 模拟历史明文行
	if _, err := h.Exec(
		`INSERT INTO notification_configs (user_id, email_enabled, email_smtp_host, email_password) VALUES (?, 1, 'smtp.legacy', 'legacy-plain-password')`,
		uid,
	); err != nil {
		t.Fatal(err)
	}
	n, err := r.EncryptPlainConfigs()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应迁移 1 行, got %d", n)
	}
	got, _ := r.ConfigForUser(uid)
	if got.EmailPassword != "legacy-plain-password" {
		t.Errorf("迁移后解密应还原原值, got %q", got.EmailPassword)
	}
	// 再次运行为空操作
	if n, _ := r.EncryptPlainConfigs(); n != 0 {
		t.Errorf("第二次迁移应为空操作, got %d", n)
	}
}

func TestNotificationLifecycle(t *testing.T) {
	h := testDB(t)
	r := &NotifyRepo{DB: h}
	uid := mustUser(t, &UserRepo{DB: h}, "alice", "user")
	now := time.Now()

	id, err := r.Record(uid, 0, "renewal", "telegram", "subject", "body")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MarkFailed(id, "boom"); err != nil {
		t.Fatal(err)
	}

	// 当天去重：需以号码维度查询（先建一个号码占位）
	nr := &NumberRepo{DB: h}
	numID, err := nr.Create(&PhoneNumber{
		UserID: uid, PhoneNumber: "+85266000000", CountryCode: "HK", CountryName: "香港",
		ExpiryDate: "2099-01-01", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`UPDATE notifications SET phone_number_id = ? WHERE id = ?`, numID, id); err != nil {
		t.Fatal(err)
	}
	done, err := r.HasNotificationToday(numID, "renewal", now)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("同号码同类型当天应视为已发送")
	}

	// 重投查询：失败且未超期应命中
	recs, err := r.FailedForRetry(now, 24*time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].ID != id || recs[0].RetryCount != 0 {
		t.Fatalf("应命中 1 条待重投, got %+v", recs)
	}

	// 三次重投后不再命中
	for i := 0; i < 3; i++ {
		if err := r.MarkFailedWithRetry(id, "retry "+time.Now().Format(time.DateTime)); err != nil {
			t.Fatal(err)
		}
	}
	if recs, _ := r.FailedForRetry(now, 24*time.Hour, 3); len(recs) != 0 {
		t.Errorf("达到重试上限后不应再命中, got %d", len(recs))
	}

	// 测试节流
	if _, err := r.Record(uid, 0, "test", "telegram", "", "t"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.LastTestWithin(uid, now, time.Minute); !ok {
		t.Error("1 分钟内发过测试应触发节流")
	}

	// 清理：把记录回拨 100 天，按 90 天保留期应被清掉
	if _, err := h.Exec(
		`UPDATE notifications SET created_at = datetime('now', '-100 days') WHERE id = ?`, id,
	); err != nil {
		t.Fatal(err)
	}
	if n, err := r.CleanupExpired(90); err != nil || n == 0 {
		t.Errorf("超期记录应被清理, got %d/%v", n, err)
	}
}

func TestSettingsAndLoginAttempts(t *testing.T) {
	h := testDB(t)
	s := &SettingRepo{DB: h}
	if err := s.Set("site_name", "测试站"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("site_name"); v != "测试站" {
		t.Errorf("site_name = %q", v)
	}
	if got := s.GetInt("max_numbers_per_user", 0); got != 50 {
		t.Errorf("预置默认值应为 50, got %d", got)
	}
	if got := s.GetInt("nonexistent", 7); got != 7 {
		t.Errorf("缺失键应回退, got %d", got)
	}

	a := &LoginAttemptRepo{DB: h}
	for i := 0; i < 5; i++ {
		if err := a.Record("1.2.3.4", "alice"); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := a.RecentCount("1.2.3.4", 15*time.Minute); n != 5 {
		t.Errorf("失败计数 = %d, want 5", n)
	}
	if err := a.Clear("1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if n, _ := a.RecentCount("1.2.3.4", 15*time.Minute); n != 0 {
		t.Errorf("清空后计数 = %d", n)
	}
}

func TestSchemaMigrationsIdempotent(t *testing.T) {
	h := testDB(t)
	// Migrate 重复执行不应报错（store_test 的 testDB 已跑过一次）
	if err := h.Migrate(); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}
	var n int
	if err := h.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("schema_migrations 应记录版本 1, got %d 行", n)
	}
}
