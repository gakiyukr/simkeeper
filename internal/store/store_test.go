package store

import (
	"path/filepath"
	"strings"
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

func TestUserAuthenticate(t *testing.T) {
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
}

func TestNumbersDuplicateAndScope(t *testing.T) {
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

// TestFailedForRetryCarriesCreatedAt 回归：SELECT 漏选 created_at 会导致
// 重投节奏推算失败、全部记录被静默跳过（重投永不执行）。
func TestFailedForRetryCarriesCreatedAt(t *testing.T) {
	h := testDB(t)
	r := &NotifyRepo{DB: h}
	uid := mustUser(t, &UserRepo{DB: h}, "bob", "user")
	now := time.Now()

	id, err := r.Record(uid, 0, "usage", "feishu", "subject", "body")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MarkFailed(id, "HTTP 500"); err != nil {
		t.Fatal(err)
	}
	// 把创建时间回拨 2 小时，模拟「失败已满 1 小时」的重投时机
	backdated := now.Add(-2 * time.Hour).Format(time.DateTime)
	if _, err := h.Exec(`UPDATE notifications SET created_at = ? WHERE id = ?`, backdated, id); err != nil {
		t.Fatal(err)
	}

	recs, err := r.FailedForRetry(now, 24*time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("应命中 1 条待重投, got %d", len(recs))
	}
	if recs[0].CreatedAt == "" {
		t.Fatal("created_at 未被选出：重投节奏将无法推算，记录会被静默跳过")
	}
	created, err := time.ParseInLocation(time.DateTime, recs[0].CreatedAt, now.Location())
	if err != nil {
		t.Fatalf("created_at 解析失败: %v", err)
	}
	if age := now.Sub(created); age < time.Hour {
		t.Fatalf("回拨后 age=%v，应 ≥ 1h 才会被重投", age)
	}
}

// TestNumberMarkRenewed 标记已续费：到期日与起始日重置、归属校验生效。
func TestNumberMarkRenewed(t *testing.T) {
	h := testDB(t)
	r := &NumberRepo{DB: h}
	uid := mustUser(t, &UserRepo{DB: h}, "renewer", "user")
	numID, err := r.Create(&PhoneNumber{
		UserID: uid, PhoneNumber: "+85267000000", CountryCode: "HK", CountryName: "香港",
		ExpiryDate: "2026-12-31", AutoExpiryEnabled: true, AutoStartDate: "2026-01-01",
		AutoExpiryPeriod: 90, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := r.MarkRenewed(numID, uid, "2027-03-31", "2026-12-31"); err != nil || n != 1 {
		t.Fatalf("MarkRenewed 应命中 1 行, got %d, err %v", n, err)
	}
	got, err := r.ByID(numID, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiryDate != "2027-03-31" || got.AutoStartDate != "2026-12-31" || got.AutoCalculatedExpiry != "2027-03-31" {
		t.Fatalf("到期日/起始日/计算到期日未同步更新: %+v", got)
	}
	// 他人调用不得命中（归属校验）
	if n, err := r.MarkRenewed(numID, uid+999, "2027-01-01", "2026-10-04"); err != nil || n != 0 {
		t.Fatalf("非归属用户应 0 行命中, got %d, err %v", n, err)
	}
}

// TestListExpiringCarriesPlanAndSecondaries 回归：ListExpiring 的 SELECT 列表
// 必须与 scanNumber 的列集一致——漏列/错位会让定时任务整轮读取号码失败
// （sql: expected N destination arguments），到期提醒静默瘫痪。
func TestListExpiringCarriesPlanAndSecondaries(t *testing.T) {
	h := testDB(t)
	r := &NumberRepo{DB: h}
	uid := mustUser(t, &UserRepo{DB: h}, "planner", "user")
	id, err := r.Create(&PhoneNumber{
		UserID: uid, PhoneNumber: "+85269000000", CountryCode: "HK", CountryName: "香港",
		ExpiryDate: "2099-01-01", Status: "active",
		PlanName:         "30 天不限流量",
		SecondaryNumbers: "+85269000001\n+85269000002",
	})
	if err != nil {
		t.Fatal(err)
	}
	nums, err := r.ListExpiring()
	if err != nil {
		t.Fatalf("ListExpiring 查询失败（多为 SELECT 列与 scanNumber 不一致）: %v", err)
	}
	var got *PhoneNumber
	for i := range nums {
		if nums[i].ID == id {
			got = &nums[i]
		}
	}
	if got == nil {
		t.Fatalf("ListExpiring 应包含号码 %d, got %+v", id, nums)
	}
	if got.PlanName != "30 天不限流量" {
		t.Errorf("plan_name 未选出或列序错位: %q", got.PlanName)
	}
	if got.SecondaryNumbers != "+85269000001\n+85269000002" {
		t.Errorf("secondary_numbers 未选出或列序错位: %q", got.SecondaryNumbers)
	}
}

// TestNoKeepaliveRoundTripAndExclusion 无需保号：字段往返；到期提醒与周期滚动均排除。
func TestNoKeepaliveRoundTripAndExclusion(t *testing.T) {
	h := testDB(t)
	r := &NumberRepo{DB: h}
	uid := mustUser(t, &UserRepo{DB: h}, "nk", "user")
	past := time.Now().AddDate(0, 0, -5).Format("2006-01-02")

	nkID, err := r.Create(&PhoneNumber{
		UserID: uid, PhoneNumber: "+85268000000", CountryCode: "HK", CountryName: "香港",
		ExpiryDate: past, Status: "active", NoKeepalive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ByID(nkID, uid)
	if err != nil || !got.NoKeepalive {
		t.Fatalf("no_keepalive 应往返保留, got %+v err %v", got, err)
	}

	normalID, err := r.Create(&PhoneNumber{
		UserID: uid, PhoneNumber: "+85268000001", CountryCode: "HK", CountryName: "香港",
		ExpiryDate: past, Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 到期提醒：只包含普通号码，无需保号被排除
	expiring, err := r.ListExpiring()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, n := range expiring {
		ids[n.ID] = true
	}
	if ids[nkID] || !ids[normalID] {
		t.Fatalf("ListExpiring 应排除无需保号: %+v", ids)
	}
}

// TestNumberSimTypeAndESIMSecrets 号码类型与 eSIM 激活信息：
// 密文落库、读回解密、类型归一化、切回实体卡清空、ListExpiring 列集一致。
func TestNumberSimTypeAndESIMSecrets(t *testing.T) {
	h := testDB(t)
	r := &NumberRepo{DB: h}
	uid := mustUser(t, &UserRepo{DB: h}, "esimer", "user")
	id, err := r.Create(&PhoneNumber{
		UserID: uid, PhoneNumber: "+819012345678", CountryCode: "JP", CountryName: "日本",
		ExpiryDate: "2099-01-01", Status: "active", SimType: "esim",
		LPAString: "LPA:1$rsp.example.com$ABCD-001", ConfirmCode: "654321",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 库里必须是密文，不能出现 LPA 明文片段
	var rawLPA, rawConfirm string
	if err := h.QueryRow(`SELECT lpa_string, confirm_code FROM phone_numbers WHERE id = ?`, id).
		Scan(&rawLPA, &rawConfirm); err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(rawLPA) || strings.Contains(rawLPA, "rsp.example.com") {
		t.Errorf("LPA 应以密文落库, got %q", rawLPA)
	}
	if !secret.IsEncrypted(rawConfirm) {
		t.Errorf("确认码应以密文落库, got %q", rawConfirm)
	}

	// 读回解密还原
	got, err := r.ByID(id, uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.SimType != "esim" || got.LPAString != "LPA:1$rsp.example.com$ABCD-001" || got.ConfirmCode != "654321" {
		t.Errorf("eSIM 信息往返不一致: %+v", got)
	}

	// SimType 空值归一化为 physical，且无激活信息
	phyID, err := r.Create(&PhoneNumber{
		UserID: uid, PhoneNumber: "+819012345679", CountryCode: "JP", CountryName: "日本",
		ExpiryDate: "2099-01-01", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	phy, _ := r.ByID(phyID, uid)
	if phy.SimType != "physical" || phy.LPAString != "" {
		t.Errorf("默认应为 physical 且无 LPA: %+v", phy)
	}

	// 切回实体卡：类型改写、激活信息清空
	got.SimType = "physical"
	got.LPAString, got.ConfirmCode = "", ""
	if err := r.Update(got); err != nil {
		t.Fatal(err)
	}
	var cnt int
	if err := h.QueryRow(`SELECT COUNT(*) FROM phone_numbers WHERE id = ? AND lpa_string IS NULL`, id).Scan(&cnt); err != nil || cnt != 1 {
		t.Errorf("切回实体卡后 LPA 应清空, got %d/%v", cnt, err)
	}

	// ListExpiring 的列集必须与 scanNumber 一致（含新列）
	if _, err := r.ListExpiring(); err != nil {
		t.Fatalf("ListExpiring 查询失败: %v", err)
	}
}

// TestDevicesCRUDAndDetach 设备管理：增改查、归属隔离、删除时名下号码解绑。
func TestDevicesCRUDAndDetach(t *testing.T) {
	h := testDB(t)
	dr := &DeviceRepo{DB: h}
	nr := &NumberRepo{DB: h}
	uid := mustUser(t, &UserRepo{DB: h}, "dev", "user")

	id, err := dr.Create(&Device{UserID: uid, Name: "iPhone 15", DeviceType: "phone", Notes: "主力机"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dr.ByID(id, uid)
	if err != nil || got.Name != "iPhone 15" || got.DeviceType != "phone" {
		t.Fatalf("设备读取不一致: %+v %v", got, err)
	}
	if _, err := dr.ByID(id, uid+999); err != ErrNotFound {
		t.Errorf("非归属用户应 ErrNotFound, got %v", err)
	}

	numID, err := nr.Create(&PhoneNumber{
		UserID: uid, PhoneNumber: "+819012345678", CountryCode: "JP", CountryName: "日本",
		ExpiryDate: "2099-01-01", Status: "active", DeviceID: id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := nr.ByID(numID, uid); n.DeviceID != id {
		t.Errorf("device_id 应往返保留, got %d", n.DeviceID)
	}

	got.Name, got.DeviceType = "iPhone 15 Pro", "tablet"
	if err := dr.Update(got); err != nil {
		t.Fatal(err)
	}
	if g2, _ := dr.ByID(id, uid); g2.Name != "iPhone 15 Pro" || g2.DeviceType != "tablet" {
		t.Errorf("更新未生效: %+v", g2)
	}

	if err := dr.Delete(id, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := dr.ByID(id, uid); err != ErrNotFound {
		t.Errorf("删除后应 ErrNotFound, got %v", err)
	}
	if n, _ := nr.ByID(numID, uid); n.DeviceID != 0 {
		t.Errorf("删除设备后号码应解绑, got %d", n.DeviceID)
	}

	d2, _ := dr.Create(&Device{UserID: uid, Name: "随身 WiFi", DeviceType: "modem"})
	devs, err := dr.ListForUser(uid)
	if err != nil || len(devs) != 1 || devs[0].ID != d2 {
		t.Errorf("列表应只剩 1 台, got %+v %v", devs, err)
	}
}
