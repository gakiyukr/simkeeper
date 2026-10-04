package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"simkeeper/internal/db"
)

// PasswordResetRepo 密码重置令牌：数据库只存 SHA-256 哈希，
// 原文仅存在于发给用户的邮件链接里，1 小时有效、单次使用。
type PasswordResetRepo struct {
	DB *db.DB
}

// NewResetToken 生成一对（令牌原文、SHA-256 哈希）。
func NewResetToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(buf)
	h := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(h[:]), nil
}

// HashToken 对用户提交的令牌原文做同样的哈希，用于查找。
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Create 记录一个新令牌。
func (r *PasswordResetRepo) Create(userID int64, tokenHash string, expires time.Time) error {
	_, err := r.DB.Exec(
		`INSERT INTO password_resets (user_id, token_hash, expires_at) VALUES (?, ?, ?)`,
		userID, tokenHash, expires.Format(time.DateTime),
	)
	return err
}

// CountActiveForUser 统计用户名下未使用、未过期的令牌数（自助重置限流用）。
func (r *PasswordResetRepo) CountActiveForUser(userID int64, now time.Time) (int, error) {
	var n int
	err := r.DB.QueryRow(
		`SELECT COUNT(*) FROM password_resets WHERE user_id = ? AND used_at IS NULL AND expires_at > ?`,
		userID, now.Format(time.DateTime),
	).Scan(&n)
	return n, err
}

// Consume 校验并使用令牌：有效则标记已用并返回 user_id，否则返回 ErrNotFound。
// 查找与标记非原子，但令牌单次使用的竞争窗口后果仅是重复改密，可接受。
func (r *PasswordResetRepo) Consume(tokenHash string, now time.Time) (int64, error) {
	var userID int64
	err := r.DB.QueryRow(
		`SELECT user_id FROM password_resets
		 WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		tokenHash, now.Format(time.DateTime),
	).Scan(&userID)
	if err == sql.ErrNoRows {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	_, err = r.DB.Exec(
		`UPDATE password_resets SET used_at = ? WHERE token_hash = ?`,
		now.Format(time.DateTime), tokenHash,
	)
	return userID, err
}

// Valid 报告令牌是否有效（不消费）。
func (r *PasswordResetRepo) Valid(tokenHash string, now time.Time) bool {
	var n int
	err := r.DB.QueryRow(
		`SELECT COUNT(*) FROM password_resets
		 WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		tokenHash, now.Format(time.DateTime),
	).Scan(&n)
	return err == nil && n > 0
}
