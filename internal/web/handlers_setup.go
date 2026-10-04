package web

import (
	"net/http"
	"strings"

	"baohaotong/internal/auth"
)

// HandleSetup 初始化管理员。
// 仅当系统里一个用户都没有时可用——等价于 PHP 版安装向导的「创建管理员」步骤；
// 一旦存在任何账号，本入口永久失效（返回 404），不能被用来接管运行中的系统。
func (a *App) HandleSetup(w http.ResponseWriter, r *http.Request) {
	d := a.baseData(r, "初始化管理员")

	_, total, err := a.Users.List(1, 1, "")
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	if total > 0 {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.PostFormValue("username"))
		email := strings.TrimSpace(r.PostFormValue("email"))
		password := r.PostFormValue("password")
		confirm := r.PostFormValue("confirm_password")

		switch {
		case username == "" || email == "" || password == "":
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
		if d.Flash == "" {
			hash, err := auth.HashPassword(password)
			if err != nil {
				http.Error(w, "内部错误", http.StatusInternalServerError)
				return
			}
			if _, err := a.Users.Create(username, email, hash, "admin"); err != nil {
				d.Flash, d.FlashIsErr = "创建失败（用户名或邮箱可能已被占用）", true
			} else {
				a.setFlash(w, "管理员已创建，请登录", false)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
		}
	}

	a.render(w, http.StatusOK, "page_setup", d)
}
