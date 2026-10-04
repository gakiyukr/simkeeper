package auth

import (
	"crypto/subtle"
	"net/http"
)

// CSRF 校验失败时返回的哨兵错误；由中间件统一转成 403。
type CSRFError struct{}

func (e *CSRFError) Error() string { return "CSRF 校验失败" }

// VerifyCSRF 校验 POST 请求携带的 CSRF 令牌与会话中的令牌一致。
// 令牌可放在表单字段 csrf_token 或请求头 X-CSRF-Token。
// 只有写操作（POST/PUT/PATCH/DELETE）需要校验；GET 直接放行，
// 但 Go 版所有状态变更一律走 POST，不存在 PHP 版 GET 触发 cron 的问题。
func VerifyCSRF(r *http.Request, sess *Session) bool {
	if sess == nil {
		return false
	}
	var sent string
	if r.Method == http.MethodPost || r.Method == http.MethodPut ||
		r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		sent = r.PostFormValue("csrf_token")
		if sent == "" {
			sent = r.Header.Get("X-CSRF-Token")
		}
	}
	if sent == "" || sess.CSRFToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sent), []byte(sess.CSRFToken)) == 1
}
