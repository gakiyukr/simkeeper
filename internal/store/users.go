package store

import (
	"database/sql"
	"errors"
	"time"

	"baohaotong/internal/auth"
	"baohaotong/internal/db"
)

// ErrNotFound 数据记录不存在时的统一错误。
var ErrNotFound = errors.New("记录不存在")

// User 对应 users 表。
type User struct {
	ID        int64
	Username  string
	Email     string
	Role      string // user | admin
	Status    string // active | inactive | banned
	CreatedAt string
}

// UserRepo 用户数据访问。
type UserRepo struct {
	DB *db.DB
}

const userCols = `id, username, email, role, status, created_at`

func scanUser(scan interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := scan.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.Status, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ByUsername 按用户名查找。
func (r *UserRepo) ByUsername(username string) (*User, error) {
	return scanUser(r.DB.QueryRow(
		`SELECT `+userCols+` FROM users WHERE username = ?`, username,
	))
}

// ByEmail 按邮箱查找。
func (r *UserRepo) ByEmail(email string) (*User, error) {
	return scanUser(r.DB.QueryRow(
		`SELECT `+userCols+` FROM users WHERE email = ?`, email,
	))
}

// ByID 按主键查找。
func (r *UserRepo) ByID(id int64) (*User, error) {
	return scanUser(r.DB.QueryRow(
		`SELECT `+userCols+` FROM users WHERE id = ?`, id,
	))
}

// EmailHashByID 只取密码哈希，供登录校验使用。
func (r *UserRepo) PasswordHashByID(id int64) (string, error) {
	var hash string
	err := r.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return hash, err
}

// Create 新建用户（注册与初始化管理员共用）。密码哈希在调用前生成。
func (r *UserRepo) Create(username, email, passwordHash, role string) (int64, error) {
	return r.DB.InsertID(
		`INSERT INTO users (username, email, password_hash, role) VALUES (?, ?, ?, ?)`,
		username, email, passwordHash, role,
	)
}

// UpdatePassword 重置密码哈希。
func (r *UserRepo) UpdatePassword(id int64, passwordHash string) error {
	_, err := r.DB.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, db.Touch(time.Now()), id,
	)
	return err
}

// UpdateEmail 修改邮箱。
func (r *UserRepo) UpdateEmail(id int64, email string) error {
	_, err := r.DB.Exec(
		`UPDATE users SET email = ?, updated_at = ? WHERE id = ?`,
		email, db.Touch(time.Now()), id,
	)
	return err
}

// SetStatus 启用/禁用/封禁。
func (r *UserRepo) SetStatus(id int64, status string) error {
	_, err := r.DB.Exec(`UPDATE users SET status = ?, updated_at = ? WHERE id = ?`,
		status, db.Touch(time.Now()), id)
	return err
}

// SetRole 调整角色。
func (r *UserRepo) SetRole(id int64, role string) error {
	_, err := r.DB.Exec(`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`,
		role, db.Touch(time.Now()), id)
	return err
}

// Delete 删除用户；号码与通知记录由外键级联清理。
func (r *UserRepo) Delete(id int64) error {
	_, err := r.DB.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// CountAdmins 统计 active 管理员数量。
// 后台封禁/删除管理员的操作必须先经此校验，避免删光管理员把自己锁死。
func (r *UserRepo) CountAdmins() (int, error) {
	var n int
	err := r.DB.QueryRow(
		`SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active'`,
	).Scan(&n)
	return n, err
}

// Authenticate 登录校验：查用户、比对哈希、检查状态。
// 返回 (用户, 是否成功)；不区分「用户不存在」与「密码错误」。
// 用户不存在时仍执行一次假哈希比对，避免时序侧信道暴露用户名。
func (r *UserRepo) Authenticate(username, password string) (*User, bool, error) {
	// 预生成的 bcrypt 哈希，明文任意，仅为对不存在用户拉平耗时
	const dummyHash = "$2a$12$C6UzMDM.H6dfI/f/IKcEeO7ZDZQj1Vp1p2b3c4d5e6f7g8h9i0jKu"
	u, err := r.ByUsername(username)
	if err == ErrNotFound {
		_ = auth.CheckPassword(dummyHash, password)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	hash, err := r.PasswordHashByID(u.ID)
	if err != nil {
		return nil, false, err
	}
	if !auth.CheckPassword(hash, password) || u.Status != "active" {
		return nil, false, nil
	}
	return u, true, nil
}

// List 分页列出用户，支持按用户名/邮箱模糊搜索。
// LIKE 一律套 LOWER()：三种方言的大小写敏感行为不同，lower 后行为一致。
func (r *UserRepo) List(page, limit int, search string) ([]User, int, error) {
	where := ``
	args := []any{}
	if search != "" {
		where = ` WHERE LOWER(username) LIKE LOWER(?) OR LOWER(email) LIKE LOWER(?)`
		like := `%` + search + `%`
		args = append(args, like, like)
	}
	var total int
	if err := r.DB.QueryRow(`SELECT COUNT(*) FROM users`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit
	rows, err := r.DB.Query(
		`SELECT `+userCols+` FROM users`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.Status, &u.CreatedAt); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}
