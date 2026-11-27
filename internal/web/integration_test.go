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
		"renewal_days_before": {"30"}, "reminder_interval_days": {"3"}, "sim_type": {"esim"},
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
	if n.RenewalDaysBefore != 30 || n.ReminderIntervalDays != 3 {
		t.Errorf("提醒窗口/重复间隔未往返还原: window=%d interval=%d", n.RenewalDaysBefore, n.ReminderIntervalDays)
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

// TestHTTPNumberPastedFullNumber 回归：前端未执行 blur 正規化（或 JS 关闭）时，
// 直接提交带 + 的完整国际号码也应创建成功，且只保留一个 + 与国家码。
func TestHTTPNumberPastedFullNumber(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	tok := getCSRF(t, c, srv.URL, "/numbers/new")
	code, body := postForm(t, c, srv.URL, "/numbers/new", url.Values{
		"country_code": {"JP"}, "phone_national": {"+81 90-1234-5678"}, "carrier": {"KDDI"},
		"expiry_date": {"2099-01-01"}, "auto_expiry_period": {"90"}, "keepalive_mode": {"keep"},
		"renewal_days_before": {"7"}, "csrf_token": {tok},
	})
	if code != 303 {
		t.Fatalf("粘贴完整号码应创建成功并 303, got %d %s", code, body)
	}
	n, err := a.Numbers.ByPhone(1, "+819012345678")
	if err != nil {
		t.Fatalf("号码未按预期保存: %v", err)
	}
	if n.PhoneNumber != "+819012345678" {
		t.Errorf("号码 = %q, want %q", n.PhoneNumber, "+819012345678")
	}
}

// TestHTTPNumberReminderInterval 重复提醒间隔：保存并在窗口内校验；
// 间隔大于提醒提前天数必须拒绝。
func TestHTTPNumberReminderInterval(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	tok := getCSRF(t, c, srv.URL, "/numbers/new")
	code, body := postForm(t, c, srv.URL, "/numbers/new", url.Values{
		"country_code": {"JP"}, "phone_national": {"9033333333"}, "carrier": {"KDDI"},
		"expiry_date": {"2099-01-01"}, "auto_expiry_period": {"90"}, "keepalive_mode": {"keep"},
		"renewal_days_before": {"30"}, "reminder_interval_days": {"3"}, "csrf_token": {tok},
	})
	if code != 303 {
		t.Fatalf("建号应 303, got %d %s", code, body)
	}
	n, err := a.Numbers.ByPhone(1, "+819033333333")
	if err != nil {
		t.Fatal(err)
	}
	if n.RenewalDaysBefore != 30 || n.ReminderIntervalDays != 3 {
		t.Errorf("提醒窗口/重复间隔未保存: window=%d interval=%d", n.RenewalDaysBefore, n.ReminderIntervalDays)
	}

	tok = getCSRF(t, c, srv.URL, "/numbers/new")
	code, body = postForm(t, c, srv.URL, "/numbers/new", url.Values{
		"country_code": {"JP"}, "phone_national": {"9044444444"}, "carrier": {"KDDI"},
		"expiry_date": {"2099-01-01"}, "auto_expiry_period": {"90"}, "keepalive_mode": {"keep"},
		"renewal_days_before": {"30"}, "reminder_interval_days": {"31"}, "csrf_token": {tok},
	})
	if code != 200 {
		t.Fatalf("超限间隔应回显表单 200, got %d", code)
	}
	if !strings.Contains(body, "重复提醒间隔不能大于提醒提前天数") {
		t.Error("应提示重复间隔不能大于提醒窗口")
	}
	if _, err := a.Numbers.ByPhone(1, "+819044444444"); err == nil {
		t.Error("超限间隔的号码不应保存")
	}
}

// TestHTTPDashboardShowsAllNumbers 回归：号码列表不分页，超过旧版每页 20 条时
// 也必须一次全部渲染，且页面不再出现分页控件。
func TestHTTPDashboardShowsAllNumbers(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	const total = 25
	for i := 1; i <= total; i++ {
		if _, err := a.Numbers.Create(&store.PhoneNumber{
			UserID: 1, PhoneNumber: fmt.Sprintf("+8190%08d", i), CountryCode: "JP",
			CountryName: "日本", Carrier: fmt.Sprintf("Carrier%02d", i),
			ExpiryDate: "2099-01-01", Status: "active", RenewalDaysBefore: 7,
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	resp, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(raw)
	for i := 1; i <= total; i++ {
		if !strings.Contains(html, fmt.Sprintf("Carrier%02d", i)) {
			t.Errorf("号码列表未展示第 %d 条（应一次展示全部 %d 条）", i, total)
		}
	}
	for _, pager := range []string{"下一页", "上一页", "page="} {
		if strings.Contains(html, pager) {
			t.Errorf("号码列表不应再有分页控件 %q", pager)
		}
	}
}

// TestHTTPMobileBackdropAndFlashOnce 驗證行動端遮罩點擊能正確關閉，
// 且 Flash 提示僅顯示一次（消費後即清除 Cookie，重整不會重複出現）。
func TestHTTPMobileBackdropAndFlashOnce(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	// 觸發一次會寫入 Flash 的操作（儲存系統設定）
	tok := getCSRF(t, c, srv.URL, "/admin")
	code, _ := postForm(t, c, srv.URL, "/admin/settings", url.Values{
		"site_name": {"SimKeeper"}, "log_retention_days": {"90"}, "csrf_token": {tok},
	})
	if code != 303 {
		t.Fatalf("儲存設定應 303, got %d", code)
	}

	readPage := func(path string) string {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}

	first := readPage("/admin")
	if !strings.Contains(first, "设置已保存") {
		t.Error("首次渲染應顯示 Flash 提示")
	}
	if !strings.Contains(first, "bd.addEventListener('click',function(){sb.classList.remove('open');bd.classList.remove('show')})") {
		t.Error("行動端點擊遮罩應移除 show 樣式")
	}
	second := readPage("/admin")
	if strings.Contains(second, "设置已保存") {
		t.Error("Flash 提示消費後不應在重整頁面時重複顯示")
	}
}

// TestHTTPExpiring7ConsistencyAndCSVNoKeepalive 驗證首頁與後台的「7 天內到期」
// 統計口徑一致（同時排除已終止與無需保號號碼），且 CSV 回導更新時保留 NoKeepalive。
func TestHTTPExpiring7ConsistencyAndCSVNoKeepalive(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	soon := time.Now().AddDate(0, 0, 3).Format("2006-01-02")

	// 1. 正常 3 天後到期（應計入）
	if _, err := a.Numbers.Create(&store.PhoneNumber{
		UserID: 1, PhoneNumber: "+819011111111", CountryCode: "JP", CountryName: "日本",
		Carrier: "KDDI", ExpiryDate: soon, Status: "active", RenewalDaysBefore: 7,
	}); err != nil {
		t.Fatal(err)
	}
	// 2. 已終止但到期日在 3 天內（不應計入）
	if _, err := a.Numbers.Create(&store.PhoneNumber{
		UserID: 1, PhoneNumber: "+819022222222", CountryCode: "JP", CountryName: "日本",
		Carrier: "KDDI", ExpiryDate: soon, Status: "inactive", RenewalDaysBefore: 7,
	}); err != nil {
		t.Fatal(err)
	}
	// 3. 無需保號但庫內到期日在 3 天內（不應計入，且 CSV 回導後仍保持無需保號）
	if _, err := a.Numbers.Create(&store.PhoneNumber{
		UserID: 1, PhoneNumber: "+819033333333", CountryCode: "JP", CountryName: "日本",
		Carrier: "KDDI", ExpiryDate: soon, Status: "active", NoKeepalive: true, RenewalDaysBefore: 7,
	}); err != nil {
		t.Fatal(err)
	}

	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	// 檢查首頁與後台渲染出的「7 天內到期」統計一致（皆應為 1），
	// 且首頁「使用中」應包含使用中的無需保號號碼（共 2 個）
	const wantCard = `<div class="stat-label">7 天内到期</div><div class="stat-num warn">1</div>`
	const wantActiveCard = `<div class="stat-label">使用中</div><div class="stat-num ok">2</div>`
	for _, path := range []string{"/", "/admin"} {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), wantCard) {
			t.Errorf("%s 的 7 天內到期統計不為 1（應排除已終止與無需保號號碼）", path)
		}
		if path == "/" && !strings.Contains(string(b), wantActiveCard) {
			t.Errorf("首頁「使用中」統計應包含使用中的無需保號號碼（want 2）")
		}
	}

	resp, err := c.Get(srv.URL + "/admin/export?format=csv")
	if err != nil {
		t.Fatal(err)
	}
	csvRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// CSV 回導後，+819033333333 必須保留 NoKeepalive = true
	tok := getCSRF(t, c, srv.URL, "/admin")
	code, body := postMultipart(t, c, srv.URL, "/admin/import", "numbers.csv", csvRaw,
		url.Values{"csrf_token": {tok}})
	if code != 303 {
		t.Fatalf("CSV 回導應 303, got %d %s", code, body)
	}
	n3, err := a.Numbers.ByPhone(1, "+819033333333")
	if err != nil {
		t.Fatal(err)
	}
	if !n3.NoKeepalive {
		t.Error("CSV 回導更新既有號碼後，不應遺失 NoKeepalive 標記")
	}
}

// TestHTTPForgotGenericRedirect 驗證 POST /forgot 僅送出單一 303 重定向，
// 無論信箱是否存在或未配置郵件皆不出現二次 WriteHeader。
func TestHTTPForgotGenericRedirect(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)

	tok := getCSRF(t, c, srv.URL, "/forgot")
	code, body := postForm(t, c, srv.URL, "/forgot", url.Values{
		"email": {"admin@test.local"}, "csrf_token": {tok},
	})
	if code != 303 {
		t.Fatalf("POST /forgot 應回傳 303, got %d %s", code, body)
	}
}

// TestHTTPTOTPRateLimitNotResetByPassword 回歸安全測試：啟用 TOTP 時，
// 正確密碼驗證不應提前清空 IP 限流計數，防止攻擊者重複送密碼重置計數以高速窮舉 TOTP；
// 同時驗證 SecureCookies 開啟時 SKTOTP 具備 Secure 旗標。
func TestHTTPTOTPRateLimitNotResetByPassword(t *testing.T) {
	a := integrationApp(t)
	a.SecureCookies = true
	mustAdmin(t, a)
	sec, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Users.SetTOTP(1, sec, true); err != nil {
		t.Fatal(err)
	}
	srv, c := newTestServer(t, a)

	// 直接使用原始請求（因 httptest 為 http，cookiejar 不會將 Secure Cookie 送回 http，
	// 我們手動攜帶並檢查 Cookie 的 Secure 旗標）
	resp, err := c.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	loginBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := csrfRe.FindSubmatch(loginBody)
	if m == nil {
		t.Fatal("缺少 CSRF token")
	}
	tok := string(m[1])
	var sessCookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "SKSESSION" {
			sessCookie = ck
		}
	}
	if sessCookie == nil || !sessCookie.Secure {
		t.Fatalf("SKSESSION 應帶 Secure 旗標: %+v", sessCookie)
	}

	doPost := func(path string, form url.Values, cookies ...*http.Cookie) (*http.Response, string) {
		req, err := http.NewRequest("POST", srv.URL+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, ck := range cookies {
			if ck != nil {
				req.AddCookie(ck)
			}
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}

	// 1. 送正確密碼取得 SKTOTP
	r1, _ := doPost("/login", url.Values{
		"username": {"admin"}, "password": {"password123"}, "csrf_token": {tok},
	}, sessCookie)
	var totpCookie *http.Cookie
	for _, ck := range r1.Cookies() {
		if ck.Name == "SKTOTP" {
			totpCookie = ck
		}
	}
	if totpCookie == nil || !totpCookie.Secure {
		t.Fatalf("SKTOTP 應存在並帶 Secure 旗標: %+v", totpCookie)
	}

	// 2. 猜錯 4 次 TOTP（確保使用的測試碼非當前有效碼，避免極低機率隨機撞碼）
	badCode := "000000"
	if auth.VerifyTOTP(sec, badCode) {
		badCode = "111111"
	}
	for i := 0; i < 4; i++ {
		r, _ := doPost("/login/totp", url.Values{"code": {badCode}, "csrf_token": {tok}}, sessCookie, totpCookie)
		if r.StatusCode != 401 {
			t.Fatalf("第 %d 次錯誤 TOTP 應 401, got %d", i+1, r.StatusCode)
		}
	}

	// 3. 嘗試再次以正確密碼 POST /login：不應重置 IP 失敗計數
	rAgain, _ := doPost("/login", url.Values{
		"username": {"admin"}, "password": {"password123"}, "csrf_token": {tok},
	}, sessCookie)
	if rAgain.StatusCode != 200 {
		t.Fatalf("累計 4 次失敗時尚可進入 TOTP 步驟, got %d", rAgain.StatusCode)
	}

	// 4. 再猜錯第 5 次，接著第 6 次必須被限流攔截
	_, _ = doPost("/login/totp", url.Values{"code": {badCode}, "csrf_token": {tok}}, sessCookie, totpCookie)
	rBlocked, bodyBlocked := doPost("/login/totp", url.Values{"code": {badCode}, "csrf_token": {tok}}, sessCookie, totpCookie)
	if rBlocked.StatusCode != 401 || !strings.Contains(bodyBlocked, "尝试次数过多") {
		t.Fatalf("累計滿 5 次後應觸發限流（不可被重複密碼提交重置）, got %d %q", rBlocked.StatusCode, bodyBlocked)
	}
}

// TestHTTPNoKeepaliveBackupRoundTrip 回歸：本系統匯出的 JSON 與 CSV 備份中，
// 「無需保號」號碼到期日為空，清庫後無論以 JSON 或 CSV 還原均須成功匯入而非被跳過。
func TestHTTPNoKeepaliveBackupRoundTrip(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	tok := getCSRF(t, c, srv.URL, "/numbers/new")
	code, body := postForm(t, c, srv.URL, "/numbers/new", url.Values{
		"country_code": {"JP"}, "phone_national": {"9055556666"}, "carrier": {"KDDI"},
		"keepalive_mode": {"none"}, "renewal_days_before": {"7"}, "csrf_token": {tok},
	})
	if code != 303 {
		t.Fatalf("建立無需保號號碼應 303, got %d %s", code, body)
	}

	getExport := func(format string) []byte {
		resp, err := c.Get(srv.URL + "/admin/export?format=" + format)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return b
	}
	jsonRaw := getExport("json")
	csvRaw := getExport("csv")

	for _, tc := range []struct {
		name, filename string
		data           []byte
	}{
		{"JSON", "backup.json", jsonRaw},
		{"CSV", "backup.csv", csvRaw},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.DB.Exec(`DELETE FROM phone_numbers`); err != nil {
				t.Fatal(err)
			}
			tok := getCSRF(t, c, srv.URL, "/admin")
			code, body := postMultipart(t, c, srv.URL, "/admin/import", tc.filename, tc.data,
				url.Values{"csrf_token": {tok}})
			if code != 303 {
				t.Fatalf("%s 匯入應 303, got %d %s", tc.name, code, body)
			}
			n, err := a.Numbers.ByPhone(1, "+819055556666")
			if err != nil {
				t.Fatalf("%s 還原後無需保號號碼不應被跳過: %v", tc.name, err)
			}
			if !n.NoKeepalive || n.ExpiryDate != "" {
				t.Errorf("%s 還原屬性不符: NoKeepalive=%v ExpiryDate=%q", tc.name, n.NoKeepalive, n.ExpiryDate)
			}
		})
	}
}

// TestHTTPLastKeepaliveExpiresTodayNotPast 回歸：依「我剛保過號」推算出的到期日剛好為今天時，
// DaysLeft == 0（未過期），儲存提示不應誤判為「已過期」。
func TestHTTPLastKeepaliveExpiresTodayNotPast(t *testing.T) {
	a := integrationApp(t)
	mustAdmin(t, a)
	srv, c := newTestServer(t, a)
	loginAdmin(t, c, srv.URL)

	last := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	tok := getCSRF(t, c, srv.URL, "/numbers/new")
	code, body := postForm(t, c, srv.URL, "/numbers/new", url.Values{
		"country_code": {"JP"}, "phone_national": {"9077778888"}, "carrier": {"KDDI"},
		"keepalive_mode": {"keep"}, "date_mode": {"last"},
		"last_keepalive_date": {last}, "auto_expiry_period": {"30"},
		"renewal_days_before": {"7"}, "csrf_token": {tok},
	})
	if code != 303 {
		t.Fatalf("建號應 303, got %d %s", code, body)
	}
	resp, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(html), "该号码已过期") {
		t.Error("推算到期日為今天時不應誤報已過期")
	}
}
