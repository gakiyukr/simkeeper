package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	// 默认排序：运营商名称（数字 < 字母 < 其他字符）：CSL < EE < 中国移动 < 中国联通
	got = filterSortNumbers(nums, "", "", "")
	if got[0].ID != 1 || got[1].ID != 2 || got[2].ID != 3 || got[3].ID != 4 {
		t.Errorf("默认运营商排序错误: %v", []int64{got[0].ID, got[1].ID, got[2].ID, got[3].ID})
	}
	// 显式 carrier 与空值同序
	if got = filterSortNumbers(nums, "", "", "carrier"); got[0].ID != 1 {
		t.Errorf("carrier 排序应与默认一致, got %d", got[0].ID)
	}
	// 到期升序：显式 expiry_asc 才按到期日（无需保号排最后）
	got = filterSortNumbers(nums, "", "", "expiry_asc")
	if got[0].ID != 2 || got[1].ID != 1 || got[2].ID != 3 || got[3].ID != 4 {
		t.Errorf("到期升序错误: %v", []int64{got[0].ID, got[1].ID, got[2].ID, got[3].ID})
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

// TestDashboardTemplateRenders 渲染烟测：首页表格为「运营商（旗+名）| 号码 | …」
// 列序时应完整渲染；旗帜按白名单输出，未知国家代码退化为无旗、仅运营商文字。
func TestDashboardTemplateRenders(t *testing.T) {
	tmpl, err := template.New("sk").Funcs(funcMap).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := &pageData{
		SiteName: "SimKeeper", Title: "号码管理", CSRFToken: "tok", Year: 2026,
		User: &store.User{Username: "tester", Role: "user"},
		Content: DashPage{
			DeviceNames: map[int64]string{3: "Pixel 8"},
			Numbers: []store.PhoneNumber{
				{ID: 7, PhoneNumber: "+819012345678", CountryCode: "JP", CountryName: "日本",
					Carrier: "KDDI", ExpiryDate: "2099-01-01", Status: "active",
					RenewalDaysBefore: 7, AutoExpiryPeriod: 90,
					Notes: "MESIM购买 主号", DeviceID: 3},
				{PhoneNumber: "+99900000000", CountryCode: "XX", CountryName: "未知",
					Carrier: "Mystery Telecom", ExpiryDate: "2099-01-01", Status: "active",
					RenewalDaysBefore: 7},
			},
			TotalAll: 2, Total: 2, Page: 1, Pages: 1,
		},
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "page_dashboard", data); err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	html := buf.String()

	// 列序：运营商表头在号码之前
	ih, jh := strings.Index(html, "<th>运营商</th>"), strings.Index(html, "<th>号码</th>")
	if ih < 0 || jh < 0 || jh < ih {
		t.Errorf("表头应为 运营商|号码 顺序, i=%d j=%d", ih, jh)
	}
	// 日本号码：旗在前、运营商文字在后
	iFlag := strings.Index(html, `/flags/jp.svg" alt="日本"`)
	iCarrier := strings.Index(html, "KDDI")
	if iFlag < 0 || iCarrier < 0 || iCarrier < iFlag {
		t.Errorf("日本国旗+KDDI 渲染/顺序错误, flag=%d carrier=%d", iFlag, iCarrier)
	}
	// 号码应按国家规则分组展示（JP → 2 4 4）
	if !strings.Contains(html, "81 90 1234 5678") {
		t.Error("手机号码未按国家规则分组渲染")
	}
	// 手动保号对话框两步式：选方式（s1）→ 红色确认按钮（s2）
	if !strings.Contains(html, "renew-d7") || !strings.Contains(html, "showModal()") {
		t.Error("已续费按钮应触发育动保号对话框")
	}
	if !strings.Contains(html, "从今天重新起算") || !strings.Contains(html, "在原到期日上顺延") {
		t.Error("对话框应包含两种到期日计算方式")
	}
	if !strings.Contains(html, `id="renew-s2-7" hidden`) {
		t.Error("确认步骤应默认隐藏")
	}
	if !strings.Contains(html, "确认，我已完成保号") || !strings.Contains(html, "btn-destructive") {
		t.Error("确认步骤应有红色确认按钮")
	}
	if strings.Count(html, `name="mode"`) != 1 {
		t.Errorf("每对话框应只有一个 mode 提交项, got %d", strings.Count(html, `name="mode"`))
	}

	// 备注与安装设备列
	if !strings.Contains(html, "MESIM购买 主号") {
		t.Error("备注应显示在运营商单元格")
	}
	if !strings.Contains(html, `<th>安装设备</th>`) || !strings.Contains(html, "Pixel 8") {
		t.Error("安装设备列应显示设备名")
	}
	if !strings.Contains(html, "dev-badge dev-c3") {
		t.Error("设备徽章应按 ID 稳定取色（ID 3 → c3）")
	}

	// 未知国家代码：不渲染旗帜，运营商文字仍在
	if strings.Contains(html, "/flags/xx.svg") {
		t.Error("未知代码不应渲染旗帜")
	}
	if !strings.Contains(html, "Mystery Telecom") {
		t.Error("未知国家的运营商文字不应丢失")
	}
}

// TestFlagRouteWhitelist 国旗路由：白名单内代码（含大小写归一）200，
// 未收录代码与错误后缀 404，%2F 编码的路径穿越也被白名单挡下。
func TestFlagRouteWhitelist(t *testing.T) {
	a := &App{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /flags/{code}", a.handleFlagSVG)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/flags/jp.svg", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("jp.svg 应 200 image/svg+xml, got %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/flags/JP.SVG", nil))
	if w.Code != 200 {
		t.Errorf("大小写归一后 JP.SVG 应 200, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/flags/zz.svg", nil))
	if w.Code != 404 {
		t.Errorf("未收录代码应 404, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/flags/jp.txt", nil))
	if w.Code != 404 {
		t.Errorf("非 .svg 后缀应 404, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/flags/..%2ftemplates%2f_layout.html", nil))
	if w.Code != 404 {
		t.Errorf("路径穿越应 404, got %d", w.Code)
	}
}

// TestNumberFormTemplateRenders 表单烟测：更多设置折叠区、号码类型选中态、
// eSIM 激活信息回显、编辑 eSIM 号码时自动展开、新增表单无状态选择器。
func TestNumberFormTemplateRenders(t *testing.T) {
	tmpl, err := template.New("sk").Funcs(funcMap).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	render := func(n *store.PhoneNumber) string {
		var buf strings.Builder
		data := &pageData{
			SiteName: "SimKeeper", Title: "编辑号码", CSRFToken: "tok", Year: 2026,
			User:    &store.User{Username: "tester", Role: "user"},
			Content: map[string]any{"Countries": Countries, "Carriers": Carriers, "Devices": []store.Device{{ID: 3, Name: "Pixel 8"}}, "Country": "JP", "N": n},
		}
		if err := tmpl.ExecuteTemplate(&buf, "page_number_form", data); err != nil {
			t.Fatalf("渲染失败: %v", err)
		}
		return buf.String()
	}

	esimHTML := render(&store.PhoneNumber{
		ID: 7, PhoneNumber: "+819012345678", CountryCode: "JP", CountryName: "日本",
		ExpiryDate: "2099-01-01", Status: "active", SimType: "esim", DeviceID: 3,
		LPAString: "LPA:1$rsp.example.com$ABCD", ConfirmCode: "654321",
	})
	if !strings.Contains(esimHTML, `<details class="more-settings" open>`) {
		t.Error("编辑 eSIM 号码时更多设置应自动展开")
	}
	if !strings.Contains(esimHTML, `name="sim_type" value="esim" checked`) {
		t.Error("eSIM 单选应选中")
	}
	if !strings.Contains(esimHTML, `value="LPA:1$rsp.example.com$ABCD"`) || !strings.Contains(esimHTML, `value="654321"`) {
		t.Error("LPA 与确认码应回显")
	}
	if !strings.Contains(esimHTML, `name="status"`) {
		t.Error("状态选择器应存在（更多设置内）")
	}
	if !strings.Contains(esimHTML, `<option value="3" selected>Pixel 8</option>`) {
		t.Error("已选设备应回显选中")
	}

	phyHTML := render(&store.PhoneNumber{
		ID: 8, PhoneNumber: "+8613800138000", CountryCode: "CN", CountryName: "中国",
		ExpiryDate: "2099-01-01", Status: "active",
	})
	if !strings.Contains(phyHTML, `name="sim_type" value="physical" checked`) {
		t.Error("实体卡单选应默认选中")
	}
	if strings.Contains(phyHTML, `more-settings" open`) {
		t.Error("非 eSIM 号码不应自动展开更多设置")
	}

	newHTML := render(nil)
	if strings.Contains(newHTML, `name="status"`) {
		t.Error("新增表单不应有状态选择器")
	}
	if !strings.Contains(newHTML, `name="sim_type" value="physical" checked`) {
		t.Error("新增表单应默认实体卡")
	}
}

// TestFormatPhoneGroups 号码阅读分组：常见国家规则、裸号（副卡）不重复剥区号、
// 完整副卡剥区号、短号/无数字原样、未收录国家按位数兜底、前缀推断。
func TestFormatPhoneGroups(t *testing.T) {
	cases := []struct{ cc, in, want string }{
		{"HK", "+85291234567", "+852 9123 4567"},
		{"CN", "+8613800138000", "+86 138 0013 8000"},
		{"US", "+14155552671", "+1 415 555 2671"},
		{"JP", "+819012345678", "+81 90 1234 5678"},
		{"GB", "+447123456789", "+44 7123 456789"},
		{"RU", "+79121234567", "+7 912 123 45 67"},
		{"FR", "+33612345678", "+33 6 12 34 56 78"},
		{"TW", "+886912345678", "+886 912 345 678"},
		{"HK", "61234567", "6123 4567"},               // 裸副卡：只分组本体
		{"HK", "+85261234567", "+852 6123 4567"},      // 副卡粘了完整区号：剥掉不重复
		{"HK", "123", "123"},                          // 短号原样
		{"CN", "N/A", "N/A"},                          // 无数字原样
		{"DE", "+4915123456789", "+49 151 2345 6789"}, // 未收录国家：11 位兜底 3 4 4
	}
	for _, c := range cases {
		if got := formatPhoneCC(c.cc, c.in); got != c.want {
			t.Errorf("formatPhoneCC(%q, %q) = %q, want %q", c.cc, c.in, got, c.want)
		}
	}
	if got := formatPhoneAuto("+85291234567"); got != "+852 9123 4567" {
		t.Errorf("formatPhoneAuto 按前缀推断失败: %q", got)
	}
	if got := formatPhoneAuto("61234567"); got != "61234567" {
		t.Errorf("无 + 前缀应原样: %q", got)
	}
}

// TestCarrierCompare 运营商名称排序规则：数字 < 字母 < 其他字符（中文），
// 字母忽略大小写、大写在前，空名排最后。
func TestCarrierCompare(t *testing.T) {
	want := []string{"12", "3HK", "CSL", "csl", "EE", "NTT", "NTT docomo", "中国移动", "中国联通", ""}
	for i := 0; i < len(want)-1; i++ {
		for j := i + 1; j < len(want); j++ {
			if carrierCompare(want[i], want[j]) >= 0 {
				t.Errorf("carrierCompare(%q, %q) 应 < 0（顺序: %v）", want[i], want[j], want)
			}
			if carrierCompare(want[j], want[i]) <= 0 {
				t.Errorf("carrierCompare(%q, %q) 应 > 0", want[j], want[i])
			}
		}
	}
	for _, w := range want {
		if carrierCompare(w, w) != 0 {
			t.Errorf("carrierCompare(%q, %q) 应 = 0", w, w)
		}
	}
}

// TestSecureHeaders 所有响应都带基础安全头（防点击劫持 / MIME 嗅探）。
func TestSecureHeaders(t *testing.T) {
	var called bool
	h := (&App{}).secureHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !called {
		t.Fatal("next 未被执行")
	}
	for k, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// TestCSVFormulaInjection 公式注入：= + - @ 开头前置单引号，Excel 按文本展示；
// 普通数字不加前缀。
func TestCSVFormulaInjection(t *testing.T) {
	for _, dangerous := range []string{"=cmd()", "+8613800138000", "-1+1", "@x"} {
		got := csvRow(dangerous)
		if !strings.HasPrefix(got, "'") {
			t.Errorf("%q 应前置单引号防公式注入, got %q", dangerous, got)
		}
	}
	if got := csvRow("13800138000"); strings.Contains(got, "'") {
		t.Errorf("普通数字不应加前缀, got %q", got)
	}
}

// TestBuildNumbersJSON JSON 导出：SIMHub 兼容格式（formatVersion 5）——
// LPA/确认码、旗帜 emoji、周期、副卡与标签映射正确。
func TestBuildNumbersJSON(t *testing.T) {
	now := time.Now()
	nums := []store.PhoneNumber{
		{ID: 7, PhoneNumber: "+819012345678", CountryCode: "JP", CountryName: "日本",
			Carrier: "KDDI", ExpiryDate: "2099-01-01", Status: "active", SimType: "esim",
			LPAString: "LPA:1$rsp.example.com$ABCD", ConfirmCode: "654321",
			SecondaryNumbers: "+819012345679", RechargeAmount: 3.5, RechargeCurrency: "JPY",
			AutoExpiryEnabled: true, AutoExpiryPeriod: 90,
			CreatedAt: "2026-10-05 10:00:00", UpdatedAt: "2026-10-05 11:00:00"},
		{ID: 8, PhoneNumber: "+8613800138000", CountryCode: "CN", CountryName: "中国",
			ExpiryDate: "2099-01-01", Status: "active", NoKeepalive: true},
	}
	b, err := buildNumbersJSON(now, nums)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		FormatVersion int    `json:"formatVersion"`
		ExportedAt    string `json:"exportedAt"`
		Services      []any  `json:"services"`
		ESIMCards     []struct {
			ID               string   `json:"id"`
			PhoneNumber      string   `json:"phoneNumber"`
			ActivationCode   string   `json:"activationCode"`
			SMDPAddress      string   `json:"smdpAddress"`
			ConfirmationCode string   `json:"confirmationCode"`
			CountryCode      string   `json:"countryCode"`
			Flag             string   `json:"flag"`
			Price            string   `json:"price"`
			RenewDays        int      `json:"renewDays"`
			RenewalUnit      string   `json:"renewalUnit"`
			IsLongTerm       bool     `json:"isLongTerm"`
			Tags             []string `json:"tags"`
			SecondaryPhone   string   `json:"secondaryPhoneNumber"`
			SecondaryOn      bool     `json:"secondaryPhoneNumberEnabled"`
			ExpiryDate       string   `json:"expiryDate"`
			UpdatedAt        string   `json:"updatedAt"`
		} `json:"esimCards"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("导出 JSON 不合法: %v", err)
	}
	if doc.FormatVersion != 5 || len(doc.ESIMCards) != 2 || len(doc.Services) != 0 {
		t.Fatalf("导出元数据不符: v=%d n=%d services=%v", doc.FormatVersion, len(doc.ESIMCards), doc.Services)
	}
	c := doc.ESIMCards[0]
	if c.ID != "00000000-0000-0000-0000-000000000007" {
		t.Errorf("id 应为稳定 UUID 形态: %q", c.ID)
	}
	if c.ActivationCode != "LPA:1$rsp.example.com$ABCD" || c.SMDPAddress != "rsp.example.com" || c.ConfirmationCode != "654321" {
		t.Errorf("eSIM 激活信息映射不一致: %+v", c)
	}
	if c.Flag != "🇯🇵" || c.Price != "3.50 JPY" || c.RenewDays != 90 || c.RenewalUnit != "days" || !c.IsLongTerm {
		t.Errorf("展示字段映射不一致: flag=%q price=%q renew=%d unit=%q long=%v", c.Flag, c.Price, c.RenewDays, c.RenewalUnit, c.IsLongTerm)
	}
	if c.SecondaryPhone != "+819012345679" || !c.SecondaryOn {
		t.Errorf("副卡映射不一致: %q %v", c.SecondaryPhone, c.SecondaryOn)
	}
	if _, err := time.Parse(time.RFC3339, c.ExpiryDate); err != nil {
		t.Errorf("expiryDate 应为 RFC3339: %q", c.ExpiryDate)
	}
	if _, err := time.Parse(time.RFC3339, c.UpdatedAt); err != nil {
		t.Errorf("updatedAt 应为 RFC3339: %q", c.UpdatedAt)
	}
	plain := doc.ESIMCards[1]
	if plain.IsLongTerm || plain.Flag != "🇨🇳" {
		t.Errorf("无需保号卡映射不一致: long=%v flag=%q", plain.IsLongTerm, plain.Flag)
	}
}

// TestAdminHomeTemplateRenders 管理后台合并页：导出按钮、系统设置表单、统计卡。
func TestAdminHomeTemplateRenders(t *testing.T) {
	tmpl, err := template.New("sk").Funcs(funcMap).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := &pageData{
		SiteName: "SimKeeper", Title: "管理后台", CSRFToken: "tok", Year: 2026,
		User: &store.User{Username: "admin", Role: "admin"},
		Content: AdminPage{
			Stats:         AdminStats{Numbers: 3, Expiring7: 1},
			SiteName:      "SimKeeper",
			RetentionDays: 90,
		},
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "page_admin_home", data); err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	html := buf.String()
	for _, probe := range []string{
		`/admin/export?format=csv`, `/admin/export?format=json`,
		`action="/admin/settings"`, `name="log_retention_days"`,
		`/admin/cron`, `活跃号码`,
	} {
		if !strings.Contains(html, probe) {
			t.Errorf("管理后台页缺少 %q", probe)
		}
	}
}

// TestDevicesTemplateRenders 设备管理页：添加表单、设备卡、名下 eSIM 列表与空态。
func TestDevicesTemplateRenders(t *testing.T) {
	tmpl, err := template.New("sk").Funcs(funcMap).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	render := func(p DevicesPage) string {
		var buf strings.Builder
		data := &pageData{
			SiteName: "SimKeeper", Title: "设备管理", CSRFToken: "tok", Year: 2026,
			User:    &store.User{Username: "admin"},
			Content: p,
		}
		if err := tmpl.ExecuteTemplate(&buf, "page_devices", data); err != nil {
			t.Fatalf("渲染失败: %v", err)
		}
		return buf.String()
	}

	html := render(DevicesPage{Devices: []DeviceView{{
		Device:  store.Device{ID: 3, Name: "iPhone 15", DeviceType: "phone", Notes: "主力机"},
		Numbers: []store.PhoneNumber{{PhoneNumber: "+819012345678", CountryCode: "JP", Carrier: "KDDI", ExpiryDate: "2099-01-01", Status: "active"}},
	}}})
	for _, probe := range []string{"iPhone 15", "主力机", "手机", "/devices/3/delete", "/devices/3/edit", "81 90 1234 5678", "KDDI", "device-edit-3", "showModal()"} {
		if !strings.Contains(html, probe) {
			t.Errorf("设备页缺少 %q", probe)
		}
	}
	if strings.Contains(html, "还没有设备") {
		t.Error("有设备时不应显示空态")
	}
	empty := render(DevicesPage{})
	if !strings.Contains(empty, "还没有设备") || !strings.Contains(empty, "添加设备") {
		t.Error("空态应提示添加设备")
	}
}
