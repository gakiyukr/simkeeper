package web

import (
	"cmp"
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
	Dial string // 国际区号，不含 +
}

// Countries 预置国家/地区列表，覆盖常见 eSIM/境外号码来源地，可按需扩充。
var Countries = []Country{
	{"CN", "中国", "86"}, {"HK", "香港", "852"}, {"MO", "澳门", "853"}, {"TW", "台湾", "886"},
	{"JP", "日本", "81"}, {"KR", "韩国", "82"}, {"SG", "新加坡", "65"}, {"MY", "马来西亚", "60"},
	{"TH", "泰国", "66"}, {"VN", "越南", "84"}, {"PH", "菲律宾", "63"}, {"ID", "印度尼西亚", "62"},
	{"IN", "印度", "91"}, {"KH", "柬埔寨", "855"}, {"MM", "缅甸", "95"}, {"LA", "老挝", "856"},
	{"MV", "马尔代夫", "960"}, {"PK", "巴基斯坦", "92"}, {"BD", "孟加拉国", "880"}, {"NP", "尼泊尔", "977"},
	{"AE", "阿联酋", "971"}, {"SA", "沙特阿拉伯", "966"}, {"QA", "卡塔尔", "974"}, {"IL", "以色列", "972"},
	{"TR", "土耳其", "90"}, {"KZ", "哈萨克斯坦", "7"},
	{"GB", "英国", "44"}, {"DE", "德国", "49"}, {"FR", "法国", "33"}, {"IT", "意大利", "39"},
	{"ES", "西班牙", "34"}, {"PT", "葡萄牙", "351"}, {"NL", "荷兰", "31"}, {"BE", "比利时", "32"},
	{"CH", "瑞士", "41"}, {"AT", "奥地利", "43"}, {"SE", "瑞典", "46"}, {"NO", "挪威", "47"},
	{"DK", "丹麦", "45"}, {"FI", "芬兰", "358"}, {"IE", "爱尔兰", "353"}, {"IS", "冰岛", "354"},
	{"PL", "波兰", "48"}, {"CZ", "捷克", "420"}, {"HU", "匈牙利", "36"}, {"GR", "希腊", "30"},
	{"RO", "罗马尼亚", "40"}, {"BG", "保加利亚", "359"}, {"HR", "克罗地亚", "385"}, {"UA", "乌克兰", "380"},
	{"RU", "俄罗斯", "7"}, {"LT", "立陶宛", "370"}, {"LV", "拉脱维亚", "371"}, {"EE", "爱沙尼亚", "372"},
	{"US", "美国", "1"}, {"CA", "加拿大", "1"}, {"MX", "墨西哥", "52"}, {"BR", "巴西", "55"},
	{"AR", "阿根廷", "54"}, {"CL", "智利", "56"}, {"CO", "哥伦比亚", "57"}, {"PE", "秘鲁", "51"},
	{"AU", "澳大利亚", "61"}, {"NZ", "新西兰", "64"}, {"FJ", "斐济", "679"},
	{"ZA", "南非", "27"}, {"EG", "埃及", "20"}, {"KE", "肯尼亚", "254"}, {"NG", "尼日利亚", "234"},
	{"MA", "摩洛哥", "212"}, {"MU", "毛里求斯", "230"},
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

// countryDialByCode 按 ISO 代码查国际区号（不含 +）；未收录返回空串。
func countryDialByCode(code string) string {
	for _, c := range Countries {
		if c.Code == code {
			return c.Dial
		}
	}
	return ""
}

// flagFiles 内嵌旗帜的白名单（小写 ISO 代码 → 存在），路由据此防路径穿越，
// 模板据此对未知代码退化为不渲染旗帜。
var flagFiles = map[string]bool{}

func init() {
	for _, c := range Countries {
		flagFiles[strings.ToLower(c.Code)] = true
	}
}

// phoneGroupRules 常见国家/地区对「去区号后号码本体」的阅读分组（从左按组切；
// 规则耗尽后仍有剩余：>2 位单独成组、≤2 位并入上一组）。未收录国家按位数兜底。
var phoneGroupRules = map[string][]int{
	"CN": {3, 4, 4}, // 138 0013 8000
	"HK": {4, 4},    // 9123 4567
	"MO": {4, 4},
	"TW": {3, 3, 3}, // 912 345 678
	"JP": {2, 4, 4}, // 90 1234 5678
	"KR": {2, 4, 4}, // 10 1234 5678
	"SG": {4, 4},    // 9123 4567
	"MY": {2, 3, 4}, // 12 345 6789
	"TH": {2, 3, 4}, // 81 234 5678
	"VN": {2, 3, 4},
	"ID": {3, 4},    // 811 1234 567（余数并入尾组）
	"PH": {3, 3, 4}, // 917 123 4567
	"IN": {5, 5},    // 91234 56789
	"KH": {2, 3, 4},
	"MM": {3, 4},
	"LA": {2, 3, 4},
	"MV": {3, 4},
	"PK": {3, 4},
	"AE": {2, 3, 4}, // 50 123 4567
	"SA": {2, 3, 4},
	"IL": {2, 3, 4},
	"TR": {3, 3, 2, 2}, // 532 123 45 67
	"KZ": {3, 3, 2, 2},
	"RU": {3, 3, 2, 2},
	"UA": {2, 3, 2, 2},
	"GB": {4, 6}, // 7123 456789
	"IE": {2, 3, 4},
	"FR": {1, 2, 2, 2, 2}, // 6 12 34 56 78
	"MA": {1, 2, 2, 2, 2},
	"ES": {3, 3, 3},
	"PT": {3, 3, 3},
	"NL": {1, 4, 4}, // 6 1234 5678
	"BE": {3, 2, 2, 2},
	"IT": {3, 3, 4},
	"US": {3, 3, 4}, // 415 555 2671
	"CA": {3, 3, 4},
	"MX": {2, 4, 4},
	"BR": {2, 5, 4}, // 11 91234 5678
	"CL": {1, 4, 4},
	"CO": {3, 3, 4},
	"PE": {3, 3, 3},
	"AU": {3, 3, 3}, // 412 345 678
	"NZ": {2, 3, 4},
	"ZA": {2, 3, 4},
	"EG": {2, 4, 4},
	"KE": {3, 3, 3},
	"NG": {3, 3, 4},
}

// digitsOnly 提取字符串中的数字。
func digitsOnly(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// formatPhoneCC 按国家/地区的号码规则插空格便于阅读：完整号码展示为
// 「+区号 分组本体」；裸号（副卡按原样记录）剥掉重复粘贴的区号后只分组本体。
// 数字过少或不含数字时原样返回；与所选国家区号不符的完整号码改按前缀推断。
func formatPhoneCC(cc, number string) string {
	raw := strings.TrimSpace(number)
	if raw == "" {
		return ""
	}
	digits := digitsOnly(raw)
	if len(digits) < 5 {
		return raw
	}
	dial := countryDialByCode(cc)
	nsn := digits
	prefix := ""
	switch {
	case dial != "" && strings.HasPrefix(digits, dial):
		nsn = digits[len(dial):]
		prefix = "+" + dial + " "
	case dial != "" && strings.HasPrefix(raw, "+"):
		return formatPhoneAuto(raw)
	}
	if len(nsn) < 5 {
		return raw
	}
	return prefix + groupNSN(cc, nsn)
}

// formatPhoneAuto 无国家码上下文时按号码前缀推断区号再分组
// （+1/+7 双国共享同一分组规则，前缀推断对分组无歧义）。
func formatPhoneAuto(number string) string {
	raw := strings.TrimSpace(number)
	if !strings.HasPrefix(raw, "+") {
		return raw
	}
	digits := digitsOnly(raw)
	if len(digits) < 5 {
		return raw
	}
	// digitsOnly 已剥掉 +，剩余即完整号码（区号从首位起匹配）
	bestDial, bestCC := "", ""
	for _, c := range Countries {
		if len(c.Dial) > len(bestDial) && strings.HasPrefix(digits, c.Dial) {
			bestDial, bestCC = c.Dial, c.Code
		}
	}
	if bestDial == "" {
		return raw
	}
	return "+" + bestDial + " " + groupNSN(bestCC, digits[len(bestDial):])
}

// groupNSN 对号码本体按规则插空格（pattern 耗尽后的剩余位数：>2 单独成组、
// ≤2 并入上一组，避免出现孤零零的末位）。
func groupNSN(cc, nsn string) string {
	if len(nsn) < 5 {
		return nsn
	}
	pattern, ok := phoneGroupRules[cc]
	if !ok {
		pattern = fallbackPattern(len(nsn))
	}
	var parts []string
	rest := nsn
	for _, g := range pattern {
		if rest == "" {
			break
		}
		if len(rest) <= g {
			parts = append(parts, rest)
			rest = ""
			break
		}
		parts = append(parts, rest[:g])
		rest = rest[g:]
	}
	if rest != "" {
		if len(rest) <= 2 && len(parts) > 0 {
			parts[len(parts)-1] += rest
		} else {
			parts = append(parts, rest)
		}
	}
	return strings.Join(parts, " ")
}

// fallbackPattern 未收录国家按位数兜底：7-12 位用常见惯例，其余 3 位一组、
// 尾组吞余数（余 1 时并入成组避免 3+…+1 的孤位）。
func fallbackPattern(n int) []int {
	switch n {
	case 7:
		return []int{3, 4}
	case 8:
		return []int{4, 4}
	case 9:
		return []int{3, 3, 3}
	case 10:
		return []int{3, 3, 4}
	case 11:
		return []int{3, 4, 4}
	case 12:
		return []int{4, 4, 4}
	}
	var p []int
	for n > 0 {
		if n > 4 || n == 3 {
			p = append(p, 3)
			n -= 3
		} else {
			p = append(p, n)
			n = 0
		}
	}
	return p
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
	DeviceNames  map[int64]string    // 已登记设备（号码表显示安装位置）
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
	deviceNames := map[int64]string{}
	if devices, err := a.Devices.ListForUser(u.ID); err == nil {
		for _, dev := range devices {
			deviceNames[dev.ID] = dev.Name
		}
	}
	d.Content = DashPage{
		Numbers: filtered[start:end], Total: total, TotalAll: len(all),
		Page: pageNum, Pages: pages, Search: q, Cat: cat, Sort: sortKey,
		Counts: counts, Expiring7: expiring7, ActiveCnt: activeCnt,
		RecentNotifs: recent, DeviceNames: deviceNames,
	}
	a.render(w, http.StatusOK, "page_dashboard", d)
}

// CategoryCounts 号码三类保号分类 + 已终止的数量（互斥，合计 = 总数）。
type CategoryCounts struct {
	Cycle   int // 需要周期性保号（活跃、未过期）
	None    int // 无需保号
	Lost    int // 已终止（仅作记录）
	Expired int // 已过期
}

// categoryCounts 按优先级归类：无需保号 > 已终止 > 已过期 > 周期保号。
// 终止是用户显式标记的记录，即使其到期日已过也归入终止，避免找不到。
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
// 分类过滤（与 categoryCounts 的优先级口径一致）。排序：默认（空）与 carrier
// 按运营商名称（数字 < 字母 < 其他字符，大写在前；同名按到期日升序、ID 兜底）；
// 到期时间升序/降序中无需保号号码排最后（其到期日仅作参考）；平局按 ID 保证稳定。
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
		case "inactive": // 已终止（优先于已过期）
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
	case "expiry_asc":
		less = byExpiry(false)
	case "expiry_desc":
		less = byExpiry(true)
	case "created_desc":
		less = func(i, j int) bool {
			if out[i].CreatedAt != out[j].CreatedAt {
				return out[i].CreatedAt > out[j].CreatedAt
			}
			return out[i].ID > out[j].ID
		}
	default: // 空值与 "carrier"：首页默认按运营商名称排序
		less = func(i, j int) bool {
			if c := carrierCompare(out[i].Carrier, out[j].Carrier); c != 0 {
				return c < 0
			}
			// 同名运营商：到期日升序，再按 ID 保证稳定
			if out[i].ExpiryDate != out[j].ExpiryDate {
				return out[i].ExpiryDate < out[j].ExpiryDate
			}
			return out[i].ID < out[j].ID
		}
	}
	sort.Slice(out, less)
	return out
}

// charRank 运营商排序的字符类别：数字 0 < 拉丁字母 1 < 其他字符 2（如中文）。
func charRank(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return 0
	case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
		return 1
	default:
		return 2
	}
}

// carrierCompare 运营商名称排序比较。逐字符比较：类别 数字 < 字母 < 其他字符；
// 字母忽略大小写、平局时大写在前（码点序天然满足 'A' < 'a'）；
// 数字与其他字符按码点。空名称排最后（历史数据允许留空）。返回 -1/0/1。
func carrierCompare(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		switch {
		case len(ra) == len(rb):
			return 0
		case len(ra) == 0:
			return 1
		default:
			return -1
		}
	}
	for i := 0; i < min(len(ra), len(rb)); i++ {
		x, y := ra[i], rb[i]
		if cx, cy := charRank(x), charRank(y); cx != cy {
			return cmp.Compare(cx, cy)
		}
		if charRank(x) == 1 {
			if lx, ly := unicode.ToLower(x), unicode.ToLower(y); lx != ly {
				return cmp.Compare(lx, ly)
			}
		}
		if x != y {
			return cmp.Compare(x, y)
		}
	}
	return cmp.Compare(len(ra), len(rb))
}

// HandleNumberNew 新增号码表单。
func (a *App) HandleNumberNew(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	d := a.baseData(r, "新增号码")
	if r.Method == http.MethodPost {
		a.saveNumberForm(w, r, u.ID, nil, &d)
		return
	}
	devices, _ := a.Devices.ListForUser(u.ID)
	// 新增默认「我刚保过号」口径，上次保号日期预填今天（刚续完就录入的常见场景）
	d.Content = map[string]any{"Countries": Countries, "Carriers": Carriers, "Devices": devices,
		"Country": "", "DateMode": "last", "LastKeepalive": time.Now().Format(dateLayout)}
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
	devices, _ := a.Devices.ListForUser(u.ID)
	// 编辑默认「我知道到期日」口径；上次保号日期以周期起点预填（切口径即有正确值）
	d.Content = map[string]any{
		"Countries": Countries, "Carriers": Carriers, "Devices": devices, "N": number,
		"Country": number.CountryCode, "DateMode": "expiry", "LastKeepalive": number.AutoStartDate,
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

	countryCode := strings.TrimSpace(r.PostFormValue("country_code"))
	countryName := countryNameByCode(countryCode)
	dial := countryDialByCode(countryCode)
	// 主号：用户只填号码本体，区号按所选国家自动带上；粘贴了带区号的完整号码则不重复拼接
	national := strings.TrimSpace(r.PostFormValue("phone_national"))
	if strings.HasPrefix(national, "+") {
		national = "+" + strings.TrimLeft(national[1:], dial) // 粘贴整号：剥掉区号避免重复
	} else if dial != "" && strings.HasPrefix(national, dial) {
		national = strings.TrimPrefix(national, dial)
	}
	phone := normalizePhone("+" + dial + national)
	// 副卡：多行自由录入（不带区号），空行忽略
	var secondaries []string
	for _, v := range r.Form["secondary"] {
		if v = strings.TrimSpace(v); v != "" {
			secondaries = append(secondaries, v)
		}
	}
	secondary := strings.Join(secondaries, "\n")
	planName := strings.TrimSpace(r.PostFormValue("plan_name"))
	carrier := strings.TrimSpace(r.PostFormValue("carrier"))
	expiry := strings.TrimSpace(r.PostFormValue("expiry_date"))
	amount, _ := strconv.ParseFloat(r.PostFormValue("recharge_amount"), 64)
	currency := strings.TrimSpace(r.PostFormValue("recharge_currency"))
	if currency == "" {
		currency = "USD"
	}
	renewalDays := atoiDefault(r.PostFormValue("renewal_days_before"), 7)
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

	// 日期口径：last = 填上次保号日期（到期日由服务端加周期推算）；
	// expiry = 直接填到期日。两种口径落库结构完全一致；空值按 expiry
	//（旧客户端 / 无 JS 提交）处理。
	dateMode := r.PostFormValue("date_mode")
	if dateMode != "last" {
		dateMode = "expiry"
	}
	lastKeepalive := strings.TrimSpace(r.PostFormValue("last_keepalive_date"))

	// 号码类型：physical 实体卡 / esim。eSIM 才保留激活信息（LPA 激活码 +
	// 确认码）；LPA 来自扫码复制的整串文本，去掉混入的空白再校验格式。
	simType := "physical"
	if r.PostFormValue("sim_type") == "esim" {
		simType = "esim"
	}
	lpa := strings.Join(strings.Fields(r.PostFormValue("lpa_string")), "")
	confirmCode := strings.TrimSpace(r.PostFormValue("confirm_code"))
	if simType != "esim" {
		lpa, confirmCode = "", ""
	}
	// 安装设备：可选；归属校验失败或未选则视为未指定
	deviceID := int64(0)
	if v := r.PostFormValue("device_id"); v != "" {
		if id64, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			if _, derr := a.Devices.ByID(id64, userID); derr == nil {
				deviceID = id64
			}
		}
	}
	devices, _ := a.Devices.ListForUser(userID)

	// 校验失败回显用：以本次提交值构造实体——报错后表单不清空；
	// 编辑模式下沿用原 ID（模板据 ID 区分编辑/新增）且回显用户改过的值而非库里旧值。
	submitted := &store.PhoneNumber{
		UserID: userID, PhoneNumber: phone, CountryCode: countryCode, CountryName: countryName,
		Carrier: carrier, ExpiryDate: expiry,
		RechargeAmount: amount, RechargeCurrency: currency,
		RenewalDaysBefore: renewalDays,
		AutoExpiryEnabled: autoEnabled, AutoStartDate: autoStart,
		AutoExpiryPeriod: autoPeriod, Status: status, Notes: notes,
		NoKeepalive: noKeepalive, PlanName: planName, SecondaryNumbers: secondary,
		SimType: simType, LPAString: lpa, ConfirmCode: confirmCode, DeviceID: deviceID,
	}
	if existing != nil {
		submitted.ID = existing.ID
	}
	form := map[string]any{"Countries": Countries, "Carriers": Carriers, "Devices": devices, "N": submitted, "Country": countryCode, "DateMode": dateMode, "LastKeepalive": lastKeepalive}
	fail := func(msg string) {
		d.Flash, d.FlashIsErr = msg, true
		d.Content = form
		a.render(w, http.StatusOK, "page_number_form", *d)
	}

	var expiryDate time.Time
	expiryPast := false // last 口径推算出的到期日 ≤ 今天时置位，保存后附带提醒
	switch {
	case phone == "" || !phoneRe.MatchString(phone):
		fail("号码格式不正确（示例：+8613800138000 或 13800138000）")
		return
	case countryName == "":
		fail("请选择国家/地区")
		return
	case carrier == "":
		fail("请填写运营商")
		return
	case simType == "esim" && lpa != "" && !strings.HasPrefix(strings.ToUpper(lpa), "LPA:"):
		fail("LPA 激活码应以 LPA:1$ 开头（运营商下载二维码里的整串文本）")
		return
	case simType == "esim" && len(lpa) > 2000:
		fail("LPA 激活码过长")
		return
	case len(confirmCode) > 200:
		fail("确认码过长")
		return
	}

	if noKeepalive {
		// 无需保号：不录入到期日，不参与任何提醒与滚动
		expiry = ""
	} else {
		// 需要周期性保号：周期校验在前（last 口径推算到期日要用）
		if autoPeriod < 1 || autoPeriod > 3650 {
			fail("续费周期应为 1-3650 天")
			return
		}
		switch dateMode {
		case "last":
			// 上次保号日期 + 周期 = 到期日；周期起点即上次保号日期
			if lastKeepalive == "" {
				fail("请填写上次保号日期（或切换为「我知道到期日」直接填写）")
				return
			}
			last, perr := time.ParseInLocation(dateLayout, lastKeepalive, time.Local)
			if perr != nil {
				fail("上次保号日期格式应为 YYYY-MM-DD")
				return
			}
			expiryDate = last.AddDate(0, 0, autoPeriod)
			expiry = expiryDate.Format(dateLayout)
			autoStart = last.Format(dateLayout)
			expiryPast = !expiryDate.After(time.Now())
		default:
			// 到期日直接填写，原样保存；周期起点 = 到期日 - 周期
			if expiry == "" {
				fail("需要周期性保号的号码需填写到期时间（或切换为「我刚保过号」填上次保号日期）")
				return
			}
			var perr error
			expiryDate, perr = time.ParseInLocation(dateLayout, expiry, time.Local)
			if perr != nil {
				fail("到期时间格式应为 YYYY-MM-DD")
				return
			}
			autoStart = expiryDate.AddDate(0, 0, -autoPeriod).Format(dateLayout)
		}
	}
	if renewalDays < 1 || renewalDays > 90 {
		fail("保号提醒提前天数应为 1-90")
		return
	}

	n := &store.PhoneNumber{
		UserID: userID, PhoneNumber: phone, CountryCode: countryCode, CountryName: countryName,
		Carrier: carrier, ExpiryDate: expiry,
		RechargeAmount: amount, RechargeCurrency: currency,
		RenewalDaysBefore: renewalDays,
		AutoExpiryEnabled: autoEnabled, AutoStartDate: autoStart,
		AutoExpiryPeriod: autoPeriod, Status: status, Notes: notes,
		NoKeepalive: noKeepalive, PlanName: planName, SecondaryNumbers: secondary,
		SimType: simType, LPAString: lpa, ConfirmCode: confirmCode, DeviceID: deviceID,
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
		msg := "号码已更新"
		if expiryPast {
			msg += "。注意：按上次保号日期推算该号码已过期，点「已续费」可从今天起算复活"
		}
		a.setFlash(w, msg, false)
	} else {
		if cnt, err := a.Numbers.CountDuplicate(userID, phone, 0); err == nil && cnt > 0 {
			fail("该号码已存在于你的列表中")
			return
		}
		if _, err := a.Numbers.Create(n); err != nil {
			fail("保存失败，请重试")
			return
		}
		msg := "号码已添加"
		if expiryPast {
			msg += "。注意：按上次保号日期推算该号码已过期，点「已续费」可从今天起算复活"
		}
		a.setFlash(w, msg, false)
	}
	http.Redirect(w, r, "/numbers", http.StatusSeeOther)
}

// HandleNumberRenew 手动保号（周期号码唯一的滚动机制），新到期日两种算法：
//
//	today  从今天重新起算——按活跃间隔计费的运营商（如 giffgaff）：今天 + 周期；
//	extend 在原到期日上顺延——有效期叠加的运营商（如 AIS）：原到期日 + 周期，
//	       顺延结果仍早于今天（过期太久）时拒绝，提示改用重起算。
//
// 兼容未带 mode 的旧提交：未过期锚定原到期日、已过期从今天起算（原行为）。
// 周期起点同步更新为新周期的开始。GET 访问直接回列表。
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
	mode := r.PostFormValue("mode")
	today := time.Now()
	var anchor time.Time
	switch mode {
	case "today":
		anchor = today
	case "extend":
		// 原到期日无效时按今天起算兜底
		if t, err := time.ParseInLocation(dateLayout, n.ExpiryDate, time.Local); err == nil {
			anchor = t
		} else {
			anchor = today
		}
	default: // 兼容旧提交：未过期锚定原到期日、已过期从今天起算
		anchor = today
		if t, err := time.ParseInLocation(dateLayout, n.ExpiryDate, time.Local); err == nil && t.After(today) {
			anchor = t
		}
	}
	newExpiry := anchor.AddDate(0, 0, n.AutoExpiryPeriod).Format(dateLayout)
	if mode == "extend" {
		if t, err := time.ParseInLocation(dateLayout, newExpiry, time.Local); err != nil || !t.After(today) {
			back("在原到期日上顺延仍早于今天（号码已过期太久），请改用「从今天重新起算」", true)
			return
		}
	}
	newStart := anchor.Format(dateLayout)
	if _, err := a.Numbers.MarkRenewed(n.ID, u.ID, newExpiry, newStart); err != nil {
		back("操作失败", true)
		return
	}
	back(fmt.Sprintf("已标记续费，到期日更新至 %s", newExpiry), false)
}

// HandleNumberSetStatus 快捷切换号码状态：终止（仅作记录）↔ 撤销恢复使用。
// 已终止的号码不参与提醒与配额，可随时撤销。
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
		back("已终止：仅作记录，不再提醒", false)
		return
	}
	back("已撤销终止，恢复为使用中", false)
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

// csvRow 拼 CSV 行并转义：含逗号/引号的字段加引号包裹；以 = + - @ 开头的
// 字段前置单引号，防止 Excel/LibreOffice 把单元格当公式执行（公式注入）。
// 号码列普遍以 + 开头，前置 ' 后 Excel 会按文本展示、加号不再被吃掉。
func csvRow(fields ...string) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		if strings.ContainsAny(f, ",\"\n") {
			f = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		}
		if len(f) > 0 && (f[0] == '=' || f[0] == '+' || f[0] == '-' || f[0] == '@') {
			f = "'" + f
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
	// deviceTypeText 设备类型展示名（设备管理页）。
	"deviceTypeText": deviceTypeText,
	"statusText": func(s string) string {
		if s == "inactive" {
			return "已终止"
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
			return "保号提醒"
		case "usage":
			return "使用提醒（旧）"
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
	// secondaryLines 把副卡存储文本（每行一个）拆成列表，并按主号国家的
	// 号码规则分组展示；非数字内容原样保留。
	"secondaryLines": func(n store.PhoneNumber) []string {
		var out []string
		for _, line := range strings.Split(n.SecondaryNumbers, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, formatPhoneCC(n.CountryCode, line))
			}
		}
		return out
	},
	// secondaryRaw 副卡编辑回显用：拆原始行，不做任何格式化
	//（副卡按原样记录，展示层才分组）。
	"secondaryRaw": func(n store.PhoneNumber) []string {
		var out []string
		for _, line := range strings.Split(n.SecondaryNumbers, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				out = append(out, line)
			}
		}
		return out
	},
	// todayPlus / expiryPlus 手动保号对话框里的两个候选到期日。
	"todayPlus": func(days int) string {
		return time.Now().AddDate(0, 0, days).Format(dateLayout)
	},
	"expiryPlus": func(n store.PhoneNumber) string {
		t, err := time.ParseInLocation(dateLayout, n.ExpiryDate, time.Local)
		if err != nil {
			return ""
		}
		return t.AddDate(0, 0, n.AutoExpiryPeriod).Format(dateLayout)
	},
	// fmtPhone / phoneFmt 号码阅读分组：前者按号码的国家代码，后者按号码
	// 前缀推断区号（通知历史里只有号码字符串时用）。
	"fmtPhone": formatPhoneCC,
	"phoneFmt": formatPhoneAuto,
	// phoneNational 编辑回显：完整号码去掉 +区号，得到号码本体。
	"phoneNational": func(n store.PhoneNumber) string {
		return strings.TrimPrefix(n.PhoneNumber, "+"+countryDialByCode(n.CountryCode))
	},
	// countryJSON / countryName：国家可搜索选择器用。
	"countryJSON": func(v any) template.JS {
		b, _ := json.Marshal(v)
		return template.JS(b)
	},
	"countryName": countryNameByCode,
	"countryDial": countryDialByCode,
	// flagHTML 国旗小图（内嵌 SVG）。不用 Unicode 旗帜 emoji：Windows 浏览器
	// 没有旗帜字形，会退化成 "JP" 字母。名称取自固定的 Countries 表，非用户输入。
	// 未知代码返回空串，单元格自然退化为仅运营商文字。
	"flagHTML": func(code string) template.HTML {
		lower := strings.ToLower(code)
		if !flagFiles[lower] {
			return ""
		}
		name := countryNameByCode(code)
		return template.HTML(`<img class="flag" src="/flags/` + lower + `.svg" alt="` + name + `" title="` + name + `">`)
	},
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
