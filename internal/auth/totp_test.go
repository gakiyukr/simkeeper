package auth

import (
	"testing"
	"time"
)

// RFC 6238 附录 B 的官方测试向量（SHA-1，密钥 ASCII "12345678901234567890"，
// 其 Base32 为 GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ）。8 位截断去掉前 2 位即 6 位结果。

func TestTOTPRFC6238Vectors(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	cases := []struct {
		unix int64
		want string // RFC 向量的后 6 位
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, c := range cases {
		got, err := totpCode(secret, time.Unix(c.unix, 0))
		if err != nil {
			t.Fatalf("totpCode(%d): %v", c.unix, err)
		}
		if got != c.want {
			t.Errorf("totpCode(%d) = %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	now := time.Now()
	got, err := totpCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyTOTP(secret, got) {
		t.Errorf("当前验证码 %s 应通过校验", got)
	}
	if VerifyTOTP(secret, "abcdef") {
		t.Error("非数字输入不应通过")
	}
	if VerifyTOTP(secret, "12345") {
		t.Error("5 位输入不应通过")
	}
	if VerifyTOTP("!!invalid!!", "123456") {
		t.Error("非法密钥不应通过")
	}
}

func TestGenerateTOTPSecret(t *testing.T) {
	s, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 32 { // 20 字节 → base32 无填充 32 字符
		t.Errorf("secret 长度 = %d, want 32", len(s))
	}
	s2, _ := GenerateTOTPSecret()
	if s == s2 {
		t.Error("两次生成的密钥不应相同")
	}
}
