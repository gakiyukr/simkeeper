package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"simkeeper/internal/store"
)

// 手机号格式：可选 + 开头，1-9 起始，总长符合 E.164 粗校验。
var phoneRe = regexp.MustCompile(`^\+?[1-9]\d{1,14}$`)

// 日期格式。
const dateLayout = "2006-01-02"

// Country 下拉选项（Code 为 ISO 3166 代码，与 PHP 版 getCountries 语义一致）。
type Country struct {
	Code string
	Name string
}

// Countries 预置国家/地区列表，覆盖常见 eSIM/境外号码来源地，可按需扩充。
var Countries = []Country{
	{"CN", "中国"}, {"HK", "香港"}, {"MO", "澳门"}, {"TW", "台湾"},
	{"JP", "日本"}, {"KR", "韩国"}, {"SG", "新加坡"}, {"MY", "马来西亚"},
	{"TH", "泰国"}, {"VN", "越南"}, {"PH", "菲律宾"}, {"ID", "印度尼西亚"},
	{"IN", "印度"}, {"KH", "柬埔寨"}, {"MM", "缅甸"}, {"LA", "老挝"},
	{"MV", "马尔代夫"}, {"PK", "巴基斯坦"}, {"BD", "孟加拉国"}, {"NP", "尼泊尔"},
	{"AE", "阿联酋"}, {"SA", "沙特阿拉伯"}, {"QA", "卡塔尔"}, {"IL", "以色列"},
	{"TR", "土耳其"}, {"KZ", "哈萨克斯坦"},
	{"GB", "英国"}, {"DE", "德国"}, {"FR", "法国"}, {"IT", "意大利"},
	{"ES", "西班牙"}, {"PT", "葡萄牙"}, {"NL", "荷兰"}, {"BE", "比利时"},
	{"CH", "瑞士"}, {"AT", "奥地利"}, {"SE", "瑞典"}, {"NO", "挪威"},
	{"DK", "丹麦"}, {"FI", "芬兰"}, {"IE", "爱尔兰"}, {"IS", "冰岛"},
	{"PL", "波兰"}, {"CZ", "捷克"}, {"HU", "匈牙利"}, {"GR", "希腊"},
	{"RO", "罗马尼亚"}, {"BG", "保加利亚"}, {"HR", "克罗地亚"}, {"UA", "乌克兰"},
	{"RU", "俄罗斯"}, {"LT", "立陶宛"}, {"LV", "拉脱维亚"}, {"EE", "爱沙尼亚"},
	{"US", "美国"}, {"CA", "加拿大"}, {"MX", "墨西哥"}, {"BR", "巴西"},
	{"AR", "阿根廷"}, {"CL", "智利"}, {"CO", "哥伦比亚"}, {"PE", "秘鲁"},
	{"AU", "澳大利亚"}, {"NZ", "新西兰"}, {"FJ", "斐济"},
	{"ZA", "南非"}, {"EG", "埃及"}, {"KE", "肯尼亚"}, {"NG", "尼日利亚"},
	{"MA", "摩洛哥"}, {"MU", "毛里求斯"},
}

// Carriers 常见运营商候选（表单 datalist，可自由输入）。
var Carriers = []string{
	"China Mobile", "China Unicom", "China Telecom", "CSL", "SmarTone", "3HK",
	"Chunghwa Telecom", "Taiwan Mobile", "FarEasTone",
	"NTT DoCoMo", "SoftBank", "au", "SK Telecom", "KT", "LG U+",
	"Singtel", "StarHub", "M1", "Maxis", "Celcom", "Digi",
	"AIS", "dtac", "TrueMove", "Telkomsel", "Indosat", "XL Axiata",
	"Globe", "Smart", "DITO", "Viettel", "Vinaphone", "MobiFone",
	"Airtel", "Jio", "Vodafone Idea", "Etisalat", "du",
	"Vodafone", "EE", "O2", "Three", "Orange", "SFR", "Bouygues",
	"Deutsche Telekom", "Telefónica", "T-Mobile", "AT&T", "Verizon",
	"Rogers", "Bell", "Telus", "Telstra", "Optus", "Turkcell",
}

// countryNameByCode 按 ISO 代码查名称；未收录返回空串。
func countryNameByCode(code string) string {
	for _, c := range Countries {
		if c.Code == code {
			return c.Name
		}
	}
	return ""
}

// normalizePhone 规范化手机号：去空白与分隔符，缺 + 自动补（与 PHP 版 formatPhoneNumber 一致）。
func normalizePhone(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9', r == '+':
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s != "" && !strings.HasPrefix(s, "+") {
		s = "+" + s
	}
	return s
}

// DashPage 首页数据：统计 + 完整号码管理（分类/搜索/排序/分页）+ 最近通知。
type DashPage struct {
	Numbers      []store.PhoneNumber // 当前分类+搜索+排序+分页后的号码
	Total        int                 // 过滤后的号码数（分页用）
	TotalAll     int                 // 全部号码数（页头展示）
	Page         int
	Pages        int
	Search       string
	Cat          string
	Sort         string
	Counts       CategoryCounts
	Expiring7    int // 7 天内到期（含今天）
	ActiveCnt    int
	RecentNotifs []store.Notification
}


// HandleDashboard 首页 = 号码管理主页：统计 + 完整号码管理 + 最近通知。
// 号码是本程序的核心功能，独立的管理页已并入首页（/numbers 重定向到 /）。
func (a *App) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	d := a.baseData(r, "号码管理")
	d.ActiveNav = ""

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	cat := r.URL.Query().Get("cat")
	sortKey := r.URL.Query().Get("sort")
	all, _, err := a.Numbers.ListForUser(u.ID, 1, 1000000)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	today := time.Now()
	counts := categoryCounts(all, today)
	expiring7, activeCnt := 0, 0
	for i := range all {
		if all[i].NoKeepalive {
			continue
		}
		days := all[i].DaysLeft(today)
		if days >= 0 && days <= 7 {
			expiring7++
		}
		if all[i].Status == "active" {
			activeCnt++
		}
	}
	filtered := filterSortNumbers(all, q, cat, sortKey)
	total := len(filtered)
	pageNum := atoiDefault(r.URL.Query().Get("page"), 1)
	pages := (total + 19) / 20
	if pages < 1 {
		pages = 1
	}
	if pageNum < 1 {
		pageNum = 1
	}
	if pageNum > pages {
		pageNum = pages
	}
	start := (pageNum - 1) * 20
	end := start + 20
	if end > total {
		end = total
	}
	recent, _, _ := a.Notify.ListForUser(u.ID, 1, 5)
	d.Content = DashPage{
		Numbers: filtered[start:end], Total: total, TotalAll: len(all),
		Page: pageNum, Pages: pages, Search: q, Cat: cat, Sort: sortKey,
		Counts: counts, Expiring7: expiring7, ActiveCnt: activeCnt,
		RecentNotifs: recent,
	}
	a.render(w, http.StatusOK, "page_dashboard", d)
}

// CategoryCounts 号码三类保号分类 + 号码已丢失的数量（互斥，合计 = 总数）。
type CategoryCounts struct {
	Cycle   int // 需要周期性保号（活跃、未过期）
	None    int // 无需保号
	Lost    int // 号码已丢失（仅作记录）
	Expired int // 已过期
}

// categoryCounts 按优先级归类：无需保号 > 号码已丢失 > 已过期 > 周期保号。
// 丢失是用户显式标记的记录，即使其到期日已过也归入丢失，避免找不到。
func categoryCounts(nums []store.PhoneNumber, now time.Time) CategoryCounts {
	var c CategoryCounts
	for i := range nums {
		switch {
		case nums[i].NoKeepalive:
			c.None++
		case nums[i].Status == "inactive":
			c.Lost++
		case nums[i].DaysLeft(now) < 0:
			c.Expired++
		default:
			c.Cycle++
		}
	}
	return c
}

// HandleNumbers 旧号码管理页已并入首页，永久重定向。
func (a *App) HandleNumbers(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusMovedPermanently)
}

// filterSortNumbers 纯函数：关键词匹配号码/国家/运营商/备注（不区分大小写），
// 分类过滤（与 categoryCounts 的优先级口径一致），按到期时间（默认升序）/
// 降序/最新添加排序；无需保号号码在到期排序中排最后；平局按 ID 保证稳定。
func filterSortNumbers(all []store.PhoneNumber, q, cat, sortKey string) []store.PhoneNumber {
	q = strings.ToLower(strings.TrimSpace(q))
	now := time.Now()
	out := make([]store.PhoneNumber, 0, len(all))
	for _, n := range all {
		expired := n.DaysLeft(now) < 0
		switch cat {
		case "none":
			if !n.NoKeepalive {
				continue
			}
		case "inactive": // 号码已丢失（优先于已过期）
			if n.NoKeepalive || n.Status != "inactive" {
				continue
			}
		case "expired":
			if n.NoKeepalive || n.Status == "inactive" || !expired {
				continue
			}
		case "cycle":
			if n.NoKeepalive || n.Status == "inactive" || expired {
				continue
			}
		}
		if q != "" {
			hay := strings.ToLower(n.PhoneNumber + " " + n.CountryName + " " + n.CountryCode + " " + n.Carrier + " " + n.Notes)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, n)
	}
	// 无需保号号码的到期日仅作参考，任何到期排序中都排最后
	byExpiry := func(desc bool) func(i, j int) bool {
		return func(i, j int) bool {
			if out[i].NoKeepalive != out[j].NoKeepalive {
				return out[j].NoKeepalive // 非无需保号在前
			}
			if out[i].ExpiryDate != out[j].ExpiryDate {
				if desc {
					return out[i].ExpiryDate > out[j].ExpiryDate
				}
				return out[i].ExpiryDate < out[j].ExpiryDate
			}
			return out[i].ID < out[j].ID
		}
	}
	var less func(i, j int) bool
	switch sortKey {
	case "expiry_desc":
		less = byExpiry(true)
	case "created_desc":
		less = func(i, j int) bool {
			if out[i].CreatedAt != out[j].CreatedAt {
				return out[i].CreatedAt > out[j].CreatedAt
			}
			return out[i].ID > out[j].ID
		}
	default: // expiry_asc
		less = byExpiry(false)
	}
	sort.Slice(out, less)
	return out
}

// HandleNumberNew 新增号码表单。
func (a *App) HandleNumberNew(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	d := a.baseData(r, "新增号码")
	if r.Method == http.MethodPost {
		a.saveNumberForm(w, r, u.ID, nil, &d)
		return
	}
	d.Content = map[string]any{"Countries": Countries, "Carriers": Carriers}
	a.render(w, http.StatusOK, "page_number_form", d)
}

// HandleNumberEdit 编辑号码；id 归属校验内置于 ByID。
func (a *App) HandleNumberEdit(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	d := a.baseData(r, "编辑号码")
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	number, err := a.Numbers.ByID(id, u.ID)
	if err != nil {
		a.setFlash(w, "号码不存在", true)
		http.Redirect(w, r, "/numbers", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		a.saveNumberForm(w, r, u.ID, number, &d)
		return
	}
	d.Content = map[string]any{
		"Countries": Countries, "Carriers": Carriers, "N": number, "Country": number.CountryCode,
	}
	a.render(w, http.StatusOK, "page_number_form", d)
}

// saveNumberForm 解析并校验号码表单；n 为 nil 表示新增。
// 输出层由 html/template 自动转义；输入在此统一校验与规范化。
func (a *App) saveNumberForm(w http.ResponseWriter, r *http.Request, userID int64, existing *store.PhoneNumber, d *pageData) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "请求体解析失败", http.StatusBadRequest)
		return
	}

	phone := normalizePhone(r.PostFormValue("phone_number"))
	countryCode := strings.TrimSpace(r.PostFormValue("country_code"))
	countryName := countryNameByCode(countryCode)
	carrier := strings.TrimSpace(r.PostFormValue("carrier"))
	expiry := strings.TrimSpace(r.PostFormValue("expiry_date"))
	amount, _ := strconv.ParseFloat(r.PostFormValue("recharge_amount"), 64)
	currency := strings.TrimSpace(r.PostFormValue("recharge_currency"))
	if currency == "" {
		currency = "USD"
	}
	renewalDays := atoiDefault(r.PostFormValue("renewal_days_before"), 7)
	usageDays := atoiDefault(r.PostFormValue("usage_days_before"), 3)
	notes := strings.TrimSpace(r.PostFormValue("notes"))
	status := r.PostFormValue("status")
	if status != "inactive" {
		status = "active"
	}
	// 保号方式二选一：keep 需要周期性保号 / none 无需保号。
	// 需要保号的号码以「下次续费日期 + 周期」定义（保号本身就是循环操作）：
	// 下次续费日期即到期日；周期起点（auto_start_date）由 到期日-周期 推导存储，
	// 滚动只在用户点「已续费」时发生。
	mode := r.PostFormValue("keepalive_mode")
	noKeepalive := mode == "none"
	autoPeriod := atoiDefault(r.PostFormValue("auto_expiry_period"), 0)
	autoStart := ""
	if noKeepalive {
		// 无需保号：清空周期字段，不录入到期日
		expiry, autoPeriod = "", 0
	}
	autoEnabled := !noKeepalive

	// 校验失败回显用：以本次提交值构造实体——报错后表单不清空；
	// 编辑模式下沿用原 ID（模板据 ID 区分编辑/新增）且回显用户改过的值而非库里旧值。
	submitted := &store.PhoneNumber{
		UserID: userID, PhoneNumber: phone, CountryCode: countryCode, CountryName: countryName,
		Carrier: carrier, ExpiryDate: expiry,
		RechargeAmount: amount, RechargeCurrency: currency,
		RenewalDaysBefore: renewalDays, UsageDaysBefore: usageDays,
		AutoExpiryEnabled: autoEnabled, AutoStartDate: autoStart,
		AutoExpiryPeriod: autoPeriod, Status: status, Notes: notes,
		NoKeepalive: noKeepalive,
	}
	if existing != nil {
		submitted.ID = existing.ID
	}
	form := map[string]any{"Countries": Countries, "Carriers": Carriers, "N": submitted, "Country": countryCode}
	fail := func(msg string) {
		d.Flash, d.FlashIsErr = msg, true
		d.Content = form
		a.render(w, http.StatusOK, "page_number_form", *d)
	}

	var expiryDate time.Time
	switch {
	case phone == "" || !phoneRe.MatchString(phone):
		fail("号码格式不正确（示例：+8613800138000 或 13800138000）")
		return
	case countryName == "":
		fail("请选择国家/地区")
		return
	}

	if noKeepalive {
		// 无需保号：不录入到期日，不参与任何提醒与滚动
		expiry = ""
	} else {
		// 需要周期性保号：下次续费日期即到期日；周期起点 = 到期日 - 周期
		if expiry == "" {
			fail("需要周期性保号的号码需填写下次续费日期")
			return
		}
		var err error
		expiryDate, err = time.ParseInLocation(dateLayout, expiry, time.Local)
		if err != nil {
			fail("下次续费日期格式应为 YYYY-MM-DD")
			return
		}
		if autoPeriod < 1 || autoPeriod > 3650 {
			fail("续费周期应为 1-3650 天")
			return
		}
		autoStart = expiryDate.AddDate(0, 0, -autoPeriod).Format(dateLayout)
	}
	if renewalDays < 1 || renewalDays > 90 {
		fail("续费提醒提前天数应为 1-90")
		return
	}
	if usageDays < 1 || usageDays > 90 {
		fail("使用提醒提前天数应为 1-90")
		return
	}

	n := &store.PhoneNumber{
		UserID: userID, PhoneNumber: phone, CountryCode: countryCode, CountryName: countryName,
		Carrier: carrier, ExpiryDate: expiry,
		RechargeAmount: amount, RechargeCurrency: currency,
		RenewalDaysBefore: renewalDays, UsageDaysBefore: usageDays,
		AutoExpiryEnabled: autoEnabled, AutoStartDate: autoStart,
		AutoExpiryPeriod: autoPeriod, Status: status, Notes: notes,
		NoKeepalive: noKeepalive,
	}
	if autoEnabled {
		n.AutoCalculatedExpiry = expiry
	}
	if existing != nil {
		n.ID = existing.ID
		if cnt, err := a.Numbers.CountDuplicate(userID, phone, n.ID); err == nil && cnt > 0 {
			fail("该号码已存在于你的列表中")
			return
		}
		if err := a.Numbers.Update(n); err != nil {
			fail("保存失败，请重试")
			return
		}
		a.setFlash(w, "号码已更新", false)
	} else {
		// 上限校验只在新增时做
		maxNumbers := a.Settings.GetInt("max_numbers_per_user", 50)
		if cnt, err := a.Numbers.CountForUser(userID); err == nil && cnt >= maxNumbers {
			fail("每个用户最多可添加 " + strconv.Itoa(maxNumbers) + " 个号码")
			return
		}
		if cnt, err := a.Numbers.CountDuplicate(userID, phone, 0); err == nil && cnt > 0 {
			fail("该号码已存在于你的列表中")
			return
		}
		if _, err := a.Numbers.Create(n); err != nil {
			fail("保存失败，请重试")
			return
		}
		a.setFlash(w, "号码已添加", false)
	}
	http.Redirect(w, r, "/numbers", http.StatusSeeOther)
}

// HandleNumberRenew 标记已续费：这是周期号码唯一的滚动机制——
// 未过期时按原到期日锚定顺延一个周期（提前续费不吃亏），
// 已过期则从今天起算（复活）；周期起点同步更新为新周期的开始。
// GET 访问直接回列表。
func (a *App) HandleNumberRenew(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	back := func(msg string, isErr bool) {
		a.setFlash(w, msg, isErr)
		http.Redirect(w, r, "/numbers", http.StatusSeeOther)
	}
	if r.Method != http.MethodPost {
		back("", false)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	n, err := a.Numbers.ByID(id, u.ID)
	if err != nil {
		back("号码不存在", true)
		return
	}
	if n.AutoExpiryPeriod <= 0 {
		back("该号码未设置续费周期，请先编辑补填", true)
		return
	}
	today := time.Now()
	anchor := today
	if t, err := time.ParseInLocation(dateLayout, n.ExpiryDate, time.Local); err == nil && t.After(today) {
		anchor = t // 未过期：锚定原到期日，周期首尾相接
	}
	newStart := anchor.Format(dateLayout)
	newExpiry := anchor.AddDate(0, 0, n.AutoExpiryPeriod).Format(dateLayout)
	if _, err := a.Numbers.MarkRenewed(n.ID, u.ID, newExpiry, newStart); err != nil {
		back("操作失败", true)
		return
	}
	back(fmt.Sprintf("已标记续费，到期日更新至 %s", newExpiry), false)
}

// HandleNumberSetStatus 快捷切换号码状态：标记丢失（仅作记录）↔ 恢复使用。
// 丢失的号码不参与提醒与配额，可随时恢复。
func (a *App) HandleNumberSetStatus(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	back := func(msg string, isErr bool) {
		a.setFlash(w, msg, isErr)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
	if r.Method != http.MethodPost {
		back("", false)
		return
	}
	status := r.PostFormValue("status")
	if status != "active" && status != "inactive" {
		back("无效的状态", true)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, err := a.Numbers.ByID(id, u.ID); err != nil {
		back("号码不存在", true)
		return
	}
	if _, err := a.Numbers.SetStatus(id, u.ID, status); err != nil {
		back("操作失败", true)
		return
	}
	if status == "inactive" {
		back("已标记为号码丢失（仅作记录，不再提醒）", false)
		return
	}
	back("已恢复使用", false)
}

// HandleNumberDelete 删除号码（POST + CSRF + 归属校验）。
func (a *App) HandleNumberDelete(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, err := a.Numbers.ByID(id, u.ID); err != nil {
		a.setFlash(w, "号码不存在", true)
	} else if err := a.Numbers.Delete(id, u.ID); err != nil {
		a.setFlash(w, "删除失败", true)
	} else {
		a.setFlash(w, "号码已删除", false)
	}
	http.Redirect(w, r, "/numbers", http.StatusSeeOther)
}

// NotificationsPage 通知历史页数据。
type NotificationsPage struct {
	Items []store.Notification
	Total int
	Page  int
	Pages int
}

// HandleNotificationHistory 用户通知历史。
func (a *App) HandleNotificationHistory(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	d := a.baseData(r, "通知历史")
	d.ActiveNav = "notifications"
	pageNum := atoiDefault(r.URL.Query().Get("page"), 1)
	items, total, err := a.Notify.ListForUser(u.ID, pageNum, 20)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	pages := (total + 19) / 20
	if pages < 1 {
		pages = 1
	}
	d.Content = NotificationsPage{Items: items, Total: total, Page: pageNum, Pages: pages}
	a.render(w, http.StatusOK, "page_notifications", d)
}

// 剩余天数徽标配色（模板直接调用）。
func daysBadge(days int) string {
	switch {
	case days < 0:
		return "badge-danger badge-blink" // 已过期：红色闪烁提示
	case days <= 3:
		return "badge-danger"
	case days <= 7:
		return "badge-warning"
	default:
		return "badge-ok"
	}
}

// HandleHistoryExport 导出用户号码 CSV。
func (a *App) HandleHistoryExport(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	numbers, _, err := a.Numbers.ListForUser(u.ID, 1, 100000)
	if err != nil {
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	// UTF-8 BOM：让 Excel 正确识别中文
	b.WriteString("\xef\xbb\xbf")
	b.WriteString("号码,国家代码,国家,运营商,到期日,剩余天数,状态,充值金额,币种\n")
	for _, n := range numbers {
		b.WriteString(csvRow(n.PhoneNumber, n.CountryCode, n.CountryName, n.Carrier,
			n.ExpiryDate, strconv.Itoa(n.DaysLeft(time.Now())), n.Status,
			strconv.FormatFloat(n.RechargeAmount, 'f', -1, 64), n.RechargeCurrency))
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=numbers.csv")
	_, _ = w.Write([]byte(b.String()))
}

// csvRow 拼 CSV 行并转义含逗号/引号的字段。
func csvRow(fields ...string) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		if strings.ContainsAny(f, ",\"\n") {
			f = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		}
		out[i] = f
	}
	return strings.Join(out, ",") + "\n"
}

// atoiDefault 字符串转 int，失败返回 fallback。
func atoiDefault(s string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return n
}

// 模板函数表：供 html/template 注册。
var funcMap = template.FuncMap{
	"daysBadge": daysBadge,
	// daysLeft 只收号码一个参数；now 由函数内部取，模板里直接 {{daysLeft .}}
	"daysLeft":  func(n store.PhoneNumber) int { return n.DaysLeft(time.Now()) },
	"add":       func(a, b int) int { return a + b },
	"sub":       func(a, b int) int { return a - b },
	"hasPrefix": strings.HasPrefix,
	"snippet": func(s string) string {
		runes := []rune(strings.TrimSpace(s))
		if len(runes) <= 42 {
			return string(runes)
		}
		return string(runes[:42]) + "…"
	},
	"statusText": func(s string) string {
		if s == "inactive" {
			return "号码已丢失"
		}
		return "使用中"
	},
	"channelText": func(c string) string {
		switch c {
		case "email":
			return "邮件"
		case "telegram":
			return "Telegram"
		case "wxpusher":
			return "WxPusher"
		case "feishu":
			return "飞书"
		case "dingtalk":
			return "钉钉"
		case "tgcall":
			return "TG 电话"
		}
		return "系统"
	},
	"typeText": func(t string) string {
		switch t {
		case "renewal":
			return "续费提醒"
		case "usage":
			return "使用提醒"
		case "test":
			return "测试"
		}
		return "系统"
	},
	"statusBadge": func(s string) string {
		switch s {
		case "sent":
			return "badge-ok"
		case "failed":
			return "badge-danger"
		}
		return "badge-muted"
	},
	"statusTextN": func(s string) string {
		switch s {
		case "sent":
			return "已发送"
		case "failed":
			return "失败"
		case "pending":
			return "待发送"
		}
		return s
	},
	// cycleProgress 当前周期的时间进度（0-100），周期起点按「到期日 - 周期」
	// 推算（与自动滚动口径一致，滚动后依旧正确）；非周期号码返回 -1，模板不渲染进度条。
	"cycleProgress": func(n store.PhoneNumber) int {
		if n.AutoExpiryPeriod <= 0 {
			return -1
		}
		expiry, err := time.ParseInLocation(dateLayout, n.ExpiryDate, time.Local)
		if err != nil {
			return -1
		}
		now := time.Now()
		if now.After(expiry) {
			return 100
		}
		start := expiry.AddDate(0, 0, -n.AutoExpiryPeriod)
		total := expiry.Sub(start).Hours()
		if total <= 0 {
			return 100
		}
		pct := int(now.Sub(start).Hours() / total * 100)
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		return pct
	},
	// fillClass 进度条填充色：与剩余天数徽章同色系（>7 绿 / 4-7 黄 / <=3 红）。
	"fillClass": func(days int) string {
		switch {
		case days <= 3:
			return "fill-err"
		case days <= 7:
			return "fill-warn"
		}
		return "fill-ok"
	},
	// querySuffix 拼接列表链接的查询串（chips 切换分类时保留搜索词与排序）。
	"querySuffix": func(q, sort, cat string) template.URL {
		var b strings.Builder
		b.WriteString("?")
		if q != "" {
			b.WriteString("q=" + url.QueryEscape(q) + "&")
		}
		if sort != "" {
			b.WriteString("sort=" + url.QueryEscape(sort) + "&")
		}
		if cat != "" {
			b.WriteString("cat=" + url.QueryEscape(cat) + "&")
		}
		return template.URL(b.String())
	},
	// countryJSON / countryName：国家可搜索选择器用。
	"countryJSON": func(v any) template.JS {
		b, _ := json.Marshal(v)
		return template.JS(b)
	},
	"countryName": countryNameByCode,
	// firstRune 取用户名首字符（按 rune），用作侧边栏头像字母。
	"firstRune": func(s string) string {
		for _, r := range strings.TrimSpace(s) {
			return string(unicode.ToUpper(r))
		}
		return "?"
	},
	"maskSecret": func(s string) string {
		if len(s) <= 4 {
			return strings.Repeat("*", len(s))
		}
		return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
	},
}
