package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"simkeeper/internal/cronjob"
	"simkeeper/internal/store"
)

// AdminStats 管理端首页统计。
type AdminStats struct {
	ActiveUsers  int
	AdminCount   int
	Numbers      int
	Expiring7    int
	SentTotal    int
	FailedTotal  int
	LastCronRun  string
	RecentNotifs []store.Notification
}

// HandleAdminHome 管理端首页。
func (a *App) HandleAdminHome(w http.ResponseWriter, r *http.Request) {
	d := a.baseData(r, "管理后台")
	d.ActiveNav = "admin"
	stats := AdminStats{}
	today := time.Now()

	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE status='active'`).Scan(&stats.ActiveUsers)
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin' AND status='active'`).Scan(&stats.AdminCount)
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM phone_numbers WHERE status='active'`).Scan(&stats.Numbers)
	_ = a.DB.QueryRow(
		`SELECT COUNT(*) FROM phone_numbers WHERE status='active' AND expiry_date >= ? AND expiry_date <= ?`,
		today.Format(dateLayout), today.AddDate(0, 0, 7).Format(dateLayout),
	).Scan(&stats.Expiring7)
	_ = a.DB.QueryRow(`SELECT setting_value FROM system_settings WHERE setting_key='total_sent'`).Scan(&stats.SentTotal)
	_ = a.DB.QueryRow(`SELECT setting_value FROM system_settings WHERE setting_key='last_cron_run'`).Scan(&stats.LastCronRun)
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE status='failed'`).Scan(&stats.FailedTotal)
	stats.RecentNotifs, _, _ = a.Notify.ListForAdmin(1, 8, "")

	d.Content = stats
	a.render(w, http.StatusOK, "page_admin_home", d)
}

// AdminUsersPage 用户管理列表数据。
type AdminUsersPage struct {
	Users  []store.User
	Total  int
	Page   int
	Pages  int
	Search string
}

// HandleAdminUsers 用户管理：列表 + 封禁/解禁/删除/角色。
// 写操作全部 POST + CSRF + 自身/最后管理员保护（修复 PHP 版可删光管理员的问题）。
func (a *App) HandleAdminUsers(w http.ResponseWriter, r *http.Request) {
	d := a.baseData(r, "用户管理")
	d.ActiveNav = "admin/users"
	me := a.currentUser(r)

	if r.Method == http.MethodPost {
		a.adminUserAction(w, r, me.ID, &d)
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	pageNum := atoiDefault(r.URL.Query().Get("page"), 1)
	users, total, err := a.Users.List(pageNum, 20, search)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	pages := (total + 19) / 20
	if pages < 1 {
		pages = 1
	}
	d.Content = AdminUsersPage{Users: users, Total: total, Page: pageNum, Pages: pages, Search: search}
	a.render(w, http.StatusOK, "page_admin_users", d)
}

// adminUserAction 处理单个用户管理动作。
func (a *App) adminUserAction(w http.ResponseWriter, r *http.Request, myID int64, d *pageData) {
	action := r.PostFormValue("action")
	targetID, _ := strconv.ParseInt(r.PostFormValue("user_id"), 10, 64)
	back := func(msg string, isErr bool) {
		a.setFlash(w, msg, isErr)
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
	if targetID <= 0 {
		back("无效的用户", true)
		return
	}
	target, err := a.Users.ByID(targetID)
	if err != nil {
		back("用户不存在", true)
		return
	}
	// 保护规则：不能操作自己（避免误删/误封当前登录会话）
	if targetID == myID {
		back("不能对当前登录账号执行该操作", true)
		return
	}

	switch action {
	case "ban", "delete":
		// 保护规则：不允许封禁/删除最后一名 active 管理员
		if target.Role == "admin" {
			n, err := a.Users.CountAdmins()
			if err != nil || n <= 1 {
				back("系统至少需要保留一名管理员", true)
				return
			}
		}
		if action == "ban" {
			if err := a.Users.SetStatus(targetID, "banned"); err != nil {
				back("操作失败", true)
				return
			}
			_ = a.Sessions.DestroyForUser(targetID)
			back("用户已封禁", false)
		} else {
			if err := a.Users.Delete(targetID); err != nil {
				back("删除失败", true)
				return
			}
			back("用户已删除", false)
		}
	case "unban":
		if err := a.Users.SetStatus(targetID, "active"); err != nil {
			back("操作失败", true)
			return
		}
		back("用户已解禁", false)
	case "role":
		role := r.PostFormValue("role")
		if role != "user" && role != "admin" {
			back("无效的角色", true)
			return
		}
		// 降级最后一名管理员同样不允许
		if target.Role == "admin" && role == "user" {
			n, err := a.Users.CountAdmins()
			if err != nil || n <= 1 {
				back("系统至少需要保留一名管理员", true)
				return
			}
		}
		if err := a.Users.SetRole(targetID, role); err != nil {
			back("操作失败", true)
			return
		}
		_ = a.Sessions.DestroyForUser(targetID)
		back("角色已调整", false)
	default:
		back("未知操作", true)
	}
}

// AdminNumbersPage 管理端号码列表数据。
type AdminNumbersPage struct {
	Numbers []store.PhoneNumber
	Total   int
	Page    int
	Pages   int
	Search  string
}

// HandleAdminNumbers 管理端号码列表。
func (a *App) HandleAdminNumbers(w http.ResponseWriter, r *http.Request) {
	d := a.baseData(r, "全部号码")
	d.ActiveNav = "admin/numbers"
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	pageNum := atoiDefault(r.URL.Query().Get("page"), 1)
	numbers, total, err := a.Numbers.ListForAdmin(pageNum, 20, search)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	pages := (total + 19) / 20
	if pages < 1 {
		pages = 1
	}
	d.Content = AdminNumbersPage{Numbers: numbers, Total: total, Page: pageNum, Pages: pages, Search: search}
	a.render(w, http.StatusOK, "page_admin_numbers", d)
}

// HandleAdminExport 导出全部号码 CSV（管理端）。
func (a *App) HandleAdminExport(w http.ResponseWriter, r *http.Request) {
	numbers, _, err := a.Numbers.ListForAdmin(1, 1000000, "")
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString("号码,用户ID,国家代码,国家,运营商,到期日,剩余天数,状态\n")
	for _, n := range numbers {
		b.WriteString(csvRow(n.PhoneNumber, strconv.FormatInt(n.UserID, 10), n.CountryCode,
			n.CountryName, n.Carrier, n.ExpiryDate,
			strconv.Itoa(n.DaysLeft(time.Now())), n.Status))
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=all_numbers.csv")
	_, _ = w.Write([]byte(b.String()))
}

// AdminSettingsPage 系统设置页数据。
type AdminSettingsPage struct {
	SiteName      string
	MaxNumbers    int
	RetentionDays int
	AllowReg      bool
}

// HandleAdminSettings 系统设置（GET 展示 / POST 保存）。
// 修复 PHP 版 reset_stats 引用不存在列的问题：Go 版不提供该坏功能。
func (a *App) HandleAdminSettings(w http.ResponseWriter, r *http.Request) {
	d := a.baseData(r, "系统设置")
	d.ActiveNav = "admin/settings"

	if r.Method == http.MethodPost {
		siteName := strings.TrimSpace(r.PostFormValue("site_name"))
		maxNumbers := atoiDefault(r.PostFormValue("max_numbers_per_user"), 50)
		retention := atoiDefault(r.PostFormValue("log_retention_days"), 90)
		allowReg := r.PostFormValue("allow_registration") == "1"
		if siteName == "" {
			siteName = "保号通"
		}
		if maxNumbers < 1 {
			maxNumbers = 50
		}
		if retention < 1 {
			retention = 90
		}
		sets := [][2]string{
			{"site_name", siteName},
			{"max_numbers_per_user", strconv.Itoa(maxNumbers)},
			{"log_retention_days", strconv.Itoa(retention)},
			{"allow_registration", boolStr(allowReg)},
		}
		for _, kv := range sets {
			if err := a.Settings.Set(kv[0], kv[1]); err != nil {
				d.Flash, d.FlashIsErr = "保存失败："+kv[0], true
				a.render(w, http.StatusOK, "page_admin_settings", d)
				return
			}
		}
		a.setFlash(w, "设置已保存", false)
		http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
		return
	}
	page := AdminSettingsPage{
		SiteName:      getStr(a, "site_name", "保号通"),
		MaxNumbers:    a.Settings.GetInt("max_numbers_per_user", 50),
		RetentionDays: a.Settings.GetInt("log_retention_days", 90),
		AllowReg:      a.Settings.GetInt("allow_registration", 1) == 1,
	}
	d.Content = page
	a.render(w, http.StatusOK, "page_admin_settings", d)
}

// HandleAdminCron 手动触发一次定时任务（POST + CSRF）。
// 修复 PHP 版 GET 直接触发 cron 的问题。
func (a *App) HandleAdminCron(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}
	jobs := cronjob.New(a.DB)
	sent, failed, lines := jobs.RunOnce(time.Now())
	a.setFlash(w, fmt.Sprintf("任务完成：成功 %d，失败 %d（详见服务端日志）", sent, failed), failed > 0 && sent == 0)
	_ = lines
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// getStr 读取设置的便捷封装。
func getStr(a *App, key, fallback string) string {
	v, err := a.Settings.Get(key)
	if err != nil || v == "" {
		return fallback
	}
	return v
}

// boolStr 布尔转 0/1 字符串。
func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
