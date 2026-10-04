package notify

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"simkeeper/internal/store"
	"simkeeper/internal/tgcall"
)

// Sender 根据用户配置把一条通知分发到全部启用渠道，并写入通知记录。
type Sender struct {
	Notify *store.NotifyRepo
	Users  *store.UserRepo
	// HTTPClient 供 HTTP 类渠道使用；nil 时各发送器自建默认客户端
	HTTPClient *http.Client
}

// Result 汇总一次多渠道分发的结果。
type Result struct {
	SentCount     int
	TotalChannels int
	Errors        []string
}

// SubjectFor 生成渠道主题；语义与 PHP 版一致。
func SubjectFor(typ string) string {
	if typ == "renewal" {
		return "📱 eSIM续费提醒"
	}
	return "📞 eSIM使用提醒"
}

// SendToEnabledChannels 向用户已启用的全部渠道发送通知。
// 每个渠道先落一条 pending 记录，发送后更新为 sent/failed。
func (s *Sender) SendToEnabledChannels(userID, numberID int64, typ, message string) Result {
	res := Result{}
	cfg, err := s.Notify.ConfigForUser(userID)
	if err != nil {
		res.Errors = append(res.Errors, "读取通知配置失败: "+err.Error())
		return res
	}
	if cfg.IsZero() {
		res.Errors = append(res.Errors, "用户未配置通知渠道")
		return res
	}
	user, err := s.Users.ByID(userID)
	if err != nil {
		res.Errors = append(res.Errors, "用户不存在")
		return res
	}

	subject := SubjectFor(typ)

	// send 记录结果并统计；errors 收集失败原因
	send := func(channel string, do func() error) {
		res.TotalChannels++
		id, err := s.Notify.Record(userID, numberID, typ, channel, subject, message)
		if err != nil {
			res.Errors = append(res.Errors, channel+": 写入通知记录失败: "+err.Error())
			return
		}
		if sendErr := do(); sendErr != nil {
			_ = s.Notify.MarkFailed(id, sendErr.Error())
			res.Errors = append(res.Errors, channel+": "+sendErr.Error())
		} else {
			_ = s.Notify.MarkSent(id)
			res.SentCount++
		}
	}

	if cfg.EmailEnabled {
		to := cfg.EmailToEmail
		if to == "" {
			to = user.Email
		}
		ecfg := EmailConfig{
			Host:     cfg.EmailSMTPHost,
			Port:     cfg.EmailSMTPPort,
			Secure:   cfg.EmailSMTPSecure,
			Username: cfg.EmailSMTPUsername,
			Password: cfg.EmailPassword,
			From:     cfg.EmailFromEmail,
			FromName: cfg.EmailFromName,
		}
		send("email", func() error {
			return SendEmail(ecfg, to, subject, FormatEmailBody(message), message)
		})
	}

	if cfg.TelegramEnabled {
		tcfg := TelegramConfig{BotToken: cfg.TelegramBotToken, ChatID: cfg.TelegramChatID}
		send("telegram", func() error { return SendTelegram(s.HTTPClient, tcfg, message) })
	}

	if cfg.WxPusherEnabled {
		wcfg := WxPusherConfig{AppToken: cfg.WxPusherAppToken, UID: cfg.WxPusherUID}
		send("wxpusher", func() error { return SendWxPusher(s.HTTPClient, wcfg, subject, message) })
	}

	if cfg.FeishuEnabled {
		fcfg := FeishuConfig{Webhook: cfg.FeishuWebhook, Secret: cfg.FeishuSecret}
		send("feishu", func() error { return SendFeishu(s.HTTPClient, fcfg, message) })
	}

	if cfg.DingTalkEnabled {
		dcfg := DingTalkConfig{Webhook: cfg.DingTalkWebhook, Secret: cfg.DingTalkSecret}
		send("dingtalk", func() error { return SendDingTalk(s.HTTPClient, dcfg, message) })
	}

	if cfg.TGCallEnabled {
		// 电话没有文本内容，message 仅作留档
		send("tgcall", func() error { return s.placeMissedCall(userID, cfg) })
	}

	return res
}

// placeMissedCall 执行一次 TG 未接来电；会话若被刷新则回存数据库。
func (s *Sender) placeMissedCall(userID int64, cfg *store.NotifyConfig) error {
	store := &tgcall.SessionStore{
		Load: func() ([]byte, error) {
			return base64.StdEncoding.DecodeString(cfg.TGCallSession)
		},
		Save: func(b []byte) error {
			return s.Notify.SaveTGCallSession(userID, base64.StdEncoding.EncodeToString(b))
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	newSession, err := tgcall.PlaceMissedCall(ctx, tgcall.Params{
		APIID:   cfg.TGCallAPIID,
		APIHash: cfg.TGCallAPIHash,
		Phone:   cfg.TGCallPhone,
		Session: cfg.TGCallSession,
		Target:  cfg.TGCallTarget,
	}, store)
	if err != nil {
		return err
	}
	if newSession != "" && newSession != cfg.TGCallSession {
		_ = s.Notify.SaveTGCallSession(userID, newSession)
	}
	return nil
}

// SendTest 向用户已启用的全部渠道发送一条测试消息并落记录（type=test）。
// preview 可选，附加一段内容预览；返回给前端的错误信息已做成可直接展示的文案。
func (s *Sender) SendTest(userID int64, preview string) Result {
	var b strings.Builder
	b.WriteString("🧪 SimKeeper测试通知\n\n")
	b.WriteString("这是一条测试消息，说明通知渠道配置可用。\n")
	if preview != "" {
		b.WriteString("\n内容预览：\n" + preview + "\n")
	}
	b.WriteString("\n发送时间：" + time.Now().Format("2006-01-02 15:04:05"))
	return s.SendToEnabledChannels(userID, 0, "test", b.String())
}
