package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"baohaotong/internal/db"
)

// SessionTTL 会话有效期。固定 7 天，不做滑动续期，到期需重新登录。
const SessionTTL = 7 * 24 * time.Hour

// Session 是一条服务器端会话记录。
type Session struct {
	Token     string
	UserID    int64
	CSRFToken string
	IP        string
	UserAgent string
	ExpiresAt time.Time
}

// SessionStore 管理服务器端会话；Cookie 只携带不透明 token。
type SessionStore struct {
	DB *db.DB
}

// randomToken 生成 32 字节随机数的十六进制串，用作会话与 CSRF 令牌。
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机令牌失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Create 为用户新建会话，返回会话记录。
func (s *SessionStore) Create(userID int64, ip, userAgent string) (*Session, error) {
	return s.insert(sql.NullInt64{Int64: userID, Valid: true}, ip, userAgent)
}

// CreateAnonymous 建立匿名预会话（user_id 为 NULL），仅用于在
// 登录/注册/初始化页面向未登录访客提供 CSRF 令牌。
func (s *SessionStore) CreateAnonymous(ip, userAgent string) (*Session, error) {
	return s.insert(sql.NullInt64{}, ip, userAgent)
}

func (s *SessionStore) insert(userID sql.NullInt64, ip, userAgent string) (*Session, error) {
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(SessionTTL)
	var uid any
	if userID.Valid {
		uid = userID.Int64
	}
	_, err = s.DB.Exec(
		`INSERT INTO sessions (token, user_id, ip, user_agent, csrf_token, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		token, uid, ip, userAgent, csrf, expires.Format(time.DateTime),
	)
	if err != nil {
		return nil, fmt.Errorf("写入会话失败: %w", err)
	}
	sess := &Session{
		Token:     token,
		CSRFToken: csrf,
		IP:        ip,
		UserAgent: userAgent,
		ExpiresAt: expires,
	}
	if userID.Valid {
		sess.UserID = userID.Int64
	}
	return sess, nil
}

// Get 读取有效会话；不存在或已过期返回 nil。
func (s *SessionStore) Get(token string) (*Session, error) {
	if token == "" {
		return nil, nil
	}
	row := s.DB.QueryRow(
		`SELECT token, user_id, csrf_token, ip, user_agent, expires_at
		 FROM sessions WHERE token = ?`, token,
	)
	var sess Session
	var uid sql.NullInt64
	var expiresStr string
	err := row.Scan(&sess.Token, &uid, &sess.CSRFToken, &sess.IP, &sess.UserAgent, &expiresStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if uid.Valid {
		sess.UserID = uid.Int64
	}
	expires, err := time.Parse(time.DateTime, expiresStr)
	if err != nil {
		return nil, fmt.Errorf("解析会话过期时间失败: %w", err)
	}
	if time.Now().After(expires) {
		// 顺手清掉过期会话，防止表膨胀
		_ = s.Destroy(token)
		return nil, nil
	}
	sess.ExpiresAt = expires
	return &sess, nil
}

// Destroy 删除指定会话（登出）。
func (s *SessionStore) Destroy(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// DestroyForUser 撤销某用户的全部会话（改密后调用，强制全部设备重新登录）。
func (s *SessionStore) DestroyForUser(userID int64) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// GC 清理全部过期会话，由定时任务周期调用。
func (s *SessionStore) GC() error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Format(time.DateTime))
	return err
}
