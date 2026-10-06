package web

// HTTP 集成测试：真实路由 + 临时 SQLite 库（httptest），覆盖
// 登录/CSRF/限流/未登录拦截与号码、渠道配置的导入导出往返。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"simkeeper/internal/auth"
	"simkeeper/internal/db"
	"simkeeper/internal/secret"
	"simkeeper/internal/store"
)

// integrationApp 组装真实 App（临时 SQLite + 模板）。
func integrationApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	h, err := db.Open("sqlite", filepath.Join(dir, "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if err := h.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := (&store.SettingRepo{DB: h}).SeedDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := secret.Init(filepath.Join(dir, "secret.key")); err != nil {
		t.Fatal(err)
	}
	a, err := New(h)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// newTestServer 起真实 HTTP 服务（不跟随跳转，便于断言状态码）。
func newTestServer(t *testing.T, a *App) (*httptest.Server, *http.Client) {
	t.Helper()
	// CSRF 令牌绑定服务器端会话：客户端必须带 CookieJar 才能持有 SKSESSION
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Routes())
	t.Cleanup(srv.Close)
	return srv, &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

func getCSRF(t *testing.T, c *http.Client, base, path string) string {
	t.Helper()
	resp, err := c.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("%s 页面无 CSRF 令牌", path)
	}
	return string(m[1])
}

func postForm(t *testing.T, c *http.Client, base, path string, form url.Values) (int, string) {
	t.Helper()
	resp, err := c.PostForm(base+path, form)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
}

func postMultipart(t *testing.T, c *http.Client, base, path, filename string, content []byte, extra url.Values) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, vs := range extra {
		for _, v := range vs {
			_ = w.WriteField(k, v)
		}
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", base+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body)
}

func loginAdmin(t *testing.T, c *http.Client, base string) {
	t.Helper()
	tok := getCSRF(t, c, base, "/login")
	code, body := postForm(t, c, base, "/login", url.Values{
		"username": {"admin"}, "password": {"password123"}, "csrf_token": {tok},
	})
	if code != 302 {
		t.Fatalf("登录应 302, got %d %s", code, body)
	}
}

func mustAdmin(t *testing.T, a *App) {
	t.Helper()
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Users.Create("admin", "admin@test.local", hash, "admin"); err != nil {
		t.Fatal(err)
	}
}

// TestHTTPLoginFlowAndCSRF 登录链路：无 CSRF 403、错密码回显、正确密码 302、登录后首页可达。
func TestHTTPLoginFlowAndCSRF(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)

	tok := getCSRF(t, c, srv.URL, "/login")
	code, _ := postForm(t, c, srv.URL, "/login", url.Values{
		"username": {"admin"}, "password": {"password123"},
	})
	if code != 403 {
		t.Fatalf("无 CSRF 应 403, got %d", code)
	}
	code, _ = postForm(t, c, srv.URL, "/login", url.Values{
		"username": {"admin"}, "password": {"wrong-pass"}, "csrf_token": {tok},
	})
	if code != 200 {
		t.Fatalf("错误密码应回显登录页 200, got %d", code)
	}
	code, _ = postForm(t, c, srv.URL, "/login", url.Values{
		"username": {"admin"}, "password": {"password123"}, "csrf_token": {tok},
	})
	if code != 302 {
		t.Fatalf("正确密码应 302, got %d", code)
	}
	resp, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("登录后首页应 200, got %d", resp.StatusCode)
	}
	// 未登录另一客户端访问首页 → 302 到登录页
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp2, err := anon.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 302 {
		t.Fatalf("未登录访问首页应 302, got %d", resp2.StatusCode)
	}
}

// TestHTTPLoginRateLimit 连续 5 次错误密码后，第 6 次（即使密码正确）被限流。
func TestHTTPLoginRateLimit(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)

	tok := getCSRF(t, c, srv.URL, "/login")
	for i := 0; i < 5; i++ {
		code, _ := postForm(t, c, srv.URL, "/login", url.Values{
			"username": {"admin"}, "password": {"wrong"}, "csrf_token": {tok},
		})
		if code != 200 {
			t.Fatalf("第 %d 次错误密码应 200, got %d", i+1, code)
		}
	}
	code, body := postForm(t, c, srv.URL, "/login", url.Values{
		"username": {"admin"}, "password": {"password123"}, "csrf_token": {tok},
	})
	if code != 429 {
		t.Fatalf("第 6 次应 429 限流, got %d", code)
	}
	if !strings.Contains(body, "登录失败次数过多") {
		t.Error("应提示限流文案")
	}
}

// TestHTTPExportImportRoundTrip 号码导出 → 清库 → 导入：eSIM 激活信息完整还原。
func TestHTTPExportImportRoundTrip(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	devID, err := a.Devices.Create(&store.Device{UserID: 1, Name: "Pixel 8", DeviceType: "phone"})
	if err != nil {
		t.Fatal(err)
	}
	tok := getCSRF(t, c, srv.URL, "/numbers/new")
	code, _ := postForm(t, c, srv.URL, "/numbers/new", url.Values{
		"country_code": {"JP"}, "phone_national": {"9012345678"}, "carrier": {"KDDI"},
		"expiry_date": {"2099-01-01"}, "auto_expiry_period": {"90"}, "keepalive_mode": {"keep"},
		"renewal_days_before": {"7"}, "sim_type": {"esim"},
		"lpa_string": {"LPA:1$rsp.example.com$ABCD"}, "device_id": {fmt.Sprint(devID)},
		"csrf_token": {tok},
	})
	if code != 303 {
		t.Fatalf("建号应 303, got %d", code)
	}

	resp, err := c.Get(srv.URL + "/admin/export?format=json")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("导出应 200, got %d", resp.StatusCode)
	}
	var doc struct {
		ESIMCards []json.RawMessage `json:"esimCards"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.ESIMCards) != 1 {
		t.Fatalf("导出 JSON 应含 1 张卡: %v", err)
	}

	// 模拟换机：清空号码后用导出文件导入
	if _, err := a.DB.Exec(`DELETE FROM phone_numbers`); err != nil {
		t.Fatal(err)
	}
	tok = getCSRF(t, c, srv.URL, "/admin")
	code, body := postMultipart(t, c, srv.URL, "/admin/import", "export.json", raw,
		url.Values{"csrf_token": {tok}})
	if code != 303 {
		t.Fatalf("导入应 303, got %d %s", code, body)
	}
	adminHTML := func() string {
		resp, err := c.Get(srv.URL + "/admin")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}()
	if !strings.Contains(adminHTML, "导入完成：新建 1") {
		t.Errorf("应提示新建 1 条, got: %q", adminHTML[:min(200, len(adminHTML))])
	}
	n, err := a.Numbers.ByPhone(1, "+819012345678")
	if err != nil {
		t.Fatalf("导入后号码应存在: %v", err)
	}
	if n.LPAString != "LPA:1$rsp.example.com$ABCD" || n.ConfirmCode != "" || n.SimType != "esim" {
		t.Errorf("eSIM 激活信息未还原: %+v", n)
	}
}

// TestHTTPChannelConfigExportImport 渠道配置导出（明文凭据）→ 清空 → 导入还原。
func TestHTTPChannelConfigExportImport(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	if err := a.Notify.SaveConfig(&store.NotifyConfig{
		UserID: 1, TelegramEnabled: true, TelegramBotToken: "123:abc",
		EmailEnabled: true, EmailSMTPHost: "smtp.x", EmailSMTPPort: 465,
		EmailSMTPSecure: "ssl", EmailPassword: "pw",
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(srv.URL + "/admin/export?format=channels")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("渠道导出应 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(raw), "123:abc") {
		t.Error("渠道导出应含明文凭据（备份用途）")
	}
	// 清空配置后导入
	if err := a.Notify.SaveConfig(&store.NotifyConfig{UserID: 1}); err != nil {
		t.Fatal(err)
	}
	tok := getCSRF(t, c, srv.URL, "/admin")
	code, body := postMultipart(t, c, srv.URL, "/admin/import", "channels.json", raw,
		url.Values{"csrf_token": {tok}})
	if code != 303 {
		t.Fatalf("导入应 303, got %d %s", code, body)
	}
	cfg, err := a.Notify.ConfigForUser(1)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TelegramEnabled || cfg.TelegramBotToken != "123:abc" || cfg.EmailPassword != "pw" {
		t.Errorf("渠道配置未还原: %+v", cfg)
	}
}

// TestHTTPUnknownImportFormat 无法识别的文件应有明确提示。
func TestHTTPUnknownImportFormat(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)
	tok := getCSRF(t, c, srv.URL, "/admin")
	code, body := postMultipart(t, c, srv.URL, "/admin/import", "x.txt", []byte("hello world"),
		url.Values{"csrf_token": {tok}})
	if code != 303 {
		t.Fatalf("导入应 303, got %d %s", code, body)
	}
	resp, err := c.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "无法识别的文件格式") {
		t.Error("应提示无法识别格式")
	}
}

// TestHTTPNumberLastKeepaliveMode 「我刚保过号」口径：服务端按
// 上次保号日期 + 周期推算到期日，周期起点即上次保号日期。
func TestHTTPNumberLastKeepaliveMode(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	last := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	tok := getCSRF(t, c, srv.URL, "/numbers/new")
	code, body := postForm(t, c, srv.URL, "/numbers/new", url.Values{
		"country_code": {"JP"}, "phone_national": {"9012345678"}, "carrier": {"KDDI"},
		"keepalive_mode": {"keep"}, "date_mode": {"last"},
		"last_keepalive_date": {last}, "auto_expiry_period": {"180"},
		"renewal_days_before": {"7"}, "csrf_token": {tok},
	})
	if code != 303 {
		t.Fatalf("建号应 303, got %d %s", code, body)
	}
	n, err := a.Numbers.ByPhone(1, "+819012345678")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Now().AddDate(0, 0, 177).Format("2006-01-02") // -3 + 180
	if n.ExpiryDate != want || n.AutoStartDate != last {
		t.Errorf("到期日应 = 上次保号 + 周期: expiry=%s want=%s start=%s want=%s",
			n.ExpiryDate, want, n.AutoStartDate, last)
	}
}
