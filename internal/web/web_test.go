package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"simkeeper/internal/auth"
)

// Web 层纯函数测试：ClientIP 的信任策略、号码规范化、CSV 转义、CSRF 校验。

func TestClientIPTrustPolicy(t *testing.T) {
	a := &App{}
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 10.0.0.1")

	// 默认不信任 XFF：取 RemoteAddr
	if got := a.ClientIP(r); got != "203.0.113.9" {
		t.Errorf("trust=false 应取 RemoteAddr, got %q", got)
	}

	// 信任反代：取链最右值（反代追加的，客户端伪造的前缀无效）
	a.TrustProxy = true
	if got := a.ClientIP(r); got != "10.0.0.1" {
		t.Errorf("trust=true 应取最右值 10.0.0.1, got %q", got)
	}

	// 无 XFF 时回退 RemoteAddr
	r2 := httptest.NewRequest("POST", "/login", nil)
	r2.RemoteAddr = "198.51.100.7:1234"
	if got := a.ClientIP(r2); got != "198.51.100.7" {
		t.Errorf("无 XFF 应回退 RemoteAddr, got %q", got)
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"+852 9123-4567":    "+85291234567",
		" 86-138-0013-8000": "+8613800138000",
		"13800138000":       "+13800138000",
		"+44 20 7946 0958":  "+442079460958",
	}
	for in, want := range cases {
		if got := normalizePhone(in); got != want {
			t.Errorf("normalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
	// 非法字符透传由 phoneRe 在校验层拒绝，这里只保证不崩溃、不产出错误前缀
	if got := normalizePhone("+++"); got != "+++" {
		t.Errorf("无数字输入应原样透传, got %q", got)
	}
	if got := normalizePhone("abc"); got != "" {
		t.Errorf("无数字输入应得空串, got %q", got)
	}
}

func TestCSVRowEscaping(t *testing.T) {
	if got := csvRow("a", `he said "hi", ok`); !strings.Contains(got, `"he said ""hi"", ok"`) {
		t.Errorf("含逗号/引号的字段应被引号包裹并转义: %q", got)
	}
	if got := csvRow("a", "b"); got != "a,b\n" {
		t.Errorf("普通行 = %q", got)
	}
}

func TestMaskSecret(t *testing.T) {
	mask := funcMap["maskSecret"].(func(string) string)
	if got := mask("abcd1234efgh"); len(got) != len("abcd1234efgh") || strings.Contains(got, "1234") {
		t.Errorf("遮蔽后不应泄露中段: %q", got)
	}
	if got := mask("ab"); got != "**" {
		t.Errorf("短值全遮蔽: %q", got)
	}
}

func TestVerifyCSRF(t *testing.T) {
	sess := &auth.Session{CSRFToken: "tok-123"}

	// 表单字段
	r := httptest.NewRequest("POST", "/x", strings.NewReader("csrf_token=tok-123"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !auth.VerifyCSRF(r, sess) {
		t.Error("正确的表单令牌应通过")
	}

	// 请求头
	r2 := httptest.NewRequest("POST", "/x", nil)
	r2.Header.Set("X-CSRF-Token", "tok-123")
	if !auth.VerifyCSRF(r2, sess) {
		t.Error("正确的请求头令牌应通过")
	}

	// 错误令牌 / 无会话
	r3 := httptest.NewRequest("POST", "/x", strings.NewReader("csrf_token=wrong"))
	r3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if auth.VerifyCSRF(r3, sess) {
		t.Error("错误令牌不应通过")
	}
	if auth.VerifyCSRF(r3, nil) {
		t.Error("无会话不应通过")
	}

	// GET 请求不校验
	r4 := httptest.NewRequest("GET", "/x", nil)
	if auth.VerifyCSRF(r4, nil) {
		t.Error("GET 不应要求 CSRF")
	}
}
