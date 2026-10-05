package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"simkeeper/internal/auth"
	"simkeeper/internal/store"
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


func TestFilterSortNumbers(t *testing.T) {
	mk := func(id int64, phone, country, carrier, notes, status, expiry, created string) store.PhoneNumber {
		return store.PhoneNumber{ID: id, PhoneNumber: phone, CountryName: country, CountryCode: "XX",
			Carrier: carrier, Notes: notes, Status: status, ExpiryDate: expiry, CreatedAt: created}
	}
	nums := []store.PhoneNumber{
		mk(3, "+8613800000003", "中国", "移动", "备用", "active", "2026-12-01", "2026-10-01 10:00:00"),
		mk(1, "+85266000001", "香港", "CSL", "", "active", "2026-11-01", "2026-10-03 09:00:00"),
		mk(2, "+442079460958", "英国", "EE", "主力号", "inactive", "2020-01-01", "2026-10-02 12:00:00"), // 已过期+停用
		mk(4, "+8613900000004", "中国", "联通", "", "active", "2099-01-01", "2026-10-04 08:00:00"),
	}
	nums[3].NoKeepalive = true

	// 关键词：备注匹配
	if got := filterSortNumbers(nums, "主力号", "", ""); len(got) != 1 || got[0].ID != 2 {
		t.Errorf("备注搜索应命中 ID=2, got %+v", got)
	}
	// 分类：无需保号
	if got := filterSortNumbers(nums, "", "none", ""); len(got) != 1 || got[0].ID != 4 {
		t.Errorf("无需保号分类应命中 ID=4, got %+v", got)
	}
	// 分类：号码已丢失（用户显式标记，优先于已过期）
	if got := filterSortNumbers(nums, "", "inactive", ""); len(got) != 1 || got[0].ID != 2 {
		t.Errorf("丢失分类应命中 ID=2, got %+v", got)
	}
	// 分类：已过期（排除丢失后无剩余）
	if got := filterSortNumbers(nums, "", "expired", ""); len(got) != 0 {
		t.Errorf("已过期分类应为空, got %+v", got)
	}
	// 分类：需要周期性保号（活跃未过期，含手动与周期）
	got := filterSortNumbers(nums, "", "cycle", "")
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Errorf("周期保号分类应为 ID=1,3, got %+v", got)
	}
	// 默认排序：到期升序，无需保号排最后
	got = filterSortNumbers(nums, "", "", "")
	if got[0].ID != 2 || got[1].ID != 1 || got[2].ID != 3 || got[3].ID != 4 {
		t.Errorf("默认排序错误: %v", []int64{got[0].ID, got[1].ID, got[2].ID, got[3].ID})
	}
	// 降序 + 最新添加
	got = filterSortNumbers(nums, "", "", "expiry_desc")
	if got[0].ID != 3 {
		t.Errorf("到期降序应 ID=3 在前, got %d", got[0].ID)
	}
	got = filterSortNumbers(nums, "", "", "created_desc")
	if got[0].ID != 4 {
		t.Errorf("最新添加应 ID=4 在前, got %d", got[0].ID)
	}
}
