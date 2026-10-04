package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"simkeeper/internal/store"
	"simkeeper/internal/tgcall"
)

// notifyConfigPublic 模板可安全展示的字段子集。
type notifyConfigPublic struct {
	EmailEnabled      bool
	EmailSMTPHost     string
	EmailSMTPPort     int
	EmailSMTPSecure   string
	EmailSMTPUsername string
	EmailFromEmail    string
	EmailFromName     string
	EmailToEmail      string
	TelegramEnabled   bool
	TelegramChatID    string
	WxPusherEnabled   bool
	WxPusherUID       string
	FeishuEnabled     bool
	DingTalkEnabled   bool
	TGCallEnabled     bool
	TGCallAPIID       int
	TGCallPhone       string
	TGCallTarget      string
	// Has* 指示是否已设置密码/令牌（只展示是否，不回显值；
	// 修复 PHP 版把 smtp_password 回显进 HTML 的问题）。
	// 注意：两家 webhook URL 内嵌 token（钉钉 access_token / 飞书 hook id），
	// 与密码同等对待，保存后不回显。
	HasEmailPassword   bool
	HasTelegramToken   bool
	HasWxPusherToken   bool
	HasFeishuWebhook   bool
	HasFeishuSecret    bool
	HasDingTalkWebhook bool
	HasDingTalkSecret  bool
	HasTGCallAPIHash   bool
	HasTGCallSession   bool
}

// toPublic 从存储配置构造展示子集。
func toPublic(c *store.NotifyConfig) *notifyConfigPublic {
	if c == nil {
		return nil
	}
	return &notifyConfigPublic{
		EmailEnabled: c.EmailEnabled, EmailSMTPHost: c.EmailSMTPHost, EmailSMTPPort: c.EmailSMTPPort,
		EmailSMTPSecure: c.EmailSMTPSecure, EmailSMTPUsername: c.EmailSMTPUsername,
		EmailFromEmail: c.EmailFromEmail, EmailFromName: c.EmailFromName, EmailToEmail: c.EmailToEmail,
		TelegramEnabled: c.TelegramEnabled, TelegramChatID: c.TelegramChatID,
		WxPusherEnabled: c.WxPusherEnabled, WxPusherUID: c.WxPusherUID,
		FeishuEnabled:   c.FeishuEnabled,
		DingTalkEnabled: c.DingTalkEnabled,
		TGCallEnabled:   c.TGCallEnabled, TGCallAPIID: c.TGCallAPIID,
		TGCallPhone: c.TGCallPhone, TGCallTarget: c.TGCallTarget,
		HasEmailPassword:   c.EmailPassword != "",
		HasTelegramToken:   c.TelegramBotToken != "",
		HasWxPusherToken:   c.WxPusherAppToken != "",
		HasFeishuWebhook:   c.FeishuWebhook != "",
		HasFeishuSecret:    c.FeishuSecret != "",
		HasDingTalkWebhook: c.DingTalkWebhook != "",
		HasDingTalkSecret:  c.DingTalkSecret != "",
		HasTGCallAPIHash:   c.TGCallAPIHash != "",
		HasTGCallSession:   c.TGCallSession != "",
	}
}

// HandleNotifyConfig 通知渠道配置页（GET 展示 / POST 保存）。
func (a *App) HandleNotifyConfig(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	d := a.baseData(r, "通知配置")
	d.ActiveNav = "notify"

	if r.Method == http.MethodPost {
		a.saveNotifyConfig(w, r, u.ID, &d)
		return
	}
	cfg, err := a.Notify.ConfigForUser(u.ID)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	d.Content = map[string]any{"Cfg": toPublic(cfg)}
	a.render(w, http.StatusOK, "page_notify_config", d)
}

// saveNotifyConfig 保存渠道配置。
// 密码/令牌留空表示「保持不变」，仅当用户填了新值才覆盖。
func (a *App) saveNotifyConfig(w http.ResponseWriter, r *http.Request, userID int64, d *pageData) {
	existing, err := a.Notify.ConfigForUser(userID)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	cfg := &store.NotifyConfig{UserID: userID}
	if existing != nil {
		cfg = existing
	}
	form := func(key string) string { return strings.TrimSpace(r.PostFormValue(key)) }

	cfg.EmailEnabled = r.PostFormValue("email_enabled") == "1"
	cfg.EmailSMTPHost = form("email_smtp_host")
	cfg.EmailSMTPPort = atoiDefault(form("email_smtp_port"), 587)
	cfg.EmailSMTPSecure = form("email_smtp_secure")
	if cfg.EmailSMTPSecure != "ssl" && cfg.EmailSMTPSecure != "none" {
		cfg.EmailSMTPSecure = "tls"
	}
	cfg.EmailSMTPUsername = form("email_smtp_username")
	cfg.EmailFromEmail = form("email_from_email")
	cfg.EmailFromName = form("email_from_name")
	cfg.EmailToEmail = form("email_to_email")
	if pw := r.PostFormValue("email_password"); pw != "" {
		cfg.EmailPassword = pw
	}
	cfg.TelegramEnabled = r.PostFormValue("telegram_enabled") == "1"
	cfg.TelegramChatID = form("telegram_chat_id")
	if tok := r.PostFormValue("telegram_bot_token"); tok != "" {
		cfg.TelegramBotToken = tok
	}
	cfg.WxPusherEnabled = r.PostFormValue("wxpusher_enabled") == "1"
	cfg.WxPusherUID = form("wxpusher_uid")
	if tok := r.PostFormValue("wxpusher_app_token"); tok != "" {
		cfg.WxPusherAppToken = tok
	}
	cfg.FeishuEnabled = r.PostFormValue("feishu_enabled") == "1"
	if wh := form("feishu_webhook"); wh != "" {
		cfg.FeishuWebhook = wh
	}
	if sec := r.PostFormValue("feishu_secret"); sec != "" {
		cfg.FeishuSecret = sec
	}
	cfg.DingTalkEnabled = r.PostFormValue("dingtalk_enabled") == "1"
	if wh := form("dingtalk_webhook"); wh != "" {
		cfg.DingTalkWebhook = wh
	}
	if sec := r.PostFormValue("dingtalk_secret"); sec != "" {
		cfg.DingTalkSecret = sec
	}
	cfg.TGCallEnabled = r.PostFormValue("tgcall_enabled") == "1"
	cfg.TGCallAPIID = atoiDefault(form("tgcall_api_id"), 0)
	cfg.TGCallPhone = form("tgcall_phone")
	cfg.TGCallTarget = form("tgcall_target")
	if h := r.PostFormValue("tgcall_api_hash"); h != "" {
		cfg.TGCallAPIHash = h
	}

	// tdata 上传：有文件就尝试导入会话（与 tg-login 写入同一个字段）
	uploadedSession, importedUID, importErr := a.readTDataUpload(r)
	if importErr != "" {
		d.Flash, d.FlashIsErr = importErr, true
		d.Content = map[string]any{"Cfg": toPublic(cfg)}
		a.render(w, http.StatusOK, "page_notify_config", *d)
		return
	}
	if uploadedSession != "" {
		cfg.TGCallSession = uploadedSession
	}

	// 渠道启用时的完整性校验
	if cfg.EmailEnabled && (cfg.EmailSMTPHost == "" || cfg.EmailFromEmail == "") {
		d.Flash, d.FlashIsErr = "启用邮件渠道需填写 SMTP 主机与发件邮箱", true
		a.render(w, http.StatusOK, "page_notify_config", *d)
		return
	}
	if cfg.TelegramEnabled && (cfg.TelegramBotToken == "" || cfg.TelegramChatID == "") {
		d.Flash, d.FlashIsErr = "启用 Telegram 渠道需填写 Bot Token 与 Chat ID", true
		a.render(w, http.StatusOK, "page_notify_config", *d)
		return
	}
	if cfg.WxPusherEnabled && (cfg.WxPusherAppToken == "" || cfg.WxPusherUID == "") {
		d.Flash, d.FlashIsErr = "启用 WxPusher 渠道需填写 AppToken 与 UID", true
		a.render(w, http.StatusOK, "page_notify_config", *d)
		return
	}
	if cfg.FeishuEnabled && cfg.FeishuWebhook == "" {
		d.Flash, d.FlashIsErr = "启用飞书渠道需填写机器人 Webhook 地址", true
		a.render(w, http.StatusOK, "page_notify_config", *d)
		return
	}
	if cfg.DingTalkEnabled && cfg.DingTalkWebhook == "" {
		d.Flash, d.FlashIsErr = "启用钉钉渠道需填写机器人 Webhook 地址", true
		a.render(w, http.StatusOK, "page_notify_config", *d)
		return
	}
	if cfg.TGCallEnabled && (cfg.TGCallAPIID == 0 || cfg.TGCallAPIHash == "" ||
		cfg.TGCallTarget == "" || cfg.TGCallSession == "") {
		missing := "api_id、api_hash、被叫账号"
		if cfg.TGCallSession == "" {
			missing += "，且需先上传 tdata 或在服务器运行 simkeeper tg-login 完成登录"
		}
		d.Flash, d.FlashIsErr = "启用 TG 电话渠道需填写 "+missing, true
		a.render(w, http.StatusOK, "page_notify_config", *d)
		return
	}
	if err := a.Notify.SaveConfig(cfg); err != nil {
		d.Flash, d.FlashIsErr = "保存失败，请重试", true
		a.render(w, http.StatusOK, "page_notify_config", *d)
		return
	}
	msg := "通知配置已保存"
	if importedUID != 0 {
		msg = fmt.Sprintf("通知配置已保存；已从 tdata 导入会话（TG 用户 ID %d）。手机号仅 tg-login 需要，可留空", importedUID)
	}
	a.setFlash(w, msg, false)
	http.Redirect(w, r, "/notifications/config", http.StatusSeeOther)
}

// readTDataUpload 处理 TG 电话卡片里的 tdata ZIP 上传。
// 返回 (会话 base64, 被导入的 TG 用户 ID, 错误文案)；没有上传文件时返回全零。
// 上传内容只在内存中解析，不落盘。
func (a *App) readTDataUpload(r *http.Request) (sessionB64 string, tgUserID uint64, errMsg string) {
	f, _, err := r.FormFile("tgcall_tdata")
	if err != nil {
		// 没有上传文件——正常情况（用户没填这一项）
		return "", 0, ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, tgcall.TDataImportLimit+1))
	if err != nil {
		return "", 0, "读取上传文件失败"
	}
	sessionB64, uid, err := tgcall.ImportTData(data, r.PostFormValue("tgcall_tdata_passcode"))
	if err != nil {
		return "", 0, err.Error()
	}
	return sessionB64, uid, ""
}

// testResult JSON 响应结构。
type testResult struct {
	Success bool     `json:"success"`
	Sent    int      `json:"sent"`
	Total   int      `json:"total"`
	Errors  []string `json:"errors"`
	Message string   `json:"message"`
}

// HandleSendTest 发送测试通知（POST，JSON 响应）。
// 与 PHP 版 SSRF 的差异：本接口不接受任何 host/port 参数，
// 发送目标只来自用户已保存的渠道配置。
func (a *App) HandleSendTest(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// 节流：60 秒一次。TG 电话等渠道每次测试都是真实外呼，连点会触发平台限制
	if ok, _ := a.Notify.LastTestWithin(u.ID, time.Now(), time.Minute); ok {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(testResult{Success: false, Message: "测试发送过于频繁，请 1 分钟后再试"})
		return
	}
	res := a.Sender.SendTest(u.ID, r.PostFormValue("preview"))
	out := testResult{
		Success: res.SentCount > 0,
		Sent:    res.SentCount,
		Total:   res.TotalChannels,
		Errors:  res.Errors,
	}
	switch {
	case out.Total == 0:
		out.Message = "没有已启用的通知渠道，请先启用并保存"
	case out.Sent == out.Total:
		out.Message = "全部渠道发送成功"
	default:
		out.Message = "部分渠道失败，详见错误信息"
	}
	_ = json.NewEncoder(w).Encode(out)
}
