// Package web 装配 HTTP 路由、模板渲染与会话中间件。
package web

import (
	"context"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"simkeeper/internal/auth"
	"simkeeper/internal/db"
	"simkeeper/internal/notify"
	"simkeeper/internal/store"
)

// ctxKey 上下文键类型（非导出，避免与外部键冲突）。
type ctxKey int

const (
	ctxSession ctxKey = iota
	ctxUser
)

// App 聚合全部依赖；Handler 构建路由时闭包引用。
type App struct {
	DB       *db.DB
	Users    *store.UserRepo
	Numbers  *store.NumberRepo
	Devices  *store.DeviceRepo
	Notify   *store.NotifyRepo
	Settings *store.SettingRepo
	Sessions *auth.SessionStore
	Sender   *notify.Sender
	Attempts *store.LoginAttemptRepo
	Resets   *store.PasswordResetRepo
	Tmpl     *template.Template
	// TrustProxy 决定是否信任 X-Forwarded-For 取真实 IP。
	// 部署在可信反代之后时由配置开启；默认关闭，避免伪造头绕过限流。
	TrustProxy bool
	// SecureCookies 强制给会话 Cookie 加 Secure 标记。
	// 直连 TLS 时程序能从 r.TLS 自行判断；TLS 由反向代理终结时
	// 程序看到的是 HTTP，需要部署侧显式开启（-secure-cookies）。
	SecureCookies bool
}

// New 构造 App 并解析内嵌模板。
func New(database *db.DB) (*App, error) {
	tmpl, err := template.New("sk").Funcs(funcMap).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &App{
		DB:       database,
		Users:    &store.UserRepo{DB: database},
		Numbers:  &store.NumberRepo{DB: database},
		Devices:  &store.DeviceRepo{DB: database},
		Notify:   &store.NotifyRepo{DB: database},
		Settings: &store.SettingRepo{DB: database},
		Sessions: &auth.SessionStore{DB: database},
		Attempts: &store.LoginAttemptRepo{DB: database},
		Resets:   &store.PasswordResetRepo{DB: database},
		Sender:   &notify.Sender{Notify: &store.NotifyRepo{DB: database}, Users: &store.UserRepo{DB: database}},
		Tmpl:     tmpl,
	}, nil
}

// ClientIP 提取客户端 IP。
// TrustProxy=false 时只认 RemoteAddr，不解析 X-Forwarded-For
// （修复 PHP 版无条件信任请求头的问题）。
// TrustProxy=true 时取 X-Forwarded-For 链**最右侧**地址：客户端可以伪造
// XFF 的任意前缀，只有反向代理追加的最右一跳不可伪造；单层可信反代下
// 即真实客户端 IP。多层代理部署请在上游收敛该头，或自行扩展信任逻辑。
func (a *App) ClientIP(r *http.Request) string {
	if a.TrustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			parts := strings.Split(xf, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// currentUser 从请求上下文取当前登录用户；未登录返回 nil。
func (a *App) currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

// currentSession 同上取会话。
func (a *App) currentSession(r *http.Request) *auth.Session {
	s, _ := r.Context().Value(ctxSession).(*auth.Session)
	return s
}

// sessionCookie 会话 Cookie 的统一属性。
// Secure 在两种情况下开启：程序直连 TLS（r.TLS 非空），
// 或部署侧显式开启 SecureCookies（TLS 由反向代理终结的场景）。
func (a *App) sessionCookie(r *http.Request, token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     "SKSESSION",
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil || a.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	}
}

// withCtx 把会话与用户放进请求上下文。
func withCtx(r *http.Request, sess *auth.Session, u *store.User) context.Context {
	ctx := context.WithValue(r.Context(), ctxSession, sess)
	return context.WithValue(ctx, ctxUser, u)
}

// sessionMiddleware 解析会话 Cookie 并把会话与用户放进请求上下文。
// 匿名预会话（user_id 为 NULL）也放进上下文——它只为登录/注册/初始化页
// 承载 CSRF 令牌，currentUser 对它返回 nil。
func (a *App) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("SKSESSION"); err == nil {
			if sess, err := a.Sessions.Get(c.Value); err == nil && sess != nil {
				var u *store.User
				if sess.UserID > 0 {
					// 被封禁/停用的账号立即失效：查不到或状态异常都按未登录处理
					if found, err := a.Users.ByID(sess.UserID); err == nil && found.Status == "active" {
						u = found
					}
				}
				r = r.WithContext(withCtx(r, sess, u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireLogin 要求已登录，否则跳转登录页。
func (a *App) requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.currentUser(r) == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

// csrfProtect 对写方法执行 CSRF 校验；失败直接 403。
// 所有状态变更入口必须经此包装——Go 版不存在 GET 触发状态变更的路径。
func (a *App) csrfProtect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.VerifyCSRF(r, a.currentSession(r)) {
			http.Error(w, "403 CSRF 校验失败", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// handleFlagSVG GET /flags/{code}.svg：内嵌国旗小图。通配符整段取值
// （含 .svg 后缀），剥后缀后经白名单校验，不存在的代码 404；
// 公开可缓存（无敏感内容，模板大量复用）。
func (a *App) handleFlagSVG(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSuffix(strings.ToLower(r.PathValue("code")), ".svg")
	if !flagFiles[code] {
		http.NotFound(w, r)
		return
	}
	b, err := flagFS.ReadFile("flags/" + code + ".svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

// secureHeaders 基础安全响应头：防点击劫持与 MIME 嗅探。页面模板依赖
// 内联 style/script，完整 CSP 需放开 'unsafe-inline' 意义有限，先上无副作用的三个。
func (a *App) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// render 渲染指定页面模板。
func (a *App) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.Tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("[web] 渲染 %s 失败: %v", name, err)
	}
}

// pageData 每页公共字段。
type pageData struct {
	SiteName   string
	Title      string
	User       *store.User
	CSRFToken  string
	Flash      string
	FlashIsErr bool
	ActiveNav  string
	Content    any
	Year       int
}

// baseData 填充公共字段；user 可为 nil（登录页）。
func (a *App) baseData(r *http.Request, title string) pageData {
	siteName, _ := a.Settings.Get("site_name")
	if siteName == "" {
		siteName = "SimKeeper"
	}
	d := pageData{
		SiteName:  siteName,
		Title:     title,
		User:      a.currentUser(r),
		Year:      time.Now().Year(),
		ActiveNav: strings.TrimPrefix(r.URL.Path, "/"),
	}
	if sess := a.currentSession(r); sess != nil {
		d.CSRFToken = sess.CSRFToken
	}
	d.Flash, d.FlashIsErr = a.takeFlash(r)
	return d
}

// setFlash / takeFlash 用短时效 Cookie 传递一次性提示（PRG 模式）。
// 值必须 URL 编码：Go 的 SetCookie 会静默丢弃包含非 ASCII 字节（如中文）的 Cookie。
func (a *App) setFlash(w http.ResponseWriter, msg string, isErr bool) {
	name := "SKFLASH"
	if isErr {
		name = "SKFLASHERR"
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: url.QueryEscape(msg), Path: "/",
		MaxAge: 30, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) takeFlash(r *http.Request) (string, bool) {
	read := func(name string) string {
		c, err := r.Cookie(name)
		if err != nil {
			return ""
		}
		v, err := url.QueryUnescape(c.Value)
		if err != nil {
			return ""
		}
		return v
	}
	if v := read("SKFLASHERR"); v != "" {
		return v, true
	}
	return read("SKFLASH"), false
}
