package db

import (
	"strings"
	"testing"
)

// 方言生成器是纯函数，单测覆盖每种方言的关键 SQL 形态。
// 这是 MySQL / PostgreSQL 未做真实连接实测时最重要的回归防线。

func TestRebind(t *testing.T) {
	cases := []struct {
		driver, in, want string
	}{
		{DialectSQLite, "a = ? AND b = ?", "a = ? AND b = ?"},
		{DialectMySQL, "a = ? AND b = ?", "a = ? AND b = ?"},
		{DialectPostgres, "a = ? AND b = ?", "a = $1 AND b = $2"},
		{DialectPostgres, "no placeholders here", "no placeholders here"},
		{DialectPostgres, "IN (?, ?, ?)", "IN ($1, $2, $3)"},
	}
	for _, c := range cases {
		if got := (Dialect{Name: c.driver}).Rebind(c.in); got != c.want {
			t.Errorf("Rebind(%s, %q) = %q, want %q", c.driver, c.in, got, c.want)
		}
	}
}

func TestUpsertIgnore(t *testing.T) {
	cols := []string{"setting_key", "setting_value"}
	pg := (Dialect{Name: DialectPostgres}).UpsertIgnore("system_settings", "setting_key", cols)
	if !strings.Contains(pg, "ON CONFLICT (setting_key) DO NOTHING") {
		t.Errorf("postgres UpsertIgnore 缺少 ON CONFLICT DO NOTHING: %s", pg)
	}
	if n := strings.Count(pg, "?"); n != len(cols) {
		t.Errorf("postgres UpsertIgnore 占位符数量 = %d, want %d", n, len(cols))
	}
	my := (Dialect{Name: DialectMySQL}).UpsertIgnore("system_settings", "setting_key", cols)
	if !strings.HasPrefix(my, "INSERT IGNORE INTO") {
		t.Errorf("mysql UpsertIgnore 应为 INSERT IGNORE: %s", my)
	}
	lt := (Dialect{Name: DialectSQLite}).UpsertIgnore("system_settings", "setting_key", cols)
	if !strings.HasPrefix(lt, "INSERT OR IGNORE INTO") {
		t.Errorf("sqlite UpsertIgnore 应为 INSERT OR IGNORE: %s", lt)
	}
}

func TestUpsertUpdate(t *testing.T) {
	cols := []string{"user_id", "a", "b"}
	upd := []string{"a", "b"}
	base := "INSERT INTO notification_configs (user_id, a, b) VALUES (?,?,?)"

	pg := (Dialect{Name: DialectPostgres}).UpsertUpdate("notification_configs", "user_id", cols, upd)
	wantPG := base + " ON CONFLICT (user_id) DO UPDATE SET a=excluded.a, b=excluded.b"
	if pg != wantPG {
		t.Errorf("postgres UpsertUpdate:\n got %s\nwant %s", pg, wantPG)
	}

	my := (Dialect{Name: DialectMySQL}).UpsertUpdate("notification_configs", "user_id", cols, upd)
	wantMy := base + " ON DUPLICATE KEY UPDATE a=VALUES(a), b=VALUES(b)"
	if my != wantMy {
		t.Errorf("mysql UpsertUpdate:\n got %s\nwant %s", my, wantMy)
	}

	lt := (Dialect{Name: DialectSQLite}).UpsertUpdate("notification_configs", "user_id", cols, upd)
	wantLt := base + " ON CONFLICT (user_id) DO UPDATE SET a=excluded.a, b=excluded.b"
	if lt != wantLt {
		t.Errorf("sqlite UpsertUpdate:\n got %s\nwant %s", lt, wantLt)
	}
}

func TestSameDay(t *testing.T) {
	for _, d := range []string{DialectSQLite, DialectMySQL, DialectPostgres} {
		p := (Dialect{Name: d}).SameDay("created_at")
		if n := strings.Count(p, "?"); n != 1 {
			t.Errorf("%s SameDay 应恰好含 1 个占位符: %s", d, p)
		}
		if !strings.Contains(p, "created_at") {
			t.Errorf("%s SameDay 未引用列名: %s", d, p)
		}
	}
	if got := (Dialect{Name: DialectPostgres}).SameDay("created_at"); got != "left(created_at, 10) = ?" {
		t.Errorf("postgres SameDay = %s", got)
	}
}

func TestSplitStatements(t *testing.T) {
	script := `
-- 开头注释
CREATE TABLE a (id INT PRIMARY KEY);
CREATE TABLE b (id INT); -- 行尾注释

-- 结尾只有注释
`
	stmts := splitStatements(script)
	if len(stmts) != 2 {
		t.Fatalf("应拆出 2 条语句, 实际 %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "CREATE TABLE a") || strings.Contains(stmts[0], "b (") {
		t.Errorf("第 1 条语句内容不对: %s", stmts[0])
	}
	if strings.Contains(stmts[1], ";") {
		t.Errorf("语句不应再含分号: %s", stmts[1])
	}
}

func TestDriverName(t *testing.T) {
	if (Dialect{Name: DialectPostgres}).DriverName() != "pgx" {
		t.Error("postgres 的驱动注册名应为 pgx")
	}
	if (Dialect{Name: DialectMySQL}).DriverName() != "mysql" {
		t.Error("mysql 的驱动注册名应为 mysql")
	}
	if (Dialect{Name: DialectSQLite}).DriverName() != "sqlite" {
		t.Error("sqlite 的驱动注册名应为 sqlite")
	}
}
