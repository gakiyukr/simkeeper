// Package auth 提供密码哈希、会话与 CSRF 防护。
package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword 生成 bcrypt 哈希。cost 12 在本应用量级下登录延迟可忽略。
func HashPassword(plain string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(plain), 12)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// CheckPassword 校验明文密码与哈希是否匹配。
// 用户不存在时调用方应传入一份固定哈希做假校验，拉平响应时间。
func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
