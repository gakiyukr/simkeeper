// Package db 负责数据库连接管理与 schema 迁移。
// 支持三种驱动：SQLite（modernc.org/sqlite，纯 Go）、MySQL（go-sql-driver）、
// PostgreSQL（pgx/stdlib，纯 Go）。方言差异集中在 Dialect（dialect.go），
// 查询统一写 "?" 占位符，由 *DB 包装层按需重写。
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

//go:embed schema_sqlite.sql
var schemaSQLite string

//go:embed schema_mysql.sql
var schemaMySQL string

//go:embed schema_postgres.sql
var schemaPostgres string

// DB 包装 *sql.DB 并携带方言。
// 内嵌使 QueryRow/Exec 等调用点语法不变；下面的方法重写会在
// PostgreSQL 场景自动把 "?" 重绑为 "$n"，其余方言原样透传。
type DB struct {
	*sql.DB
	Dialect Dialect
}

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.DB.Exec(d.Dialect.Rebind(query), args...)
}

func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.DB.Query(d.Dialect.Rebind(query), args...)
}

func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.DB.QueryRow(d.Dialect.Rebind(query), args...)
}

// Open 按 driver 打开数据库。dsn 由调用方按驱动格式给出：
//   - sqlite：文件路径（这里负责拼上 WAL / busy_timeout / foreign_keys 编译参数）
//   - mysql：  user:pass@tcp(host:port)/dbname?charset=utf8mb4
//     （不要开 parseTime：DATETIME 需以字符串返回，与 Go 侧格式约定一致）
//   - postgres：postgres://user:pass@host:port/dbname
func Open(driver, dsn string) (*DB, error) {
	dialect := Dialect{Name: driver}
	switch driver {
	case DialectSQLite:
		dsn = fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", dsn)
	case DialectMySQL, DialectPostgres:
		if dsn == "" {
			return nil, fmt.Errorf("驱动 %s 需要 -dsn / BHT_DSN 提供连接串", driver)
		}
	default:
		return nil, fmt.Errorf("不支持的数据库驱动 %q（可选：sqlite / mysql / postgres）", driver)
	}

	handle, err := sql.Open(dialect.DriverName(), dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	if driver == DialectSQLite {
		// SQLite 驱动对并发写不友好，限制单连接串行化写入，读由 WAL 兜底；
		// MySQL/PG 有真正的并发写能力，交给连接池默认行为。
		handle.SetMaxOpenConns(1)
	}
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	return &DB{DB: handle, Dialect: dialect}, nil
}

// Migrate 执行与驱动匹配的内嵌 schema，并为旧库补齐历史新增列。
// 脚本按 ";" 拆成单条语句执行（pgx 的扩展协议拒绝一次执行多条语句），
// 全部语句都是 IF NOT EXISTS / 幂等写法，可安全重复执行。
func (d *DB) Migrate() error {
	var script string
	switch d.Dialect.Name {
	case DialectMySQL:
		script = schemaMySQL
	case DialectPostgres:
		script = schemaPostgres
	default:
		script = schemaSQLite
	}
	for _, stmt := range splitStatements(script) {
		if _, err := d.DB.Exec(d.Dialect.Rebind(stmt)); err != nil {
			return fmt.Errorf("执行 schema 迁移失败: %w\n语句: %.200s", err, stmt)
		}
	}
	if err := d.migrateExtra(); err != nil {
		return fmt.Errorf("增量迁移失败: %w", err)
	}
	return nil
}

// Touch 返回当前时间的标准存储格式（"YYYY-MM-DD HH:MM:SS"）。
// 业务代码统一用它生成时间戳，保证三方言写入格式一致。
func Touch(t time.Time) string {
	return t.Format(time.DateTime)
}
