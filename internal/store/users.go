package store

import (
	"database/sql"
	"errors"
	"time"

	"simkeeper/internal/auth"
	"simkeeper/internal/db"
	"simkeeper/internal/secret"
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

// Create 新建账号，仅由 /setup 在系统零账号时调用（单账号系统）。密码哈希在调用前生成。
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

// TOTPForUser 读取两步验证配置（secret 解密）；用户不存在返回 ErrNotFound。
func (r *UserRepo) TOTPForUser(id int64) (totpSecret string, enabled bool, err error) {
	var s sql.NullString
	var e int
	err = r.DB.QueryRow(
		`SELECT COALESCE(totp_secret,''), COALESCE(totp_enabled,0) FROM users WHERE id = ?`, id,
	).Scan(&s, &e)
	if err != nil {
		return "", false, err
	}
	plain, derr := secret.Decrypt(s.String)
	if derr != nil {
		// 密钥不匹配等：按未设置处理，用户可重新生成
		return "", false, nil
	}
	return plain, e == 1 && plain != "", nil
}

// SetTOTP 写入或清除两步验证配置（secret 加密落库；enabled=false 时应同时清空 secret）。
func (r *UserRepo) SetTOTP(id int64, totpSecret string, enabled bool) error {
	enc, err := secret.Encrypt(totpSecret)
	if err != nil {
		return err
	}
	_, err = r.DB.Exec(
		`UPDATE users SET totp_secret = ?, totp_enabled = ?, updated_at = ? WHERE id = ?`,
		nullStr(enc), boolInt(enabled), db.Touch(time.Now()), id,
	)
	return err
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
