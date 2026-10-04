// migrate_extra.go 为已存在的旧库补列。
// schema.sql 只建新表（IF NOT EXISTS），老库里已存在的表拿不到新增列，
// 这里按 information_schema / PRAGMA 检查缺失列并 ALTER TABLE 补齐。
package db

import (
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
	_, err := d.ensureColumns("notification_configs", notificationConfigColumns)
	return err
}
