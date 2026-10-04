package web

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"simkeeper/internal/auth"
	"simkeeper/internal/notify"
	"simkeeper/internal/store"
)

// HandleForgot 忘记密码：重置链接通过用户自己在「通知配置」里设置的
// 邮件渠道发出（收件人 = 账号邮箱）。无论请求的邮箱是否存在，
// 响应完全一致，不暴露账号存在性。
func (a *App) HandleForgot(w http.ResponseWriter, r *http.Request) {
	d := a.baseData(r, "找回密码")
	if r.Method != http.MethodPost {
		a.render(w, http.StatusOK, "page_forgot", d)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	a.forgotGeneric(w, r)
	if !emailRe.MatchString(email) {
		return
	}
	u, err := a.Users.ByEmail(email)
	if err != nil || u.Status != "active" {
		return
	}
	cfg, err := a.Notify.ConfigForUser(u.ID)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	if cfg.IsZero() || !cfg.EmailEnabled || cfg.EmailSMTPHost == "" || cfg.EmailFromEmail == "" {
		log.Printf("[web] 找回密码请求无法投递（用户 %d 未配置可用邮件渠道）", u.ID)
		return
	}

	now := time.Now()
	if n, err := a.Resets.CountActiveForUser(u.ID, now); err == nil && n >= 3 {
		log.Printf("[web] 用户 %d 的有效重置令牌已达上限", u.ID)
		return
	}
	token, hash, err := store.NewResetToken()
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	if err := a.Resets.Create(u.ID, hash, now.Add(time.Hour)); err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	link := a.baseURL(r) + "/reset?token=" + token
	msg := "你（或他人）请求重置 SimKeeper 账号 " + u.Username + " 的密码。\n\n" +
		"重置链接（1 小时内有效，仅可使用一次）：\n" + link + "\n\n" +
		"如果不是你本人的操作，请忽略本邮件；密码不会在未点击链接的情况下被修改。"
	ecfg := notify.EmailConfig{
		Host:     cfg.EmailSMTPHost,
		Port:     cfg.EmailSMTPPort,
		Secure:   cfg.EmailSMTPSecure,
		Username: cfg.EmailSMTPUsername,
		Password: cfg.EmailPassword,
		From:     cfg.EmailFromEmail,
		FromName: cfg.EmailFromName,
	}
	if sendErr := notify.SendEmail(ecfg, u.Email, "SimKeeper 密码重置", notify.FormatEmailBody(msg), msg); sendErr != nil {
		log.Printf("[web] 重置邮件发送失败（用户 %d）: %v", u.ID, sendErr)
	}
}

// forgotGeneric 统一响应：不区分邮箱是否存在（防账号枚举）。
func (a *App) forgotGeneric(w http.ResponseWriter, r *http.Request) {
	a.setFlash(w, "如果该邮箱存在且站点已配置邮件渠道，重置邮件已发出，请查收（含垃圾箱）。未配置邮件渠道的部署请联系管理员在后台重置。", false)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// HandleReset 重置密码：GET 校验令牌并渲染表单，POST 设置新密码。
func (a *App) HandleReset(w http.ResponseWriter, r *http.Request) {
	d := a.baseData(r, "重置密码")
	token := ""

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "请求体解析失败", http.StatusBadRequest)
			return
		}
		token = r.PostFormValue("token")
		np := r.PostFormValue("new_password")
		confirm := r.PostFormValue("confirm_password")
		switch {
		case len(token) != 64:
			a.setFlash(w, "重置链接无效", true)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		case len(np) < minPasswordLen:
			d.Flash, d.FlashIsErr = "密码长度至少 8 位", true
		case np != confirm:
			d.Flash, d.FlashIsErr = "两次输入的密码不一致", true
		}
		if d.Flash == "" {
			userID, err := a.Resets.Consume(store.HashToken(token), time.Now())
			if err != nil {
				a.setFlash(w, "重置链接无效或已过期，请重新申请", true)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			newHash, err := auth.HashPassword(np)
			if err != nil {
				http.Error(w, "内部错误", http.StatusInternalServerError)
				return
			}
			if err := a.Users.UpdatePassword(userID, newHash); err != nil {
				http.Error(w, "内部错误", http.StatusInternalServerError)
				return
			}
			// 踢掉该用户全部会话，用新密码重新登录
			_ = a.Sessions.DestroyForUser(userID)
			a.setFlash(w, "密码已重置，请使用新密码登录", false)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		d.Content = map[string]any{"Token": token}
		a.render(w, http.StatusOK, "page_reset", d)
		return
	}

	// GET：校验令牌有效性后渲染表单（不消费）
	token = r.URL.Query().Get("token")
	if len(token) != 64 || !a.Resets.Valid(sha256Hex(token), time.Now()) {
		a.setFlash(w, "重置链接无效或已过期，请重新申请", true)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	d.Content = map[string]any{"Token": token}
	a.render(w, http.StatusOK, "page_reset", d)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// baseURL 推断重置链接的外部地址：直连 TLS 或强制 Secure Cookie 时为
// https；开了 trust-proxy 时读 X-Forwarded-Proto；否则 http。
func (a *App) baseURL(r *http.Request) string {
	scheme := "http"
	switch {
	case r.TLS != nil || a.SecureCookies:
		scheme = "https"
	case a.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https":
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
