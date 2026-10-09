// handlers_import.go 数据导入：自动识别三种格式并导入。
//   - SIMHub 兼容号码 JSON（esimCards；本程序导出的 JSON 即此格式，往返无损）
//   - 号码 CSV（本程序导出的列格式；字段少于 JSON，匹配更新）
//   - 渠道配置 JSON（本程序导出；整份替换当前渠道配置，含明文凭据）
package web

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"simkeeper/internal/store"
)

// channelConfigFile 渠道配置导出/导入的文件结构。
// 凭据以明文写入导出文件（迁移用途），文件本身需妥善保管。
type channelConfigFile struct {
	ExportedAt         string `json:"exported_at"`
	Note               string `json:"_note"`
	NotificationConfig struct {
		Email struct {
			Enabled    bool   `json:"enabled"`
			SMTPHost   string `json:"smtp_host"`
			SMTPPort   int    `json:"smtp_port"`
			SMTPSecure string `json:"smtp_secure"`
			Username   string `json:"username"`
			Password   string `json:"password"`
			FromEmail  string `json:"from_email"`
			FromName   string `json:"from_name"`
			ToEmail    string `json:"to_email"`
		} `json:"email"`
		Telegram struct {
			Enabled  bool   `json:"enabled"`
			BotToken string `json:"bot_token"`
			ChatID   string `json:"chat_id"`
		} `json:"telegram"`
		WxPusher struct {
			Enabled  bool   `json:"enabled"`
			AppToken string `json:"app_token"`
			UID      string `json:"uid"`
		} `json:"wxpusher"`
		Feishu struct {
			Enabled bool   `json:"enabled"`
			Webhook string `json:"webhook"`
			Secret  string `json:"secret"`
		} `json:"feishu"`
		DingTalk struct {
			Enabled bool   `json:"enabled"`
			Webhook string `json:"webhook"`
			Secret  string `json:"secret"`
		} `json:"dingtalk"`
		TGCall struct {
			Enabled bool   `json:"enabled"`
			APIID   int    `json:"api_id"`
			APIHash string `json:"api_hash"`
			Phone   string `json:"phone"`
			Session string `json:"session"`
			Target  string `json:"target"`
		} `json:"tgcall"`
	} `json:"notification_config"`
}

// buildChannelsJSON 导出用户的渠道配置（凭据解密后明文写入，迁移用途）。
func buildChannelsJSON(now time.Time, cfg *store.NotifyConfig) ([]byte, error) {
	var f channelConfigFile
	f.ExportedAt = now.UTC().Format(time.RFC3339)
	f.Note = "含明文渠道凭据，请妥善保管；导入将整份替换目标系统的渠道配置"
	if cfg != nil {
		f.NotificationConfig.Email.Enabled = cfg.EmailEnabled
		f.NotificationConfig.Email.SMTPHost = cfg.EmailSMTPHost
		f.NotificationConfig.Email.SMTPPort = cfg.EmailSMTPPort
		f.NotificationConfig.Email.SMTPSecure = cfg.EmailSMTPSecure
		f.NotificationConfig.Email.Username = cfg.EmailSMTPUsername
		f.NotificationConfig.Email.Password = cfg.EmailPassword
		f.NotificationConfig.Email.FromEmail = cfg.EmailFromEmail
		f.NotificationConfig.Email.FromName = cfg.EmailFromName
		f.NotificationConfig.Email.ToEmail = cfg.EmailToEmail
		f.NotificationConfig.Telegram.Enabled = cfg.TelegramEnabled
		f.NotificationConfig.Telegram.BotToken = cfg.TelegramBotToken
		f.NotificationConfig.Telegram.ChatID = cfg.TelegramChatID
		f.NotificationConfig.WxPusher.Enabled = cfg.WxPusherEnabled
		f.NotificationConfig.WxPusher.AppToken = cfg.WxPusherAppToken
		f.NotificationConfig.WxPusher.UID = cfg.WxPusherUID
		f.NotificationConfig.Feishu.Enabled = cfg.FeishuEnabled
		f.NotificationConfig.Feishu.Webhook = cfg.FeishuWebhook
		f.NotificationConfig.Feishu.Secret = cfg.FeishuSecret
		f.NotificationConfig.DingTalk.Enabled = cfg.DingTalkEnabled
		f.NotificationConfig.DingTalk.Webhook = cfg.DingTalkWebhook
		f.NotificationConfig.DingTalk.Secret = cfg.DingTalkSecret
		f.NotificationConfig.TGCall.Enabled = cfg.TGCallEnabled
		f.NotificationConfig.TGCall.APIID = cfg.TGCallAPIID
		f.NotificationConfig.TGCall.APIHash = cfg.TGCallAPIHash
		f.NotificationConfig.TGCall.Phone = cfg.TGCallPhone
		f.NotificationConfig.TGCall.Session = cfg.TGCallSession
		f.NotificationConfig.TGCall.Target = cfg.TGCallTarget
	}
	return json.MarshalIndent(f, "", "  ")
}

// importChannelsFromJSON 整份替换用户的渠道配置（SaveConfig 会重新加密凭据）。
func (a *App) importChannelsFromJSON(userID int64, raw []byte) error {
	var f channelConfigFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	c := &store.NotifyConfig{UserID: userID}
	e := f.NotificationConfig.Email
	c.EmailEnabled = e.Enabled
	c.EmailSMTPHost = strings.TrimSpace(e.SMTPHost)
	c.EmailSMTPPort = e.SMTPPort
	c.EmailSMTPSecure = e.SMTPSecure
	c.EmailSMTPUsername = strings.TrimSpace(e.Username)
	c.EmailPassword = e.Password
	c.EmailFromEmail = strings.TrimSpace(e.FromEmail)
	c.EmailFromName = strings.TrimSpace(e.FromName)
	c.EmailToEmail = strings.TrimSpace(e.ToEmail)
	c.TelegramEnabled = f.NotificationConfig.Telegram.Enabled
	c.TelegramBotToken = strings.TrimSpace(f.NotificationConfig.Telegram.BotToken)
	c.TelegramChatID = strings.TrimSpace(f.NotificationConfig.Telegram.ChatID)
	c.WxPusherEnabled = f.NotificationConfig.WxPusher.Enabled
	c.WxPusherAppToken = strings.TrimSpace(f.NotificationConfig.WxPusher.AppToken)
	c.WxPusherUID = strings.TrimSpace(f.NotificationConfig.WxPusher.UID)
	c.FeishuEnabled = f.NotificationConfig.Feishu.Enabled
	c.FeishuWebhook = strings.TrimSpace(f.NotificationConfig.Feishu.Webhook)
	c.FeishuSecret = strings.TrimSpace(f.NotificationConfig.Feishu.Secret)
	c.DingTalkEnabled = f.NotificationConfig.DingTalk.Enabled
	c.DingTalkWebhook = strings.TrimSpace(f.NotificationConfig.DingTalk.Webhook)
	c.DingTalkSecret = strings.TrimSpace(f.NotificationConfig.DingTalk.Secret)
	c.TGCallEnabled = f.NotificationConfig.TGCall.Enabled
	c.TGCallAPIID = f.NotificationConfig.TGCall.APIID
	c.TGCallAPIHash = strings.TrimSpace(f.NotificationConfig.TGCall.APIHash)
	c.TGCallPhone = strings.TrimSpace(f.NotificationConfig.TGCall.Phone)
	c.TGCallSession = strings.TrimSpace(f.NotificationConfig.TGCall.Session)
	c.TGCallTarget = strings.TrimSpace(f.NotificationConfig.TGCall.Target)
	return a.Notify.SaveConfig(c)
}

// simhubCard SIMHub 兼容卡片（导入解析；与导出结构对应，专有字段忽略）。
type simhubCard struct {
	PhoneNumber          string `json:"phoneNumber"`
	CountryCode          string `json:"countryCode"`
	CountryName          string `json:"countryName"`
	Carrier              string `json:"carrier"`
	ExpiryDate           string `json:"expiryDate"`
	ActivationCode       string `json:"activationCode"`
	ConfirmationCode     string `json:"confirmationCode"`
	Plan                 string `json:"plan"`
	TransactionNotes     string `json:"transactionNotes"`
	RenewDays            int    `json:"renewDays"`
	IsLongTerm           bool   `json:"isLongTerm"`
	SecondaryPhoneNumber string `json:"secondaryPhoneNumber"`
}

// simhubCardToNumber 把 SIMHub 卡片映射为号码；返回 (号码, 跳过原因)。
// 跳过原因为空表示可导入。更新路径只覆盖文件携带的字段，设备归属与
// 提醒窗口保留库内现值（SIMHub 无此概念）。
func simhubCardToNumber(c simhubCard) (store.PhoneNumber, string) {
	var n store.PhoneNumber
	phone := normalizePhone(c.PhoneNumber)
	if phone == "" || !phoneRe.MatchString(phone) {
		return n, "号码格式无效"
	}
	cc := strings.ToUpper(strings.TrimSpace(c.CountryCode))
	name := countryNameByCode(cc)
	if name == "" {
		return n, "国家代码未收录"
	}
	expiry := ""
	if t, err := time.Parse(time.RFC3339, c.ExpiryDate); err == nil {
		expiry = t.Local().Format(dateLayout)
	}
	if expiry == "" {
		return n, "到期日缺失或格式无效"
	}
	n.PhoneNumber = phone
	n.CountryCode = cc
	n.CountryName = name
	n.Carrier = strings.TrimSpace(c.Carrier)
	n.ExpiryDate = expiry
	n.Status = "active"
	n.RenewalDaysBefore = 7
	n.SimType = "physical"
	if code := strings.Join(strings.Fields(c.ActivationCode), ""); strings.HasPrefix(strings.ToUpper(code), "LPA:") {
		n.SimType = "esim"
		n.LPAString = code
		n.ConfirmCode = strings.TrimSpace(c.ConfirmationCode)
	}
	n.PlanName = strings.TrimSpace(c.Plan)
	n.Notes = strings.TrimSpace(c.TransactionNotes)
	n.NoKeepalive = !c.IsLongTerm
	n.AutoExpiryEnabled = c.RenewDays > 0
	n.AutoExpiryPeriod = c.RenewDays
	if s := strings.TrimSpace(c.SecondaryPhoneNumber); s != "" {
		n.SecondaryNumbers = s
	}
	return n, ""
}

// importNumbersFromSIMHub 导入 SIMHub 兼容 JSON：按号码匹配，存在则更新、
// 不存在则新建；更新时保留设备归属与提醒窗口现值。
func (a *App) importNumbersFromSIMHub(userID int64, raw []byte) (created, updated, skipped int, err error) {
	var doc struct {
		ESIMCards []simhubCard `json:"esimCards"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		return 0, 0, 0, err
	}
	for _, c := range doc.ESIMCards {
		n, reason := simhubCardToNumber(c)
		if reason != "" {
			skipped++
			continue
		}
		existing, e := a.Numbers.ByPhone(userID, n.PhoneNumber)
		if e != nil {
			n.UserID = userID
			if _, e = a.Numbers.Create(&n); e != nil {
				skipped++
				continue
			}
			created++
			continue
		}
		n.ID, n.UserID, n.DeviceID = existing.ID, existing.UserID, existing.DeviceID
		n.RenewalDaysBefore = existing.RenewalDaysBefore
		n.CreatedAt = existing.CreatedAt
		if e = a.Numbers.Update(&n); e != nil {
			skipped++
			continue
		}
		updated++
	}
	return created, updated, skipped, nil
}

// importNumbersFromCSV 导出 CSV 的回导：按号码匹配，存在则更新、不存在则新建。
func (a *App) importNumbersFromCSV(userID int64, raw []byte) (created, updated, skipped int, err error) {
	text := strings.TrimPrefix(string(raw), "\xef\xbb\xbf")
	rows, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		return 0, 0, 0, err
	}
	if len(rows) < 1 {
		return 0, 0, 0, err
	}
	idx := map[string]int{}
	for i, h := range rows[0] {
		idx[strings.TrimSpace(h)] = i
	}
	for _, row := range rows[1:] {
		at := func(col string) string {
			if i, ok := idx[col]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		phone := normalizePhone(at("号码"))
		if phone == "" || !phoneRe.MatchString(phone) {
			skipped++
			continue
		}
		cc := strings.ToUpper(at("国家代码"))
		name := countryNameByCode(cc)
		if name == "" {
			skipped++
			continue
		}
		expiry := at("到期日")
		if _, perr := time.ParseInLocation(dateLayout, expiry, time.Local); perr != nil {
			skipped++
			continue
		}
		n := store.PhoneNumber{
			PhoneNumber: phone, CountryCode: cc, CountryName: name,
			Carrier: at("运营商"), ExpiryDate: expiry,
			Status:            at("状态"),
			RenewalDaysBefore: 7, SimType: "physical",
		}
		if n.Status != "inactive" {
			n.Status = "active"
		}
		existing, e := a.Numbers.ByPhone(userID, phone)
		if e != nil {
			n.UserID = userID
			if _, e = a.Numbers.Create(&n); e != nil {
				skipped++
				continue
			}
			created++
			continue
		}
		n.ID, n.UserID, n.DeviceID = existing.ID, existing.UserID, existing.DeviceID
		n.SimType, n.LPAString, n.ConfirmCode = existing.SimType, existing.LPAString, existing.ConfirmCode
		n.PlanName, n.Notes, n.SecondaryNumbers = existing.PlanName, existing.Notes, existing.SecondaryNumbers
		n.RenewalDaysBefore = existing.RenewalDaysBefore
		n.AutoExpiryEnabled, n.AutoStartDate, n.AutoExpiryPeriod, n.AutoCalculatedExpiry =
			existing.AutoExpiryEnabled, existing.AutoStartDate, existing.AutoExpiryPeriod, existing.AutoCalculatedExpiry
		n.CreatedAt = existing.CreatedAt
		if e = a.Numbers.Update(&n); e != nil {
			skipped++
			continue
		}
		updated++
	}
	return created, updated, skipped, nil
}

// HandleAdminImport POST /admin/import：上传文件，自动识别三种格式导入。
// 号码类按号码匹配（存在更新 / 不存在新建）；渠道配置整份替换。
func (a *App) HandleAdminImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	fail := func(msg string) {
		a.setFlash(w, msg, true)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		fail("读取上传文件失败")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 32<<20))
	if err != nil {
		fail("读取上传文件失败")
		return
	}
	trimmed := bytes.TrimSpace(raw)
	u := a.currentUser(r)

	switch {
	case bytes.HasPrefix(trimmed, []byte("{")) && bytes.Contains(raw, []byte(`"esimCards"`)):
		created, updated, skipped, err := a.importNumbersFromSIMHub(u.ID, raw)
		if err != nil {
			fail("号码 JSON 解析失败：" + err.Error())
			return
		}
		a.setFlash(w, "导入完成：新建 "+strconv.Itoa(created)+"，更新 "+strconv.Itoa(updated)+"，跳过 "+strconv.Itoa(skipped), skipped > 0 && created+updated == 0)
	case bytes.HasPrefix(trimmed, []byte("{")) && bytes.Contains(raw, []byte(`"notification_config"`)):
		if err := a.importChannelsFromJSON(u.ID, raw); err != nil {
			fail("渠道配置导入失败：" + err.Error())
			return
		}
		a.setFlash(w, "渠道配置已导入（整份替换），请在通知配置页核对", false)
	default:
		created, updated, skipped, err := a.importNumbersFromCSV(u.ID, raw)
		if err != nil || created+updated == 0 {
			fail("无法识别的文件格式（支持 SIMHub/本程序号码 JSON、号码 CSV、渠道配置 JSON）")
			return
		}
		a.setFlash(w, "导入完成：新建 "+strconv.Itoa(created)+"，更新 "+strconv.Itoa(updated)+"，跳过 "+strconv.Itoa(skipped), skipped > 0 && created+updated == 0)
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
