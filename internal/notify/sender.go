package notify

import (
	"context"
	"encoding/base64"
	"fmt"
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
	// 渠道级结果：定时任务据此判断是否发「渠道失效告警」
	SucceededChannels []string
	FailedChannels    []string
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

	// send 落一条记录、执行发送并按结果更新状态；渠道明细供告警使用
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
			res.FailedChannels = append(res.FailedChannels, channel)
		} else {
			_ = s.Notify.MarkSent(id)
			res.SentCount++
			res.SucceededChannels = append(res.SucceededChannels, channel)
		}
	}

	for _, channel := range []string{"email", "telegram", "wxpusher", "feishu", "dingtalk", "tgcall"} {
		if !cfg.ChannelEnabled(channel) {
			continue
		}
		send(channel, func() error { return s.sendOne(user, cfg, channel, subject, message) })
	}

	return res
}

// sendOne 向单个渠道发送；SendToEnabledChannels、重投与系统告警共用。
func (s *Sender) sendOne(user *store.User, cfg *store.NotifyConfig, channel, subject, message string) error {
	switch channel {
	case "email":
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
		return SendEmail(ecfg, to, subject, FormatEmailBody(message), message)
	case "telegram":
		return SendTelegram(s.HTTPClient, TelegramConfig{BotToken: cfg.TelegramBotToken, ChatID: cfg.TelegramChatID}, message)
	case "wxpusher":
		return SendWxPusher(s.HTTPClient, WxPusherConfig{AppToken: cfg.WxPusherAppToken, UID: cfg.WxPusherUID}, subject, message)
	case "feishu":
		return SendFeishu(s.HTTPClient, FeishuConfig{Webhook: cfg.FeishuWebhook, Secret: cfg.FeishuSecret}, message)
	case "dingtalk":
		return SendDingTalk(s.HTTPClient, DingTalkConfig{Webhook: cfg.DingTalkWebhook, Secret: cfg.DingTalkSecret}, message)
	case "tgcall":
		return s.placeMissedCall(user.ID, cfg)
	}
	return fmt.Errorf("未知渠道: %s", channel)
}

// SendOneChannel 针对一条已有失败记录的重投：只发原渠道。
// 渠道已被用户停用时返回错误（调用方会计入重试次数）。
func (s *Sender) SendOneChannel(userID int64, typ, channel, message string) error {
	user, err := s.Users.ByID(userID)
	if err != nil {
		return fmt.Errorf("用户不存在")
	}
	cfg, err := s.Notify.ConfigForUser(userID)
	if err != nil {
		return err
	}
	if cfg.IsZero() || !cfg.ChannelEnabled(channel) {
		return fmt.Errorf("渠道已停用，不再重试")
	}
	return s.sendOne(user, cfg, channel, SubjectFor(typ), message)
}

// SendSystemAlert 通过指定渠道发送系统告警（渠道投递失败提醒）。
// 告警不打 TG 电话；逐渠道落 type=system 记录。
func (s *Sender) SendSystemAlert(userID int64, channels []string, message string) error {
	user, err := s.Users.ByID(userID)
	if err != nil {
		return err
	}
	cfg, err := s.Notify.ConfigForUser(userID)
	if err != nil {
		return err
	}
	subject := "⚠️ SimKeeper 渠道投递失败告警"
	var errs []string
	for _, ch := range channels {
		if ch == "tgcall" {
			continue // 告警不打电话
		}
		id, err := s.Notify.Record(userID, 0, "system", ch, subject, message)
		if err != nil {
			errs = append(errs, ch+": 写入记录失败")
			continue
		}
		if sendErr := s.sendOne(user, cfg, ch, subject, message); sendErr != nil {
			_ = s.Notify.MarkFailed(id, sendErr.Error())
			errs = append(errs, ch+": "+sendErr.Error())
		} else {
			_ = s.Notify.MarkSent(id)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
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
