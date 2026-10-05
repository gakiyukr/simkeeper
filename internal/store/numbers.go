package store

import (
	"database/sql"
	"time"

	"simkeeper/internal/db"
)

// PhoneNumber 对应 phone_numbers 表。
type PhoneNumber struct {
	ID                   int64
	UserID               int64
	PhoneNumber          string
	CountryCode          string
	CountryName          string
	Carrier              string
	ExpiryDate           string // YYYY-MM-DD
	RechargeAmount       float64
	RechargeCurrency     string
	RenewalDaysBefore    int
	UsageDaysBefore      int
	AutoExpiryEnabled    bool
	AutoStartDate        string
	AutoExpiryPeriod     int // 90 | 180 | 365，0 表示未启用
	AutoCalculatedExpiry string
	Status               string
	Notes                string
	CreatedAt            string
	NoKeepalive          bool // 无需保号：不参与到期提醒与周期滚动
}

// NumberRepo 号码数据访问。
type NumberRepo struct {
	DB *db.DB
}

const numberCols = `id, user_id, phone_number, country_code, country_name, carrier, expiry_date,
	recharge_amount, recharge_currency, renewal_days_before, usage_days_before,
	auto_expiry_enabled, auto_start_date, auto_expiry_period, auto_calculated_expiry,
	status, notes, created_at, no_keepalive`

func scanNumber(scan interface{ Scan(...any) error }) (*PhoneNumber, error) {
	var n PhoneNumber
	var carrier, autoStart, autoCalc, notes sql.NullString
	var amount sql.NullFloat64
	var period sql.NullInt64
	var noKeepalive int
	err := scan.Scan(&n.ID, &n.UserID, &n.PhoneNumber, &n.CountryCode, &n.CountryName,
		&carrier, &n.ExpiryDate, &amount, &n.RechargeCurrency,
		&n.RenewalDaysBefore, &n.UsageDaysBefore,
		&n.AutoExpiryEnabled, &autoStart, &period, &autoCalc,
		&n.Status, &notes, &n.CreatedAt, &noKeepalive)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	n.Carrier = carrier.String
	n.AutoStartDate = autoStart.String
	n.AutoCalculatedExpiry = autoCalc.String
	n.Notes = notes.String
	if amount.Valid {
		n.RechargeAmount = amount.Float64
	}
	if period.Valid {
		n.AutoExpiryPeriod = int(period.Int64)
	}
	n.NoKeepalive = noKeepalive == 1
	return &n, nil
}

// ByID 按 ID 查找；user_id > 0 时同时限定归属，防止越权访问。
func (r *NumberRepo) ByID(id, userID int64) (*PhoneNumber, error) {
	q := `SELECT ` + numberCols + ` FROM phone_numbers WHERE id = ?`
	args := []any{id}
	if userID > 0 {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	return scanNumber(r.DB.QueryRow(q, args...))
}

// Create 新增号码。
func (r *NumberRepo) Create(n *PhoneNumber) (int64, error) {
	return r.DB.InsertID(
		`INSERT INTO phone_numbers
		 (user_id, phone_number, country_code, country_name, carrier, expiry_date,
		  recharge_amount, recharge_currency, renewal_days_before, usage_days_before,
		  auto_expiry_enabled, auto_start_date, auto_expiry_period, auto_calculated_expiry,
		  no_keepalive, status, notes)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.UserID, n.PhoneNumber, n.CountryCode, n.CountryName, nullStr(n.Carrier), n.ExpiryDate,
		nullFloat(n.RechargeAmount), n.RechargeCurrency, n.RenewalDaysBefore, n.UsageDaysBefore,
		boolInt(n.AutoExpiryEnabled), nullStr(n.AutoStartDate), nullInt(n.AutoExpiryPeriod),
		nullStr(n.AutoCalculatedExpiry), boolInt(n.NoKeepalive), n.Status, nullStr(n.Notes),
	)
}

// Update 全量更新号码字段（归属校验由调用方保证，语句仍带 user_id 条件兜底）。
func (r *NumberRepo) Update(n *PhoneNumber) error {
	_, err := r.DB.Exec(
		`UPDATE phone_numbers SET
		 phone_number=?, country_code=?, country_name=?, carrier=?, expiry_date=?,
		 recharge_amount=?, recharge_currency=?, renewal_days_before=?, usage_days_before=?,
		 auto_expiry_enabled=?, auto_start_date=?, auto_expiry_period=?, auto_calculated_expiry=?,
		 no_keepalive=?, status=?, notes=?, updated_at=?
		 WHERE id=? AND user_id=?`,
		n.PhoneNumber, n.CountryCode, n.CountryName, nullStr(n.Carrier), n.ExpiryDate,
		nullFloat(n.RechargeAmount), n.RechargeCurrency, n.RenewalDaysBefore, n.UsageDaysBefore,
		boolInt(n.AutoExpiryEnabled), nullStr(n.AutoStartDate), nullInt(n.AutoExpiryPeriod),
		nullStr(n.AutoCalculatedExpiry), boolInt(n.NoKeepalive), n.Status, nullStr(n.Notes),
		db.Touch(time.Now()), n.ID, n.UserID,
	)
	return err
}

// SetStatus 更新号码状态（active 使用中 / inactive 号码已丢失）。
// 丢失状态仅作记录：不参与提醒、不占配额，可随时恢复。
func (r *NumberRepo) SetStatus(id, userID int64, status string) (int64, error) {
	res, err := r.DB.Exec(
		`UPDATE phone_numbers SET status = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		status, db.Touch(time.Now()), id, userID,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Delete 删除号码（带归属兜底）。
func (r *NumberRepo) Delete(id, userID int64) error {
	q := `DELETE FROM phone_numbers WHERE id = ?`
	args := []any{id}
	if userID > 0 {
		q += ` AND user_id = ?`
		args = append(args, userID)
	}
	_, err := r.DB.Exec(q, args...)
	return err
}

// CountForUser 用户使用中的号码数（用于 max_numbers_per_user 上限校验；
// 已停用的号码不占配额）。
func (r *NumberRepo) CountForUser(userID int64) (int, error) {
	var n int
	err := r.DB.QueryRow(
		"SELECT COUNT(*) FROM phone_numbers WHERE user_id = ? AND status = 'active'", userID,
	).Scan(&n)
	return n, err
}

// CountDuplicate 统计同一用户名下使用中的同号号码（excludeID 用于编辑时排除自身）。
// 应用层查重：三方言的区分度索引策略不同（MySQL 无部分索引），统一在代码里做。
func (r *NumberRepo) CountDuplicate(userID int64, phone string, excludeID int64) (int, error) {
	var n int
	err := r.DB.QueryRow(
		"SELECT COUNT(*) FROM phone_numbers WHERE user_id = ? AND phone_number = ? AND status = 'active' AND id != ?",
		userID, phone, excludeID,
	).Scan(&n)
	return n, err
}

// ListForUser 分页列出某用户的号码。
func (r *NumberRepo) ListForUser(userID int64, page, limit int) ([]PhoneNumber, int, error) {
	return r.list(` WHERE user_id = ?`, []any{userID}, page, limit)
}

// MarkRenewed 标记已续费：到期日与周期起点由调用方计算好写入
//（未过期锚定原到期日顺延、已过期从今天起算），auto_calculated_expiry 同步。
// 归属校验由 WHERE user_id 承担。这是周期号码唯一的滚动机制。
func (r *NumberRepo) MarkRenewed(id, userID int64, newExpiry, today string) (int64, error) {
	res, err := r.DB.Exec(
		`UPDATE phone_numbers SET expiry_date = ?, auto_calculated_expiry = ?, auto_start_date = ?, updated_at = ?
		 WHERE id = ? AND user_id = ?`,
		newExpiry, newExpiry, today, db.Touch(time.Now()), id, userID,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListForAdmin 管理端列出全部号码，可按号码/国家/运营商过滤。
// LIKE 套 LOWER() 保证三方言大小写行为一致。
func (r *NumberRepo) ListForAdmin(page, limit int, search string) ([]PhoneNumber, int, error) {
	if search != "" {
		like := `%` + search + `%`
		return r.list(
			` WHERE LOWER(phone_number) LIKE LOWER(?) OR LOWER(country_name) LIKE LOWER(?) OR LOWER(carrier) LIKE LOWER(?)`,
			[]any{like, like, like}, page, limit,
		)
	}
	return r.list(``, nil, page, limit)
}

func (r *NumberRepo) list(where string, args []any, page, limit int) ([]PhoneNumber, int, error) {
	var total int
	if err := r.DB.QueryRow(`SELECT COUNT(*) FROM phone_numbers`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	q := `SELECT ` + numberCols + ` FROM phone_numbers` + where + ` ORDER BY expiry_date ASC, id ASC LIMIT ? OFFSET ?`
	rows, err := r.DB.Query(q, append(args, limit, (page-1)*limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []PhoneNumber
	for rows.Next() {
		n, err := scanNumber(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *n)
	}
	return out, total, rows.Err()
}

// ListExpiring 列出活跃用户（status=active）名下全部活跃号码，
// 供定时任务在内存里按剩余天数过滤（数据量小，避免 SQLite 日期函数方言）。
func (r *NumberRepo) ListExpiring() ([]PhoneNumber, error) {
	q := `SELECT pn.id, pn.user_id, pn.phone_number, pn.country_code, pn.country_name, pn.carrier,
		pn.expiry_date, pn.recharge_amount, pn.recharge_currency,
		pn.renewal_days_before, pn.usage_days_before,
		pn.auto_expiry_enabled, pn.auto_start_date, pn.auto_expiry_period, pn.auto_calculated_expiry,
		pn.status, pn.notes, pn.created_at, pn.no_keepalive
		FROM phone_numbers pn JOIN users u ON u.id = pn.user_id
		WHERE pn.status = 'active' AND u.status = 'active' AND pn.no_keepalive = 0
		ORDER BY pn.expiry_date ASC, pn.id ASC`
	rows, err := r.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PhoneNumber
	for rows.Next() {
		n, err := scanNumber(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// DaysLeft 计算距离到期日的天数（到期日当天为 0）。
// 解析失败返回 -1，调用方按「不提醒」处理。
func (n *PhoneNumber) DaysLeft(now time.Time) int {
	expiry, err := time.ParseInLocation("2006-01-02", n.ExpiryDate, now.Location())
	if err != nil {
		return -1
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return int(expiry.Sub(today).Hours() / 24)
}
