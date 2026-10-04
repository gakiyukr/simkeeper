package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP（RFC 6238）：SHA-1、6 位数字、30 秒步长——Google Authenticator 等
// 验证器应用的默认参数。纯标准库实现，无第三方依赖。

const totpStep = 30 * time.Second

// GenerateTOTPSecret 生成 20 字节随机密钥的 Base32 编码（无填充）。
// 这是验证器应用「手动输入密钥」时期望的格式。
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 TOTP 密钥失败: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// totpCode 计算指定时刻的 6 位验证码。secretBase32 大小写不敏感、忽略空格。
func totpCode(secretBase32 string, t time.Time) (string, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(secretBase32, " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil {
		return "", fmt.Errorf("TOTP 密钥不是有效的 Base32: %w", err)
	}
	counter := uint64(t.Unix()) / uint64(totpStep/time.Second)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[off])&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])
	return fmt.Sprintf("%06d", value%1_000_000), nil
}

// VerifyTOTP 校验 6 位验证码，允许前后各一个步长（±30 秒）的时钟偏差。
// 使用常量时间比较，避免时序侧信道。
func VerifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, delta := range []time.Duration{-totpStep, 0, totpStep} {
		want, err := totpCode(secret, time.Now().Add(delta))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// OTPAuthURI 生成验证器应用可识别的 otpauth:// 添加链接。
func OTPAuthURI(issuer, account, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&digits=6&period=30",
		url.PathEscape(issuer), url.PathEscape(account), secret, url.QueryEscape(issuer))
}
