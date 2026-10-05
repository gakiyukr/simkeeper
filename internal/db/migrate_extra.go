// migrate_extra.go 为已存在的旧库补列。
// schema.sql 只建新表（IF NOT EXISTS），老库里已存在的表拿不到新增列，
// 这里按 information_schema / PRAGMA 检查缺失列并 ALTER TABLE 补齐。
package db

import (
	"context"
	"fmt"
	"strings"
)

// ColumnSpec 一列的三方言类型与默认值。
type ColumnSpec struct {
	Name       string
	SQLiteType string // 含 DEFAULT 子句
	MySQLType  string
	PGType     string
}

// notificationConfigColumns 通知渠道配置表的历史演进列。
// 新库由 schema 建全，本列表为空操作；旧库逐列补齐。
var notificationConfigColumns = []ColumnSpec{
	{Name: "feishu_enabled", SQLiteType: "INTEGER NOT NULL DEFAULT 0", MySQLType: "TINYINT(1) NOT NULL DEFAULT 0", PGType: "INT NOT NULL DEFAULT 0"},
	{Name: "feishu_webhook", SQLiteType: "TEXT", MySQLType: "VARCHAR(255)", PGType: "TEXT"},
	{Name: "feishu_secret", SQLiteType: "TEXT", MySQLType: "VARCHAR(255)", PGType: "TEXT"},
	{Name: "dingtalk_enabled", SQLiteType: "INTEGER NOT NULL DEFAULT 0", MySQLType: "TINYINT(1) NOT NULL DEFAULT 0", PGType: "INT NOT NULL DEFAULT 0"},
	{Name: "dingtalk_webhook", SQLiteType: "TEXT", MySQLType: "VARCHAR(255)", PGType: "TEXT"},
	{Name: "dingtalk_secret", SQLiteType: "TEXT", MySQLType: "VARCHAR(255)", PGType: "TEXT"},
	{Name: "tgcall_enabled", SQLiteType: "INTEGER NOT NULL DEFAULT 0", MySQLType: "TINYINT(1) NOT NULL DEFAULT 0", PGType: "INT NOT NULL DEFAULT 0"},
	{Name: "tgcall_api_id", SQLiteType: "INTEGER", MySQLType: "INT", PGType: "INT"},
	{Name: "tgcall_api_hash", SQLiteType: "TEXT", MySQLType: "VARCHAR(255)", PGType: "TEXT"},
	{Name: "tgcall_phone", SQLiteType: "TEXT", MySQLType: "VARCHAR(32)", PGType: "TEXT"},
	{Name: "tgcall_session", SQLiteType: "TEXT", MySQLType: "TEXT", PGType: "TEXT"},
	{Name: "tgcall_target", SQLiteType: "TEXT", MySQLType: "VARCHAR(64)", PGType: "TEXT"},
}

// phoneNumberColumns phone_numbers 表的历史演进列。
var phoneNumberColumns = []ColumnSpec{
	{Name: "no_keepalive", SQLiteType: "INTEGER NOT NULL DEFAULT 0", MySQLType: "TINYINT NOT NULL DEFAULT 0", PGType: "INT NOT NULL DEFAULT 0"},
	{Name: "plan_name", SQLiteType: "TEXT", MySQLType: "TEXT", PGType: "TEXT"},
	{Name: "secondary_numbers", SQLiteType: "TEXT", MySQLType: "TEXT", PGType: "TEXT"},
	{Name: "sim_type", SQLiteType: "TEXT NOT NULL DEFAULT 'physical'", MySQLType: "VARCHAR(16) NOT NULL DEFAULT 'physical'", PGType: "TEXT NOT NULL DEFAULT 'physical'"},
	{Name: "lpa_string", SQLiteType: "TEXT", MySQLType: "TEXT", PGType: "TEXT"},
	{Name: "confirm_code", SQLiteType: "TEXT", MySQLType: "TEXT", PGType: "TEXT"},
	{Name: "device_id", SQLiteType: "INTEGER", MySQLType: "INT UNSIGNED", PGType: "BIGINT"},
}

// notificationColumns notifications 表的历史演进列。
var notificationColumns = []ColumnSpec{
	{Name: "retry_count", SQLiteType: "INTEGER NOT NULL DEFAULT 0", MySQLType: "INT NOT NULL DEFAULT 0", PGType: "INT NOT NULL DEFAULT 0"},
}

// userColumns users 表的历史演进列。
var userColumns = []ColumnSpec{
	{Name: "totp_secret", SQLiteType: "TEXT", MySQLType: "VARCHAR(64)", PGType: "TEXT"},
	{Name: "totp_enabled", SQLiteType: "INTEGER NOT NULL DEFAULT 0", MySQLType: "TINYINT(1) NOT NULL DEFAULT 0", PGType: "INT NOT NULL DEFAULT 0"},
}

// existingColumns 列出表的现有列名。
func (d *DB) existingColumns(table string) (map[string]bool, error) {
	out := map[string]bool{}
	switch d.Dialect.Name {
	case DialectSQLite:
		rows, err := d.DB.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, ctype string
			var notNull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
				return nil, err
			}
			out[name] = true
		}
		return out, rows.Err()
	case DialectMySQL:
		rows, err := d.DB.Query(
			`SELECT COLUMN_NAME FROM information_schema.COLUMNS
			 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			out[name] = true
		}
		return out, rows.Err()
	default: // postgres
		rows, err := d.DB.Query(
			`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			out[name] = true
		}
		return out, rows.Err()
	}
}

// ensureColumns 给缺失的列执行 ALTER TABLE ADD COLUMN，返回补了多少列。
func (d *DB) ensureColumns(table string, specs []ColumnSpec) (int, error) {
	have, err := d.existingColumns(table)
	if err != nil {
		return 0, fmt.Errorf("读取表 %s 结构失败: %w", table, err)
	}
	n := 0
	for _, spec := range specs {
		if have[spec.Name] {
			continue
		}
		var typ string
		switch d.Dialect.Name {
		case DialectMySQL:
			typ = spec.MySQLType
		case DialectPostgres:
			typ = spec.PGType
		default:
			typ = spec.SQLiteType
		}
		// 列名/类型均来自代码内白名单，非用户输入
		q := "ALTER TABLE " + table + " ADD COLUMN " + spec.Name + " " + strings.TrimSpace(typ)
		if _, err := d.DB.Exec(d.Dialect.Rebind(q)); err != nil {
			return n, fmt.Errorf("补列 %s.%s 失败: %w", table, spec.Name, err)
		}
		n++
	}
	return n, nil
}

// 注意：旧库 notifications.channel 的 CHECK 约束不含新渠道值（SQLite 无法
// ALTER CHECK）。仅当你在新增渠道之前部署过本程序时才受影响——重建该表或
// 重新初始化一次数据库即可；全新安装不受影响。
func (d *DB) migrateExtra() error {
	if _, err := d.ensureColumns("users", userColumns); err != nil {
		return err
	}
	if _, err := d.ensureColumns("phone_numbers", phoneNumberColumns); err != nil {
		return err
	}
	if _, err := d.ensureColumns("notifications", notificationColumns); err != nil {
		return err
	}
	if _, err := d.ensureColumns("notification_configs", notificationConfigColumns); err != nil {
		return err
	}
	return d.relaxPeriodCheck()
}

// relaxPeriodCheck 放宽 phone_numbers.auto_expiry_period 的 CHECK 约束：
// 旧库只允许 90/180/365 天，现版本支持 1-3650 天的任意周期。
// SQLite 需整表重建（CHECK 无法 ALTER）；MySQL/PG 按名删除约束，均幂等。
func (d *DB) relaxPeriodCheck() error {
	switch d.Dialect.Name {
	case DialectSQLite:
		return d.relaxPeriodCheckSQLite()
	case DialectPostgres:
		_, err := d.DB.Exec(`ALTER TABLE phone_numbers DROP CONSTRAINT IF EXISTS phone_numbers_auto_expiry_period_check`)
		return err
	case DialectMySQL:
		var n int
		if err := d.DB.QueryRow(
			`SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
			 WHERE CONSTRAINT_SCHEMA = DATABASE() AND TABLE_NAME = 'phone_numbers'
			   AND CONSTRAINT_NAME = 'chk_numbers_period'`,
		).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		_, err := d.DB.Exec(`ALTER TABLE phone_numbers DROP CONSTRAINT chk_numbers_period`)
		return err
	}
	return nil
}

// relaxPeriodCheckSQLite 在专用连接上按标准流程重建表：
// PRAGMA 是连接级设置，必须与重建语句同连接执行。
func (d *DB) relaxPeriodCheckSQLite() error {
	var tableSQL string
	if err := d.DB.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'phone_numbers'`,
	).Scan(&tableSQL); err != nil {
		return err
	}
	if !strings.Contains(tableSQL, "IN (90, 180, 365)") {
		return nil // 已是新约束（或无约束），无需重建
	}
	ctx := context.Background()
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	exec := func(q string) error {
		_, err := conn.ExecContext(ctx, q)
		if err != nil {
			return fmt.Errorf("%s: %w", q[:min(len(q), 60)], err)
		}
		return nil
	}
	if err := exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer exec(`PRAGMA foreign_keys = ON`) // best effort 恢复

	// 新表定义与 schema_sqlite.sql 保持一致，仅 CHECK 放宽
	newDDL := `CREATE TABLE phone_numbers_new (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id               INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    phone_number          TEXT    NOT NULL,
    country_code          TEXT    NOT NULL,
    country_name          TEXT    NOT NULL,
    carrier               TEXT,
    expiry_date           TEXT    NOT NULL,
    recharge_amount       REAL,
    recharge_currency     TEXT    NOT NULL DEFAULT 'USD',
    renewal_days_before   INTEGER NOT NULL DEFAULT 7,
    usage_days_before     INTEGER NOT NULL DEFAULT 3,
    auto_expiry_enabled   INTEGER NOT NULL DEFAULT 0,
    auto_start_date       TEXT,
    auto_expiry_period    INTEGER CHECK (auto_expiry_period IS NULL OR auto_expiry_period BETWEEN 1 AND 3650),
    auto_calculated_expiry TEXT,
    no_keepalive          INTEGER NOT NULL DEFAULT 0,
    plan_name             TEXT,
    secondary_numbers     TEXT,
    status                TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
    notes                 TEXT,
    created_at            TEXT    NOT NULL DEFAULT (datetime('now', 'localtime')),
    updated_at            TEXT    NOT NULL DEFAULT (datetime('now', 'localtime'))
)`
	if err := exec(`DROP TABLE IF EXISTS phone_numbers_new`); err != nil {
		return err // 兼容上次重建中途失败留下的残留表
	}
	if err := exec(newDDL); err != nil {
		return err
	}
	// 交集列拷贝：以新表列为准 ∩ 旧表现有列（EnsureColumns 已先补齐旧库列）
	newCols := map[string]bool{}
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(phone_numbers_new)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		newCols[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// 注意：SQLite 池为单连接，专用连接被本函数持有时，池上的查询会死锁，
	// 因此旧表列信息也必须走同一专用连接。
	oldCols := map[string]bool{}
	rows, err = conn.QueryContext(ctx, `PRAGMA table_info(phone_numbers)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		oldCols[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var cols []string
	for _, name := range []string{"id", "user_id", "phone_number", "country_code", "country_name", "carrier",
		"expiry_date", "recharge_amount", "recharge_currency", "renewal_days_before", "usage_days_before",
		"auto_expiry_enabled", "auto_start_date", "auto_expiry_period", "auto_calculated_expiry",
		"status", "notes", "created_at", "updated_at", "no_keepalive",
		"plan_name", "secondary_numbers"} {
		if newCols[name] && oldCols[name] {
			cols = append(cols, name)
		}
	}
	if err := exec(`INSERT INTO phone_numbers_new (` + strings.Join(cols, ", ") + `)
	                SELECT ` + strings.Join(cols, ", ") + ` FROM phone_numbers`); err != nil {
		return err
	}
	if err := exec(`DROP TABLE phone_numbers`); err != nil {
		return err
	}
	if err := exec(`ALTER TABLE phone_numbers_new RENAME TO phone_numbers`); err != nil {
		return err
	}
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_numbers_user   ON phone_numbers(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_numbers_expiry ON phone_numbers(expiry_date)`,
		`CREATE INDEX IF NOT EXISTS idx_numbers_status ON phone_numbers(status)`,
	} {
		if err := exec(idx); err != nil {
			return err
		}
	}
	return nil
}
