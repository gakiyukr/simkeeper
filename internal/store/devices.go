package store

import (
	"database/sql"
	"time"

	"simkeeper/internal/db"
)

// Device 对应 devices 表：eSIM 安装在什么硬件上（单账号自用记录）。
type Device struct {
	ID         int64
	UserID     int64
	Name       string
	DeviceType string // phone | tablet | watch | modem | other
	Notes      string
	CreatedAt  string
	UpdatedAt  string
}

// DeviceRepo 设备数据访问。
type DeviceRepo struct {
	DB *db.DB
}

const deviceCols = `id, user_id, name, device_type, notes, created_at, updated_at`

func scanDevice(scan interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var notes sql.NullString
	err := scan.Scan(&d.ID, &d.UserID, &d.Name, &d.DeviceType, &notes, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Notes = notes.String
	return &d, nil
}

// ByID 按 ID 查找；userID > 0 时同时限定归属。
func (r *DeviceRepo) ByID(id, userID int64) (*Device, error) {
	q := `SELECT ` + deviceCols + ` FROM devices WHERE id = ?`
	args := []any{id}
	if userID > 0 {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	return scanDevice(r.DB.QueryRow(q, args...))
}

// Create 新建设备。
func (r *DeviceRepo) Create(d *Device) (int64, error) {
	return r.DB.InsertID(
		`INSERT INTO devices (user_id, name, device_type, notes) VALUES (?,?,?,?)`,
		d.UserID, d.Name, d.DeviceType, nullStr(d.Notes),
	)
}

// Update 全量更新设备（归属兜底由 WHERE user_id 承担）。
func (r *DeviceRepo) Update(d *Device) error {
	_, err := r.DB.Exec(
		`UPDATE devices SET name=?, device_type=?, notes=?, updated_at=? WHERE id=? AND user_id=?`,
		d.Name, d.DeviceType, nullStr(d.Notes), db.Touch(time.Now()), d.ID, d.UserID,
	)
	return err
}

// Delete 删除设备并将其名下号码的安装位置清空。
// device_id 迁移列不带外键（SQLite 无法 ALTER ADD CONSTRAINT），由应用层保证一致。
func (r *DeviceRepo) Delete(id, userID int64) error {
	if _, err := r.DB.Exec(
		`UPDATE phone_numbers SET device_id = NULL, updated_at = ? WHERE device_id = ? AND user_id = ?`,
		db.Touch(time.Now()), id, userID,
	); err != nil {
		return err
	}
	_, err := r.DB.Exec(`DELETE FROM devices WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// ListForUser 列出用户的设备（按名称排序，设备数量少无需分页）。
func (r *DeviceRepo) ListForUser(userID int64) ([]Device, error) {
	rows, err := r.DB.Query(
		`SELECT `+deviceCols+` FROM devices WHERE user_id = ? ORDER BY name, id`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}
