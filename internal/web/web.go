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
	Notify   *store.NotifyRepo
	Settings *store.SettingRepo
	Sessions *auth.SessionStore
	Sender   *notify.Sender
	Attempts *store.LoginAttemptRepo
	Tmpl     *template.Template
	// TrustProxy 决定是否信任 X-Forwarded-For 取真实 IP。
	// 部署在可信反代之后时由配置开启；默认关闭，避免伪造头绕过限流。
	TrustProxy bool
}

// New 构造 App 并解析内嵌模板。
func New(database *db.DB) (*App, error) {
	tmpl, err := template.New("bht").Funcs(funcMap).ParseFS(tmplFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &App{
		DB:       database,
		Users:    &store.UserRepo{DB: database},
		Numbers:  &store.NumberRepo{DB: database},
		Notify:   &store.NotifyRepo{DB: database},
		Settings: &store.SettingRepo{DB: database},
		Sessions: &auth.SessionStore{DB: database},
		Attempts: &store.LoginAttemptRepo{DB: database},
		Sender:   &notify.Sender{Notify: &store.NotifyRepo{DB: database}, Users: &store.UserRepo{DB: database}},
		Tmpl:     tmpl,
	}, nil
}

// ClientIP 提取客户端 IP。
// TrustProxy=false 时只认 RemoteAddr，不解析 X-Forwarded-For
// （修复 PHP 版无条件信任请求头的问题）。
func (a *App) ClientIP(r *http.Request) string {
	if a.TrustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			// 取链路第一个（最原始客户端）
			if i := strings.IndexByte(xf, ','); i > 0 {
				xf = xf[:i]
			}
			return strings.TrimSpace(xf)
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
// Secure 依请求是否为 TLS 决定，不读 X-Forwarded-Proto（修复 PHP 版问题）。
func (a *App) sessionCookie(r *http.Request, token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     "SKSESSION",
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil,
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

// requireAdmin 要求管理员，普通用户跳回首页，未登录去登录页。
func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := a.currentUser(r)
		if u == nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		if u.Role != "admin" {
			http.Redirect(w, r, "/", http.StatusFound)
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
	AllowReg   bool
}

// baseData 填充公共字段；user 可为 nil（登录页）。
func (a *App) baseData(r *http.Request, title string) pageData {
	siteName, _ := a.Settings.Get("site_name")
	if siteName == "" {
		siteName = "保号通"
	}
	d := pageData{
		SiteName:  siteName,
		Title:     title,
		User:      a.currentUser(r),
		Year:      time.Now().Year(),
		AllowReg:  a.Settings.GetInt("allow_registration", 1) == 1,
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
