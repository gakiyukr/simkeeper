package store

import "database/sql"

// 可空值转换辅助：Go 零值映射为 SQL NULL，避免空串污染数据。

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func nullInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullStringFrom 扫描用：把 sql.NullString 转为普通字符串。
func nullStringFrom(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}
