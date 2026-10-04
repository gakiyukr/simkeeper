// routes.go 集中注册全部 HTTP 路由。
package web

import (
	"log"
	"net/http"
	"time"

	"simkeeper/internal/auth"
)

// Routes 构建路由表。整体包一层 sessionMiddleware：
// 请求先解析会话 Cookie 并把会话/用户放进上下文，再由各 handler 取用。
// 写方法一律经 csrfProtect（CSRF 令牌来自会话，匿名预会话也有自己的令牌）。
func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()

	// ---- 未登录可达 ----
	mux.HandleFunc("GET /login", a.handleLoginPage)
	mux.Handle("POST /login", a.csrfProtect(a.HandleLogin))
	mux.Handle("POST /login/totp", a.ensureSessionMW(a.csrfProtect(a.HandleLoginTOTP)))
	mux.Handle("POST /logout", a.csrfProtect(a.HandleLogout))
	mux.HandleFunc("GET /setup", a.handleSetupPage)
	mux.Handle("POST /setup", a.csrfProtect(a.HandleSetup))

	// ---- 用户侧 ----
	mux.Handle("GET /", a.requireLogin(a.HandleDashboard))
	mux.Handle("GET /numbers", a.requireLogin(a.HandleNumbers))
	mux.Handle("GET /numbers/new", a.requireLogin(a.HandleNumberNew))
	mux.Handle("POST /numbers/new", a.requireLogin(a.csrfProtect(a.HandleNumberNew)))
	mux.Handle("GET /numbers/export", a.requireLogin(a.HandleHistoryExport))
	mux.Handle("GET /numbers/{id}/edit", a.requireLogin(a.HandleNumberEdit))
	mux.Handle("POST /numbers/{id}/edit", a.requireLogin(a.csrfProtect(a.HandleNumberEdit)))
	mux.Handle("POST /numbers/{id}/delete", a.requireLogin(a.csrfProtect(a.HandleNumberDelete)))
	mux.Handle("POST /numbers/{id}/disable-auto", a.requireLogin(a.csrfProtect(a.HandleNumberDisableAuto)))
	mux.Handle("GET /notifications", a.requireLogin(a.HandleNotificationHistory))
	mux.Handle("GET /notifications/config", a.requireLogin(a.HandleNotifyConfig))
	mux.Handle("POST /notifications/config",
		a.requireLogin(a.limitBody(64<<20, a.csrfProtect(a.HandleNotifyConfig))))
	mux.Handle("POST /notifications/test", a.requireLogin(a.csrfProtect(a.HandleSendTest)))
	mux.Handle("GET /profile", a.requireLogin(a.HandleProfile))
	mux.Handle("POST /profile", a.requireLogin(a.csrfProtect(a.HandleProfile)))

	// ---- 管理端 ----
	mux.Handle("GET /admin", a.requireAdmin(a.HandleAdminHome))
	mux.Handle("POST /admin/cron", a.requireAdmin(a.csrfProtect(a.HandleAdminCron)))
	mux.Handle("GET /admin/users", a.requireAdmin(a.HandleAdminUsers))
	mux.Handle("POST /admin/users", a.requireAdmin(a.csrfProtect(a.HandleAdminUsers)))
	mux.Handle("GET /admin/numbers", a.requireAdmin(a.HandleAdminNumbers))
	mux.Handle("GET /admin/export", a.requireAdmin(a.HandleAdminExport))
	mux.Handle("GET /admin/settings", a.requireAdmin(a.HandleAdminSettings))
	mux.Handle("POST /admin/settings", a.requireAdmin(a.csrfProtect(a.HandleAdminSettings)))

	return a.sessionMiddleware(mux)
}

// ensureSession 保证请求有会话可承载 CSRF 令牌：没有就建一条匿名预会话并种 Cookie。
// 返回带新上下文的请求。仅用于登录/注册/初始化这类「访问者还没有会话」的页面。
func (a *App) ensureSession(w http.ResponseWriter, r *http.Request) *http.Request {
	if a.currentSession(r) != nil {
		return r
	}
	sess, err := a.Sessions.CreateAnonymous(a.ClientIP(r), r.UserAgent())
	if err != nil {
		log.Printf("[web] 建立匿名会话失败: %v", err)
		return r
	}
	http.SetCookie(w, a.sessionCookie(r, sess.Token, int(auth.SessionTTL/time.Second)))
	return r.WithContext(withCtx(r, sess, nil))
}

// handleLoginPage GET /login：确保匿名会话存在，页面才能携带 CSRF 令牌。
func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if a.currentUser(r) == nil {
		r = a.ensureSession(w, r)
	}
	a.HandleLogin(w, r)
}

// handleSetupPage GET /setup：同上，且仅零用户时可见。
func (a *App) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	r = a.ensureSession(w, r)
	a.HandleSetup(w, r)
}

// limitBody 限制请求体大小（tdata 上传等大请求体的兜底）。
func (a *App) limitBody(n int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next(w, r)
	}
}

// ensureSessionMW 为「访问者尚无会话」的请求兜底建匿名预会话（承载 CSRF 令牌）。
func (a *App) ensureSessionMW(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		next(w, a.ensureSession(w, r))
	}
}
