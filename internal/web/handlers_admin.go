package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"simkeeper/internal/cronjob"
	"simkeeper/internal/store"
)

// AdminStats 管理后台统计卡。
type AdminStats struct {
	Numbers     int
	Expiring7   int
	SentTotal   int
	FailedTotal int
	LastCronRun string
}

// AdminPage 管理后台页数据：统计 + 系统设置 + 最近通知（合并为一页）。
type AdminPage struct {
	Stats         AdminStats
	SiteName      string
	RetentionDays int
	Version       string
	RecentNotifs  []store.Notification
}

// HandleAdminHome 管理后台：统计、定时任务、系统设置与最近通知合并一页。
func (a *App) HandleAdminHome(w http.ResponseWriter, r *http.Request) {
	d := a.baseData(r, "管理后台")
	d.ActiveNav = "admin"
	stats := AdminStats{}
	today := time.Now()

	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM phone_numbers WHERE status='active'`).Scan(&stats.Numbers)
	_ = a.DB.QueryRow(
		`SELECT COUNT(*) FROM phone_numbers WHERE status='active' AND expiry_date >= ? AND expiry_date <= ?`,
		today.Format(dateLayout), today.AddDate(0, 0, 7).Format(dateLayout),
	).Scan(&stats.Expiring7)
	_ = a.DB.QueryRow(`SELECT setting_value FROM system_settings WHERE setting_key='total_sent'`).Scan(&stats.SentTotal)
	_ = a.DB.QueryRow(`SELECT setting_value FROM system_settings WHERE setting_key='last_cron_run'`).Scan(&stats.LastCronRun)
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE status='failed'`).Scan(&stats.FailedTotal)
	recent, _, _ := a.Notify.ListForAdmin(1, 8, "")

	d.Content = AdminPage{
		Stats:         stats,
		SiteName:      getStr(a, "site_name", "SimKeeper"),
		RetentionDays: a.Settings.GetInt("log_retention_days", 90),
		Version:       a.Version,
		RecentNotifs:  recent,
	}
	a.render(w, http.StatusOK, "page_admin_home", d)
}

// HandleAdminSettings 保存系统设置（POST + CSRF）；设置表单展示在管理后台首页，
// 旧地址 /admin/settings 已重定向到 /admin。
func (a *App) HandleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusMovedPermanently)
		return
	}
	siteName := strings.TrimSpace(r.PostFormValue("site_name"))
	retention := atoiDefault(r.PostFormValue("log_retention_days"), 90)
	if siteName == "" {
		siteName = "SimKeeper"
	}
	if retention < 1 {
		retention = 90
	}
	sets := [][2]string{
		{"site_name", siteName},
		{"log_retention_days", strconv.Itoa(retention)},
	}
	for _, kv := range sets {
		if err := a.Settings.Set(kv[0], kv[1]); err != nil {
			a.setFlash(w, "保存失败："+kv[0], true)
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
	}
	a.setFlash(w, "设置已保存", false)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// numberExport SIMHub 兼容的卡片结构（formatVersion 5），字段名与其备份文件一致，
// 导出后可直接导入该类应用。无法映射的专有字段留空。
type numberExport struct {
	ActivationCode              string   `json:"activationCode"`
	ActivationDate              string   `json:"activationDate"`
	AutoTopUpEnabled            bool     `json:"autoTopUpEnabled"`
	CardBackgroundAssetName     string   `json:"cardBackgroundAssetName"`
	CardColorHex                string   `json:"cardColorHex"`
	Carrier                     string   `json:"carrier"`
	CarrierAppIconURLString     string   `json:"carrierAppIconURLString"`
	CarrierAppName              string   `json:"carrierAppName"`
	CarrierAppStoreURLString    string   `json:"carrierAppStoreURLString"`
	ConfirmationCode            string   `json:"confirmationCode"`
	CountryCode                 string   `json:"countryCode"`
	CountryName                 string   `json:"countryName"`
	CreatedAt                   string   `json:"createdAt"`
	CurrencyCode                string   `json:"currencyCode"`
	CurrentBalance              string   `json:"currentBalance"`
	CurrentBalanceMinorUnits    int      `json:"currentBalanceMinorUnits"`
	CustomPrompt                string   `json:"customPrompt"`
	CyclePaymentMinorUnits      int      `json:"cyclePaymentMinorUnits"`
	EID                         string   `json:"eid"`
	ExpiryDate                  string   `json:"expiryDate"`
	Flag                        string   `json:"flag"`
	ID                          string   `json:"id"`
	IsLongTerm                  bool     `json:"isLongTerm"`
	KeepAliveModeRaw            string   `json:"keepAliveModeRaw"`
	OrderIndex                  int      `json:"orderIndex"`
	PhoneNumber                 string   `json:"phoneNumber"`
	Plan                        string   `json:"plan"`
	Price                       string   `json:"price"`
	RenewDays                   int      `json:"renewDays"`
	RenewalIntervalValue        int      `json:"renewalIntervalValue"`
	RenewalUnit                 string   `json:"renewalUnit"`
	SecondaryPhoneNumber        string   `json:"secondaryPhoneNumber"`
	SecondaryPhoneNumberEnabled bool     `json:"secondaryPhoneNumberEnabled"`
	SMDPAddress                 string   `json:"smdpAddress"`
	Tags                        []string `json:"tags"`
	TopUpCycleUnit              string   `json:"topUpCycleUnit"`
	TransactionNotes            string   `json:"transactionNotes"`
	UpdatedAt                   string   `json:"updatedAt"`
	WebsiteURL                  string   `json:"websiteURL"`
}

// flagEmoji 国家代码 → 旗帜 emoji（SIMHub 导出格式用；应用内展示走 SVG 国旗）。
func flagEmoji(code string) string {
	if len(code) != 2 {
		return ""
	}
	rs := make([]rune, 2)
	for i := 0; i < 2; i++ {
		c := rune(strings.ToUpper(code)[i])
		if c < 'A' || c > 'Z' {
			return ""
		}
		rs[i] = rune(0x1F1E6 + int(c-'A'))
	}
	return string(rs)
}

// rfc3339 把库内时间文本转成 RFC3339 UTC（SIMHub 格式）；解析失败返回空串。
func rfc3339(s string) string {
	for _, layout := range []string{time.DateTime, dateLayout} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// uuidFromID 由数据库自增 ID 生成稳定的大写 UUID 形态标识
// （SIMHub 的 id 是字符串；确定性派生保证重复导出内容一致）。
func uuidFromID(id int64) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012X", id)
}

// smdpFromLPA 从 LPA:1$<smdp>$<matching-id> 提取 SM-DP+ 地址。
func smdpFromLPA(lpa string) string {
	parts := strings.Split(lpa, "$")
	if len(parts) >= 3 && strings.EqualFold(parts[0], "LPA:1") {
		return parts[1]
	}
	return ""
}

// buildNumbersJSON 把号码列表序列化为 SIMHub 兼容备份（formatVersion 5）：
// 含 eSIM LPA/确认码，导出文件请妥善保管。副卡多条时合并进单字段（该格式每卡只有一个副卡槽）。
func buildNumbersJSON(now time.Time, numbers []store.PhoneNumber) ([]byte, error) {
	cards := make([]numberExport, 0, len(numbers))
	for i, n := range numbers {
		tags := []string{"保号卡"}
		if n.NoKeepalive {
			tags = []string{"无需保号"}
		}
		if n.SimType == "esim" {
			tags = append(tags, "eSIM")
		}
		var secondary []string
		for _, line := range strings.Split(n.SecondaryNumbers, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				secondary = append(secondary, line)
			}
		}
		price := ""
		if n.RechargeAmount > 0 {
			price = strconv.FormatFloat(n.RechargeAmount, 'f', 2, 64) + " " + n.RechargeCurrency
		}
		cards = append(cards, numberExport{
			ActivationCode:              n.LPAString,
			Carrier:                     n.Carrier,
			CarrierAppName:              n.Carrier,
			ConfirmationCode:            n.ConfirmCode,
			CountryCode:                 n.CountryCode,
			CountryName:                 n.CountryName,
			CreatedAt:                   rfc3339(n.CreatedAt),
			CurrencyCode:                n.RechargeCurrency,
			CyclePaymentMinorUnits:      int(n.RechargeAmount*100 + 0.5),
			ExpiryDate:                  rfc3339(n.ExpiryDate),
			Flag:                        flagEmoji(n.CountryCode),
			ID:                          uuidFromID(n.ID),
			IsLongTerm:                  !n.NoKeepalive,
			OrderIndex:                  i,
			PhoneNumber:                 formatPhoneCC(n.CountryCode, n.PhoneNumber),
			Plan:                        n.PlanName,
			Price:                       price,
			RenewDays:                   n.AutoExpiryPeriod,
			RenewalIntervalValue:        n.AutoExpiryPeriod,
			RenewalUnit:                 "days",
			SecondaryPhoneNumber:        strings.Join(secondary, ", "),
			SecondaryPhoneNumberEnabled: len(secondary) > 0,
			SMDPAddress:                 smdpFromLPA(n.LPAString),
			Tags:                        tags,
			TransactionNotes:            n.Notes,
			UpdatedAt:                   rfc3339(n.UpdatedAt),
		})
	}
	return json.MarshalIndent(struct {
		ESIMCards     []numberExport `json:"esimCards"`
		ExportedAt    string         `json:"exportedAt"`
		FormatVersion int            `json:"formatVersion"`
		Services      []any          `json:"services"`
	}{
		ESIMCards:     cards,
		ExportedAt:    now.UTC().Format(time.RFC3339),
		FormatVersion: 5,
		Services:      []any{},
	}, "", "  ")
}

// HandleAdminExport 导出号码数据：?format=csv（表格用，含 BOM）或
// ?format=json（完整备份）。
func (a *App) HandleAdminExport(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	numbers, _, err := a.Numbers.ListForUser(u.ID, 1, 1000000)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	name := "simkeeper-numbers-" + now.Format("20060102")
	switch r.URL.Query().Get("format") {
	case "channels":
		cfg, err := a.Notify.ConfigForUser(u.ID)
		if err != nil {
			http.Error(w, "内部错误", http.StatusInternalServerError)
			return
		}
		b, err := buildChannelsJSON(now, cfg)
		if err != nil {
			http.Error(w, "内部错误", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=simkeeper-channels-"+now.Format("20060102")+".json")
		_, _ = w.Write(b)
	case "json":
		b, err := buildNumbersJSON(now, numbers)
		if err != nil {
			http.Error(w, "内部错误", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename="+name+".json")
		_, _ = w.Write(b)
	default: // csv
		var b strings.Builder
		// UTF-8 BOM：让 Excel 正确识别中文
		b.WriteString("\xef\xbb\xbf")
		b.WriteString("号码,国家代码,国家,运营商,到期日,剩余天数,状态,充值金额,币种\n")
		for _, n := range numbers {
			b.WriteString(csvRow(n.PhoneNumber, n.CountryCode, n.CountryName, n.Carrier,
				n.ExpiryDate, strconv.Itoa(n.DaysLeft(now)), n.Status,
				strconv.FormatFloat(n.RechargeAmount, 'f', -1, 64), n.RechargeCurrency))
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename="+name+".csv")
		_, _ = w.Write([]byte(b.String()))
	}
}

// HandleAdminCron 手动触发一次定时任务（POST + CSRF）。
// 修复 PHP 版 GET 直接触发 cron 的问题。
func (a *App) HandleAdminCron(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}
	jobs := cronjob.New(a.DB)
	sent, failed, lines := jobs.RunOnce(time.Now())
	a.setFlash(w, fmt.Sprintf("任务完成：成功 %d，失败 %d（详见服务端日志）", sent, failed), failed > 0 && sent == 0)
	_ = lines
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// getStr 读取设置的便捷封装。
func getStr(a *App, key, fallback string) string {
	v, err := a.Settings.Get(key)
	if err != nil || v == "" {
		return fallback
	}
	return v
}
