package db

import (
	"path/filepath"
	"testing"
)

// 回归：旧库的 auto_expiry_period 只有 90/180/365 三个取值时，启动会重建
// phone_numbers。此前重建 DDL 与列拷贝清单是写死的，跳版升级时会把后来
// 新增的 device_id / sim_type / lpa_string / confirm_code 吞掉——第一次启动
// 所有号码查询报 no such column，第二次启动补列时 eSIM 资料与设备绑定已丢失。
// 本测试确保重建后的列集合与全新库一致，且数据原样保留。

const legacyPhoneNumbersAllColumnsDDL = `
CREATE TABLE phone_numbers (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id                INTEGER NOT NULL,
    phone_number           TEXT    NOT NULL,
    country_code           TEXT    NOT NULL,
    country_name           TEXT    NOT NULL,
    carrier                TEXT,
    expiry_date            TEXT    NOT NULL,
    recharge_amount        REAL,
    recharge_currency      TEXT    NOT NULL DEFAULT 'USD',
    renewal_days_before    INTEGER NOT NULL DEFAULT 7,
    reminder_interval_days INTEGER NOT NULL DEFAULT 1,
    auto_expiry_enabled    INTEGER NOT NULL DEFAULT 0,
    auto_start_date        TEXT,
    auto_expiry_period     INTEGER CHECK (auto_expiry_period IS NULL OR auto_expiry_period IN (90, 180, 365)),
    auto_calculated_expiry TEXT,
    no_keepalive           INTEGER NOT NULL DEFAULT 0,
    plan_name              TEXT,
    secondary_numbers      TEXT,
    device_id              INTEGER,
    sim_type               TEXT    NOT NULL DEFAULT 'physical',
    lpa_string             TEXT,
    confirm_code           TEXT,
    status                 TEXT    NOT NULL DEFAULT 'active',
    notes                  TEXT,
    created_at             TEXT    NOT NULL DEFAULT (datetime('now', 'localtime')),
    updated_at             TEXT    NOT NULL DEFAULT (datetime('now', 'localtime'))
)`

const legacyPhoneNumbersMinimalDDL = `
CREATE TABLE phone_numbers (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id                INTEGER NOT NULL,
    phone_number           TEXT    NOT NULL,
    country_code           TEXT    NOT NULL,
    country_name           TEXT    NOT NULL,
    carrier                TEXT,
    expiry_date            TEXT    NOT NULL,
    renewal_days_before    INTEGER NOT NULL DEFAULT 7,
    auto_expiry_enabled    INTEGER NOT NULL DEFAULT 0,
    auto_start_date        TEXT,
    auto_expiry_period     INTEGER CHECK (auto_expiry_period IS NULL OR auto_expiry_period IN (90, 180, 365)),
    auto_calculated_expiry TEXT,
    status                 TEXT    NOT NULL DEFAULT 'active',
    notes                  TEXT,
    created_at             TEXT    NOT NULL DEFAULT (datetime('now', 'localtime')),
    updated_at             TEXT    NOT NULL DEFAULT (datetime('now', 'localtime'))
)`

// currentPhoneNumberColumns 取全新库的列集合，作为重建结果的基准。
func currentPhoneNumberColumns(t *testing.T) map[string]bool {
	t.Helper()
	h, err := Open(DialectSQLite, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if err := h.Migrate(); err != nil {
		t.Fatalf("全新库迁移失败: %v", err)
	}
	cols, err := h.existingColumns("phone_numbers")
	if err != nil {
		t.Fatal(err)
	}
	return cols
}

func openLegacyAfterMigrate(t *testing.T, ddl, seed string) *DB {
	t.Helper()
	h, err := Open(DialectSQLite, filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if _, err := h.Exec(ddl); err != nil {
		t.Fatalf("创建旧表失败: %v", err)
	}
	if seed != "" {
		if _, err := h.Exec(seed); err != nil {
			t.Fatalf("写入旧数据失败: %v", err)
		}
	}
	if err := h.Migrate(); err != nil {
		t.Fatalf("旧库迁移失败: %v", err)
	}
	return h
}

func assertSamePhoneNumberColumns(t *testing.T, h *DB) {
	t.Helper()
	want := currentPhoneNumberColumns(t)
	got, err := h.existingColumns("phone_numbers")
	if err != nil {
		t.Fatal(err)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("重建后缺少列 %q：跳版升级会丢数据或使号码查询直接报错", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("重建后出现 schema 之外的列 %q", name)
		}
	}
}

func TestSQLitePeriodCheckRebuildPreservesColumns(t *testing.T) {
	t.Run("保留 eSIM 与设备数据", func(t *testing.T) {
		h := openLegacyAfterMigrate(t, legacyPhoneNumbersAllColumnsDDL, `
			INSERT INTO phone_numbers
				(user_id, phone_number, country_code, country_name, expiry_date,
				 renewal_days_before, reminder_interval_days,
				 device_id, sim_type, lpa_string, confirm_code, status, plan_name)
			VALUES (1, '+819012345678', 'JP', '日本', '2099-01-01',
				30, 3, 7, 'esim', 'LPA:1$rsp.example.com$ABC-001', '987654', 'active', '测试套餐')`)

		assertSamePhoneNumberColumns(t, h)

		var id, deviceID int64
		var simType, lpa, confirm, plan string
		var renewalDays, interval int
		if err := h.QueryRow(`SELECT id, sim_type, COALESCE(lpa_string,''), COALESCE(confirm_code,''),
				COALESCE(device_id,0), COALESCE(plan_name,''), renewal_days_before, reminder_interval_days
			FROM phone_numbers`).Scan(&id, &simType, &lpa, &confirm, &deviceID, &plan, &renewalDays, &interval); err != nil {
			t.Fatalf("重建后读取号码失败: %v", err)
		}
		if id != 1 || simType != "esim" || lpa != "LPA:1$rsp.example.com$ABC-001" ||
			confirm != "987654" || deviceID != 7 || plan != "测试套餐" ||
			renewalDays != 30 || interval != 3 {
			t.Fatalf("重建后数据不一致: id=%d sim=%q lpa=%q confirm=%q device=%d plan=%q window=%d interval=%d",
				id, simType, lpa, confirm, deviceID, plan, renewalDays, interval)
		}
	})

	t.Run("补齐旧库缺失的新列", func(t *testing.T) {
		h := openLegacyAfterMigrate(t, legacyPhoneNumbersMinimalDDL, `
			INSERT INTO phone_numbers
				(user_id, phone_number, country_code, country_name, expiry_date, carrier, status)
			VALUES (1, '+85291234567', 'HK', '香港', '2099-01-01', 'CSL', 'active')`)

		assertSamePhoneNumberColumns(t, h)

		var phone, carrier, simType string
		var lpa, confirm, plan string
		var deviceID int64
		var interval int
		if err := h.QueryRow(`SELECT phone_number, COALESCE(carrier,''), sim_type,
				COALESCE(lpa_string,''), COALESCE(confirm_code,''), COALESCE(plan_name,''),
				COALESCE(device_id,0), reminder_interval_days
			FROM phone_numbers`).Scan(&phone, &carrier, &simType, &lpa, &confirm, &plan, &deviceID, &interval); err != nil {
			t.Fatalf("重建后读取号码失败: %v", err)
		}
		if phone != "+85291234567" || carrier != "CSL" || simType != "physical" ||
			lpa != "" || confirm != "" || plan != "" || deviceID != 0 || interval != 1 {
			t.Fatalf("缺失列补齐或旧数据保留不正确: phone=%q carrier=%q sim=%q lpa=%q confirm=%q plan=%q device=%d interval=%d",
				phone, carrier, simType, lpa, confirm, plan, deviceID, interval)
		}
	})
}

// 重建中途失败（例如旧数据违反新 CHECK）时必须回滚，旧表与数据保持可用，
// 不能留下 phone_numbers_new 残表或半成品表。
func TestSQLitePeriodCheckRebuildRollsBackOnFailure(t *testing.T) {
	h, err := Open(DialectSQLite, filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if _, err := h.Exec(legacyPhoneNumbersAllColumnsDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`INSERT INTO phone_numbers
		(user_id, phone_number, country_code, country_name, expiry_date, sim_type)
		VALUES (1, '+819012345678', 'JP', '日本', '2099-01-01', 'legacy-value')`); err != nil {
		t.Fatal(err)
	}

	if err := h.Migrate(); err == nil {
		t.Fatal("旧数据违反新 CHECK，迁移应失败并回滚")
	}

	var n int
	if err := h.QueryRow(`SELECT COUNT(*) FROM phone_numbers WHERE sim_type = 'legacy-value'`).Scan(&n); err != nil {
		t.Fatalf("回滚后旧表应仍可用: %v", err)
	}
	if n != 1 {
		t.Fatalf("回滚后旧数据应保留, got %d 行", n)
	}
	var leftovers int
	if err := h.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='phone_numbers_new'`).Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Fatalf("回滚后不应留下 phone_numbers_new 残表, got %d", leftovers)
	}
}
