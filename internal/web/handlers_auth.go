package web

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"baohaotong/internal/auth"
	"baohaotong/internal/store"
)

// 用户名规则：中英文、数字、下划线、连字符，2-50 位（与 PHP 版一致）。
var usernameRe = regexp.MustCompile(`^[\p{Han}A-Za-z0-9_-]{2,50}$`)

// 邮箱格式校验。
var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// 密码统一策略：注册与改密共用同一下限（修复 PHP 版两条路径不一致的问题）。
const minPasswordLen = 8

// loginTTL 登录限流参数：15 分钟窗口内最多 5 次失败。
const (
	loginWindow   = 15 * time.Minute
	loginMaxFails = 5
)

// HandleLogin 登录 + 注册双标签页。
func (a *App) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if a.currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	d := a.baseData(r, "登录")
	if r.Method != http.MethodPost {
		a.render(w, http.StatusOK, "page_login", d)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}
	ip := a.ClientIP(r)

	if r.PostFormValue("register") != "" {
		a.handleRegister(w, r, &d)
		return
	}

	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")

	// 先限流再验密，避免封禁窗口内继续打 DB
	fails, err := a.Attempts.RecentCount(ip, loginWindow)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	if fails >= loginMaxFails {
		d.Flash, d.FlashIsErr = "登录失败次数过多，请稍后再试", true
		a.render(w, http.StatusTooManyRequests, "page_login", d)
		return
	}
	if username == "" || password == "" {
		d.Flash, d.FlashIsErr = "请填写用户名和密码", true
		a.render(w, http.StatusOK, "page_login", d)
		return
	}

	user, ok, err := a.Users.Authenticate(username, password)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	if !ok {
		_ = a.Attempts.Record(ip, username)
		// 统一失败提示，不区分「用户不存在」和「密码错误」
		d.Flash, d.FlashIsErr = "用户名或密码错误，或账户已被禁用", true
		a.render(w, http.StatusOK, "page_login", d)
		return
	}
	_ = a.Attempts.Clear(ip)
	a.loginAs(w, r, user)
}

// handleRegister 注册分支。
func (a *App) handleRegister(w http.ResponseWriter, r *http.Request, d *pageData) {
	if d.AllowReg == false {
		d.Flash, d.FlashIsErr = "本站当前不开放注册", true
		a.render(w, http.StatusForbidden, "page_login", *d)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("reg_username"))
	email := strings.TrimSpace(r.PostFormValue("reg_email"))
	password := r.PostFormValue("reg_password")
	confirm := r.PostFormValue("reg_confirm_password")

	switch {
	case username == "" || email == "" || password == "" || confirm == "":
		d.Flash, d.FlashIsErr = "请填写所有字段", true
	case !usernameRe.MatchString(username):
		d.Flash, d.FlashIsErr = "用户名只能包含中英文、数字、下划线和连字符（2-50 位）", true
	case !emailRe.MatchString(email):
		d.Flash, d.FlashIsErr = "请输入有效的邮箱地址", true
	case len(password) < minPasswordLen:
		d.Flash, d.FlashIsErr = "密码长度至少 8 位", true
	case password != confirm:
		d.Flash, d.FlashIsErr = "两次输入的密码不一致", true
	}
	if d.Flash != "" {
		a.render(w, http.StatusOK, "page_login", *d)
		return
	}
	if _, err := a.Users.ByUsername(username); err == nil {
		d.Flash, d.FlashIsErr = "用户名已存在", true
		a.render(w, http.StatusOK, "page_login", *d)
		return
	}
	if _, err := a.Users.ByEmail(email); err == nil {
		d.Flash, d.FlashIsErr = "邮箱已被注册", true
		a.render(w, http.StatusOK, "page_login", *d)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	if _, err := a.Users.Create(username, email, hash, "user"); err != nil {
		d.Flash, d.FlashIsErr = "注册失败，请重试", true
		a.render(w, http.StatusOK, "page_login", *d)
		return
	}
	d.Flash = "注册成功，请登录"
	a.render(w, http.StatusOK, "page_login", *d)
}

// loginAs 建立 Cookie 会话并按角色跳转。
// 先销毁旧会话（匿名预会话或已被 CSRF 保护的登录前会话），
// 再发新令牌——服务端版 session rotation，防会话固定。
func (a *App) loginAs(w http.ResponseWriter, r *http.Request, u *store.User) {
	if old := a.currentSession(r); old != nil {
		_ = a.Sessions.Destroy(old.Token)
	}
	sess, err := a.Sessions.Create(u.ID, a.ClientIP(r), r.UserAgent())
	if err != nil {
		http.Error(w, "建立会话失败", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.sessionCookie(r, sess.Token, int(auth.SessionTTL/time.Second)))
	dest := "/"
	if u.Role == "admin" {
		dest = "/admin"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// HandleLogout 登出：仅接受 POST（修复 PHP 版 GET 触发状态变更的问题）。
func (a *App) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if sess := a.currentSession(r); sess != nil {
		_ = a.Sessions.Destroy(sess.Token)
	}
	http.SetCookie(w, a.sessionCookie(r, "", -1))
	http.Redirect(w, r, "/login?logout=1", http.StatusFound)
}

// HandleProfile 个人资料：改邮箱 + 改密码。
// 改密后撤销该用户全部会话（修复 PHP 版旧会话不失效的问题）。
func (a *App) HandleProfile(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	d := a.baseData(r, "个人资料")
	d.ActiveNav = "profile"

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "请求体解析失败", http.StatusBadRequest)
			return
		}
		if r.PostFormValue("change_email") != "" {
			email := strings.TrimSpace(r.PostFormValue("email"))
			if !emailRe.MatchString(email) {
				d.Flash, d.FlashIsErr = "请输入有效的邮箱地址", true
			} else if exist, err := a.Users.ByEmail(email); err == nil && exist.ID != u.ID {
				d.Flash, d.FlashIsErr = "邮箱已被其他账户使用", true
			} else if err := a.Users.UpdateEmail(u.ID, email); err != nil {
				d.Flash, d.FlashIsErr = "邮箱更新失败", true
			} else {
				a.setFlash(w, "邮箱已更新", false)
				http.Redirect(w, r, "/profile", http.StatusSeeOther)
				return
			}
		} else if r.PostFormValue("change_password") != "" {
			current := r.PostFormValue("current_password")
			np := r.PostFormValue("new_password")
			confirm := r.PostFormValue("confirm_password")
			switch {
			case len(np) < minPasswordLen:
				d.Flash, d.FlashIsErr = "新密码长度至少 8 位", true
			case np != confirm:
				d.Flash, d.FlashIsErr = "两次输入的密码不一致", true
			default:
				hash, err := a.Users.PasswordHashByID(u.ID)
				if err != nil || !auth.CheckPassword(hash, current) {
					d.Flash, d.FlashIsErr = "当前密码不正确", true
				}
			}
			if d.Flash == "" {
				newHash, err := auth.HashPassword(np)
				if err != nil {
					http.Error(w, "内部错误", http.StatusInternalServerError)
					return
				}
				if err := a.Users.UpdatePassword(u.ID, newHash); err != nil {
					d.Flash, d.FlashIsErr = "密码更新失败", true
				} else {
					// 撤销全部会话（含当前），强制重新登录
					_ = a.Sessions.DestroyForUser(u.ID)
					http.SetCookie(w, a.sessionCookie(r, "", -1))
					a.setFlash(w, "密码已更新，请重新登录", false)
					http.Redirect(w, r, "/login", http.StatusSeeOther)
					return
				}
			}
		}
	}

	d.Content = map[string]any{"Email": u.Email}
	a.render(w, http.StatusOK, "page_profile", d)
}
