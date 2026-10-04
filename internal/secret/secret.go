// Package secret 提供静态敏感数据（渠道凭据、MTProto 会话）的对称加密。
//
// 算法：AES-256-GCM，随机 12 字节 nonce，密文带 "enc:v1:" 前缀落库——
// 解密时无前缀的值按历史明文兼容处理，下次保存时自动转为密文。
//
// 密钥来源（优先级从高到低）：
//  1. 环境变量 SK_SECRET_KEY：64 位十六进制（32 字节）
//  2. 密钥文件（默认 data/secret.key，不存在则自动生成，权限 0600）
//
// 密钥与数据库分文件存放是本机制的安全前提：单独泄露数据库文件
// （或未带密钥文件的备份）时，加密字段不可读。两者同时泄露等于明文。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const prefix = "enc:v1:"

var (
	aead   cipher.AEAD
	key    []byte
	inited bool
)

// ErrNotInited 在未调用 Init 时使用加密/解密返回。
var ErrNotInited = errors.New("secret: 未初始化")

// Init 装载或生成密钥。重复调用是空操作。
// keyFile 为空时跳过文件来源（此时必须已通过环境变量提供密钥）。
func Init(keyFile string) error {
	if inited {
		return nil
	}
	if env := strings.TrimSpace(os.Getenv("SK_SECRET_KEY")); env != "" {
		k, err := hex.DecodeString(env)
		if err != nil || len(k) != 32 {
			return fmt.Errorf("SK_SECRET_KEY 应为 64 位十六进制字符串（32 字节）")
		}
		return initWithKey(k)
	}
	if keyFile == "" {
		return fmt.Errorf("secret: 未提供密钥文件且未设置 SK_SECRET_KEY")
	}
	if b, err := os.ReadFile(keyFile); err == nil && len(b) == 32 {
		return initWithKey(b)
	}
	// 生成新密钥并落盘（0600，与数据库分离）
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return fmt.Errorf("secret: 生成密钥失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return fmt.Errorf("secret: 创建密钥目录失败: %w", err)
	}
	if err := os.WriteFile(keyFile, k, 0o600); err != nil {
		return fmt.Errorf("secret: 写入密钥文件失败: %w", err)
	}
	return initWithKey(k)
}

func initWithKey(k []byte) error {
	block, err := aes.NewCipher(k)
	if err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("secret: %w", err)
	}
	key = k
	aead, inited = a, true
	return nil
}

// Encrypt 加密字符串；空串原样返回。失败时返回错误，调用方应中止保存
// 而不是降级明文。
func Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if !inited {
		return "", ErrNotInited
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secret: %w", err)
	}
	out := aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt 解密；无前缀的值按历史明文原样返回（惰性迁移的兼容层）。
func Decrypt(s string) (string, error) {
	if s == "" || !strings.HasPrefix(s, prefix) {
		return s, nil
	}
	if !inited {
		return "", ErrNotInited
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return "", fmt.Errorf("secret: %w", err)
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("secret: 密文过短")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("secret: 解密失败（密钥不匹配或数据损坏）: %w", err)
	}
	return string(plain), nil
}

// IsEncrypted 报告值是否已是本包的密文格式（迁移判断用）。
func IsEncrypted(s string) bool { return strings.HasPrefix(s, prefix) }

// MAC 计算 HMAC-SHA256 十六进制串，用于签名短期令牌（如登录两步验证的待验证 Cookie）。
func MAC(msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// Ready 报告密钥是否已装载。
func Ready() bool { return inited }
