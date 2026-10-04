// dialect.go 封装三种数据库（SQLite / MySQL / PostgreSQL）的 SQL 方言差异。
//
// 设计约定（三方言统一的行为前提）：
//   - 占位符统一写 "?"，由 *DB 包装层在 PostgreSQL 场景下重写为 "$n"。
//   - 时间戳在三种数据库里都以 "YYYY-MM-DD HH:MM:SS" 文本形式存取：
//     SQLite 用 TEXT 列，MySQL 用 DATETIME 列（驱动关闭 parseTime 时以字符串返回），
//     PostgreSQL 用 TEXT 列。该格式字典序即时间序，范围比较与排序全方言一致，
//     Go 侧的 time.Parse 也无需按方言区分。
//   - 生成的 SQL 里不含字符串字面量，因此 Rebind 可以安全地按序替换 "?"。
package db

import (
	"fmt"
	"strconv"
	"strings"
)

// 方言名。DriverName 是 sql.Open 注册名，Name 是内部标识。
const (
	DialectSQLite   = "sqlite"
	DialectMySQL    = "mysql"
	DialectPostgres = "postgres"
)

// Dialect 描述目标数据库的方言。
type Dialect struct {
	Name string // sqlite | mysql | postgres
}

// DriverName 返回 database/sql 的驱动注册名（PostgreSQL 由 pgx/stdlib 注册为 "pgx"）。
func (d Dialect) DriverName() string {
	if d.Name == DialectPostgres {
		return "pgx"
	}
	return d.Name
}

// Rebind 把查询里的 "?" 占位符按出现顺序重写为 "$1、$2…"。
// 仅 PostgreSQL 需要；其余方言原样返回。
// 前提：SQL 里没有位于字符串字面量中的 "?"（本项目全部查询满足）。
func (d Dialect) Rebind(query string) string {
	if d.Name != DialectPostgres {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// placeholders 生成 n 个 "?" 占位符组成的 "?, ?, …"。
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// UpsertIgnore 生成「主键冲突时跳过」的插入语句。
// 例如 SeedDefaults：同一行已存在就保持原值。
func (d Dialect) UpsertIgnore(table, conflictCol string, cols []string) string {
	switch d.Name {
	case DialectMySQL:
		return "INSERT IGNORE INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders(len(cols)) + ")"
	case DialectPostgres:
		return "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders(len(cols)) + ") ON CONFLICT (" + conflictCol + ") DO NOTHING"
	default: // sqlite
		return "INSERT OR IGNORE INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders(len(cols)) + ")"
	}
}

// UpsertUpdate 生成「冲突则更新指定列」的 upsert 语句。
// updateCols 里的列在冲突时取本次插入的值（SQLite/PG 的 excluded，MySQL 的 VALUES()）。
// 注意：MySQL 的 VALUES() 写法在 8.0.20+ 标记为 deprecated，但仍是覆盖版本最广的写法。
func (d Dialect) UpsertUpdate(table, conflictCol string, cols, updateCols []string) string {
	q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders(len(cols)) + ")"
	switch d.Name {
	case DialectMySQL:
		sets := make([]string, len(updateCols))
		for i, c := range updateCols {
			sets[i] = c + "=VALUES(" + c + ")"
		}
		return q + " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
	case DialectPostgres:
		sets := make([]string, len(updateCols))
		for i, c := range updateCols {
			sets[i] = c + "=excluded." + c
		}
		return q + " ON CONFLICT (" + conflictCol + ") DO UPDATE SET " + strings.Join(sets, ", ")
	default: // sqlite
		sets := make([]string, len(updateCols))
		for i, c := range updateCols {
			sets[i] = c + "=excluded." + c
		}
		return q + " ON CONFLICT (" + conflictCol + ") DO UPDATE SET " + strings.Join(sets, ", ")
	}
}

// SameDay 返回「列值与参数落在同一天」的谓词，配一个 "2006-01-02" 格式的参数使用。
// 用于通知的当日去重：三种方言的日期提取函数不同。
func (d Dialect) SameDay(col string) string {
	switch d.Name {
	case DialectMySQL:
		return "DATE(" + col + ") = ?"
	case DialectPostgres:
		// 时间戳按约定存 "YYYY-MM-DD HH:MM:SS" 文本，取前 10 位即为日期
		return "left(" + col + ", 10) = ?"
	default: // sqlite
		return "date(" + col + ") = ?"
	}
}

// InsertID 执行 INSERT 并返回自增主键。
// SQLite / MySQL 走 LastInsertId；PostgreSQL 不支持，改为追加 RETURNING id。
func (d *DB) InsertID(query string, args ...any) (int64, error) {
	if d.Dialect.Name == DialectPostgres {
		var id int64
		err := d.DB.QueryRow(d.Dialect.Rebind(query)+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	res, err := d.DB.Exec(d.Dialect.Rebind(query), args...)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("获取自增 ID 失败: %w", err)
	}
	return id, nil
}

// splitStatements 把多语句 SQL 脚本按 ";" 拆成单条语句。
// 三种驱动对单次 Exec 执行多语句的支持不一（pgx 扩展协议直接拒绝），
// 统一拆开执行最稳妥。脚本内没有出现在字符串里的分号。
func splitStatements(script string) []string {
	raw := strings.Split(script, ";")
	stmts := make([]string, 0, len(raw))
	for _, s := range raw {
		if t := strings.TrimSpace(s); t != "" && !isOnlyComments(t) {
			stmts = append(stmts, t)
		}
	}
	return stmts
}

// isOnlyComments 判断语句片段是否只剩余注释行（如脚本结尾的说明注释）。
func isOnlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "--") {
			return false
		}
	}
	return true
}
