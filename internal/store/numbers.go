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
}

// NumberRepo 号码数据访问。
type NumberRepo struct {
	DB *db.DB
}

const numberCols = `id, user_id, phone_number, country_code, country_name, carrier, expiry_date,
	recharge_amount, recharge_currency, renewal_days_before, usage_days_before,
	auto_expiry_enabled, auto_start_date, auto_expiry_period, auto_calculated_expiry,
	status, notes, created_at`

func scanNumber(scan interface{ Scan(...any) error }) (*PhoneNumber, error) {
	var n PhoneNumber
	var carrier, autoStart, autoCalc, notes sql.NullString
	var amount sql.NullFloat64
	var period sql.NullInt64
	err := scan.Scan(&n.ID, &n.UserID, &n.PhoneNumber, &n.CountryCode, &n.CountryName,
		&carrier, &n.ExpiryDate, &amount, &n.RechargeCurrency,
		&n.RenewalDaysBefore, &n.UsageDaysBefore,
		&n.AutoExpiryEnabled, &autoStart, &period, &autoCalc,
		&n.Status, &notes, &n.CreatedAt)
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
		  auto_expiry_enabled, auto_start_date, auto_expiry_period, auto_calculated_expiry, status, notes)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.UserID, n.PhoneNumber, n.CountryCode, n.CountryName, nullStr(n.Carrier), n.ExpiryDate,
		nullFloat(n.RechargeAmount), n.RechargeCurrency, n.RenewalDaysBefore, n.UsageDaysBefore,
		boolInt(n.AutoExpiryEnabled), nullStr(n.AutoStartDate), nullInt(n.AutoExpiryPeriod),
		nullStr(n.AutoCalculatedExpiry), n.Status, nullStr(n.Notes),
	)
}

// Update 全量更新号码字段（归属校验由调用方保证，语句仍带 user_id 条件兜底）。
func (r *NumberRepo) Update(n *PhoneNumber) error {
	_, err := r.DB.Exec(
		`UPDATE phone_numbers SET
		 phone_number=?, country_code=?, country_name=?, carrier=?, expiry_date=?,
		 recharge_amount=?, recharge_currency=?, renewal_days_before=?, usage_days_before=?,
		 auto_expiry_enabled=?, auto_start_date=?, auto_expiry_period=?, auto_calculated_expiry=?,
		 status=?, notes=?, updated_at=?
		 WHERE id=? AND user_id=?`,
		n.PhoneNumber, n.CountryCode, n.CountryName, nullStr(n.Carrier), n.ExpiryDate,
		nullFloat(n.RechargeAmount), n.RechargeCurrency, n.RenewalDaysBefore, n.UsageDaysBefore,
		boolInt(n.AutoExpiryEnabled), nullStr(n.AutoStartDate), nullInt(n.AutoExpiryPeriod),
		nullStr(n.AutoCalculatedExpiry), n.Status, nullStr(n.Notes),
		db.Touch(time.Now()), n.ID, n.UserID,
	)
	return err
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

// CountForUser 用户号码总数（用于 max_numbers_per_user 上限校验）。
func (r *NumberRepo) CountForUser(userID int64) (int, error) {
	var n int
	err := r.DB.QueryRow(`SELECT COUNT(*) FROM phone_numbers WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// ListForUser 分页列出某用户的号码。
func (r *NumberRepo) ListForUser(userID int64, page, limit int) ([]PhoneNumber, int, error) {
	return r.list(` WHERE user_id = ?`, []any{userID}, page, limit)
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

// DisableAutoExpiry 关闭自动续期，到期日保留为最近一次自动计算结果。
// 语义与 PHP 版 disableAutoExpiry 一致。
func (r *NumberRepo) DisableAutoExpiry(id, userID int64) error {
	_, err := r.DB.Exec(
		`UPDATE phone_numbers SET
		 auto_expiry_enabled = 0, auto_start_date = NULL, auto_expiry_period = NULL,
		 expiry_date = COALESCE(auto_calculated_expiry, expiry_date),
		 auto_calculated_expiry = NULL, updated_at = ?
		 WHERE id = ? AND user_id = ?`,
		db.Touch(time.Now()), id, userID,
	)
	return err
}

// UpdateAutoExpiry 把已过期的自动续期号码滚动到下一周期，返回受影响行数。
// 新到期日 = 原到期日 + auto_expiry_period 天（与 PHP 版从原到期日累加的语义一致）。
func (r *NumberRepo) UpdateAutoExpiry(today time.Time) (int64, error) {
	rows, err := r.DB.Query(
		`SELECT id, expiry_date, auto_expiry_period FROM phone_numbers
		 WHERE auto_expiry_enabled = 1 AND status = 'active' AND expiry_date <= ?`,
		today.Format("2006-01-02"),
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type pending struct {
		id     int64
		expiry string
		days   int
	}
	var batch []pending
	for rows.Next() {
		var p pending
		var period int
		if err := rows.Scan(&p.id, &p.expiry, &period); err != nil {
			return 0, err
		}
		p.days = period
		batch = append(batch, p)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var updated int64
	for _, p := range batch {
		base, err := time.ParseInLocation("2006-01-02", p.expiry, today.Location())
		if err != nil {
			continue
		}
		newExpiry := base.AddDate(0, 0, p.days).Format("2006-01-02")
		res, err := r.DB.Exec(
			`UPDATE phone_numbers SET expiry_date = ?, auto_calculated_expiry = ?, updated_at = ? WHERE id = ?`,
			newExpiry, newExpiry, db.Touch(time.Now()), p.id,
		)
		if err != nil {
			return updated, err
		}
		n, _ := res.RowsAffected()
		updated += n
	}
	return updated, nil
}

// ListExpiring 列出活跃用户（status=active）名下全部活跃号码，
// 供定时任务在内存里按剩余天数过滤（数据量小，避免 SQLite 日期函数方言）。
func (r *NumberRepo) ListExpiring() ([]PhoneNumber, error) {
	q := `SELECT pn.id, pn.user_id, pn.phone_number, pn.country_code, pn.country_name, pn.carrier,
		pn.expiry_date, pn.recharge_amount, pn.recharge_currency,
		pn.renewal_days_before, pn.usage_days_before,
		pn.auto_expiry_enabled, pn.auto_start_date, pn.auto_expiry_period, pn.auto_calculated_expiry,
		pn.status, pn.notes, pn.created_at
		FROM phone_numbers pn JOIN users u ON u.id = pn.user_id
		WHERE pn.status = 'active' AND u.status = 'active'
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
