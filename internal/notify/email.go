// Package notify 实现三个通知渠道：Email（SMTP）、Telegram、WxPusher。
// 全部使用标准库，不引入第三方 SMTP/HTTP 封装。
package notify

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// EmailConfig 邮件渠道配置，取自 notification_configs 行。
type EmailConfig struct {
	Host     string
	Port     int
	Secure   string // tls(STARTTLS) | ssl(隐式TLS) | none(明文)
	Username string
	Password string
	From     string
	FromName string
}

// mimeBoundary 每封邮件独立 boundary，避免正文撞分隔符。
func mimeBoundary() string {
	return "bht" + fmt.Sprint(time.Now().UnixNano())
}

// encodeHeader 按 RFC 2047 编码 UTF-8 头部（主题、显示名）。
func encodeHeader(s string) string {
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

// buildMIME 组装 multipart/alternative 邮件体：纯文本 + HTML 两部分。
func buildMIME(fromName, from, to, subject, htmlBody, altText string) string {
	boundary := mimeBoundary()
	fromHeader := from
	if fromName != "" {
		fromHeader = encodeHeader(fromName) + " <" + from + ">"
	}
	var b strings.Builder
	b.WriteString("From: " + fromHeader + "\r\n")
	b.WriteString("To: <" + to + ">\r\n")
	b.WriteString("Subject: " + encodeHeader(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(altText)
	b.WriteString("\r\n\r\n--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(htmlBody)
	b.WriteString("\r\n\r\n--" + boundary + "--\r\n")
	return b.String()
}

// SendEmail 通过用户自配的 SMTP 服务器发信。
// 与 PHP/PHPMailer 版语义一致：tls=STARTTLS（默认 587）、ssl=隐式 TLS（465）、none=不加密。
// 返回错误时信息含 SMTP 服务器响应，便于用户在页面上诊断。
func SendEmail(cfg EmailConfig, to, subject, htmlBody, altText string) error {
	if cfg.Host == "" {
		return fmt.Errorf("SMTP 主机未配置")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	raw := buildMIME(cfg.FromName, cfg.From, to, subject, htmlBody, altText)

	var client *smtp.Client
	var err error
	switch cfg.Secure {
	case "ssl":
		conn, dialErr := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
		if dialErr != nil {
			return fmt.Errorf("SMTP 连接失败(%s): %w", addr, dialErr)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
	case "none":
		conn, dialErr := net.Dial("tcp", addr)
		if dialErr != nil {
			return fmt.Errorf("SMTP 连接失败(%s): %w", addr, dialErr)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
	default: // tls: 先明文连接再 STARTTLS
		client, err = smtp.Dial(addr)
		if err == nil {
			if ok, _ := client.Extension("STARTTLS"); ok {
				err = client.StartTLS(&tls.Config{ServerName: cfg.Host})
			}
			// 服务器不支持 STARTTLS 时按明文继续，交由用户自行选择 none
		}
	}
	if err != nil {
		return fmt.Errorf("SMTP 建立连接失败: %w", err)
	}
	defer client.Close()

	if cfg.Password != "" {
		auth := chooseAuth(cfg, client)
		if auth != nil {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("SMTP 认证失败: %w", err)
			}
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("SMTP MAIL FROM 失败: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP RCPT TO 失败: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA 失败: %w", err)
	}
	if _, err := w.Write([]byte(raw)); err != nil {
		return fmt.Errorf("SMTP 写入正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP 提交正文失败: %w", err)
	}
	return client.Quit()
}

// chooseAuth 依服务器宣告的扩展选择 PLAIN 或 LOGIN 认证；两者都不支持则跳过。
func chooseAuth(cfg EmailConfig, client *smtp.Client) smtp.Auth {
	_, exts := client.Extension("AUTH")
	username := cfg.Username
	if username == "" {
		username = cfg.From
	}
	switch {
	case strings.Contains(exts, "PLAIN"):
		return smtp.PlainAuth("", username, cfg.Password, cfg.Host)
	case strings.Contains(exts, "LOGIN"):
		return &loginAuth{username: username, password: cfg.Password}
	}
	return nil
}

// loginAuth 实现 SMTP AUTH LOGIN（部分服务商只认这种）。
type loginAuth struct {
	username, password string
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.username), nil
	case "password:":
		return []byte(a.password), nil
	}
	return nil, fmt.Errorf("SMTP AUTH LOGIN 收到未知提示: %q", fromServer)
}

// FormatEmailBody 生成带品牌样式的 HTML 正文；纯文本部分由调用方传入原文。
// message 已含换行，经 escapeHTML 后把 \n 转 <br>，语义同 PHP 版 nl2br(htmlspecialchars(...))。
func FormatEmailBody(message string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>保号通通知</title><style>
body { font-family: Arial, sans-serif; line-height: 1.6; color: #333; }
.container { max-width: 600px; margin: 0 auto; padding: 20px; }
.header { background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); color: white; padding: 20px; text-align: center; border-radius: 8px 8px 0 0; }
.content { background: #f9f9f9; padding: 20px; border-radius: 0 0 8px 8px; }
.footer { text-align: center; margin-top: 20px; color: #666; font-size: 12px; }
.message { background: white; padding: 15px; border-radius: 5px; border-left: 4px solid #667eea; }
</style></head><body><div class="container"><div class="header"><h2>📱 保号通通知</h2></div><div class="content"><div class="message">`)
	b.WriteString(NL2BR(html.EscapeString(message)))
	b.WriteString(`</div></div><div class="footer"><p>此邮件由保号通系统自动发送，请勿回复。</p></div></div></body></html>`)
	return b.String()
}

// NL2BR 对已转义的文本做换行转换；escape 已由调用方通过 EscapeText 完成。
func NL2BR(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "<br>"), "\n", "<br>")
}
