package store

import (
	"database/sql"
	"log"
	"time"

	"simkeeper/internal/db"
	"simkeeper/internal/secret"
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
	UpdatedAt            string
	NoKeepalive          bool   // 无需保号：不参与到期提醒与周期滚动
	PlanName             string // 方案信息：套餐名称（如 30 天不限流量）
	SecondaryNumbers     string // 副卡号码，每行一个（仅作记录，不带区号）
	SimType              string // 号码类型：physical 实体卡 / esim
	LPAString            string // eSIM LPA 激活码（库里存 enc:v1: 密文，出入库自动加解密）
	ConfirmCode          string // eSIM 确认码（同上）
	DeviceID             int64  // 安装设备（devices.id，0 = 未指定）
}

// NumberRepo 号码数据访问。
type NumberRepo struct {
	DB *db.DB
}

const numberCols = `id, user_id, phone_number, country_code, country_name, carrier, expiry_date,
	recharge_amount, recharge_currency, renewal_days_before, usage_days_before,
	auto_expiry_enabled, auto_start_date, auto_expiry_period, auto_calculated_expiry,
	status, notes, created_at, no_keepalive, plan_name, secondary_numbers,
	sim_type, lpa_string, confirm_code, updated_at, device_id`

func scanNumber(scan interface{ Scan(...any) error }) (*PhoneNumber, error) {
	var n PhoneNumber
	var carrier, autoStart, autoCalc, notes sql.NullString
	var amount sql.NullFloat64
	var period sql.NullInt64
	var noKeepalive int
	var planName, secondary sql.NullString
	var lpa, confirm sql.NullString
	var deviceID sql.NullInt64
	err := scan.Scan(&n.ID, &n.UserID, &n.PhoneNumber, &n.CountryCode, &n.CountryName,
		&carrier, &n.ExpiryDate, &amount, &n.RechargeCurrency,
		&n.RenewalDaysBefore, &n.UsageDaysBefore,
		&n.AutoExpiryEnabled, &autoStart, &period, &autoCalc,
		&n.Status, &notes, &n.CreatedAt, &noKeepalive, &planName, &secondary,
		&n.SimType, &lpa, &confirm, &n.UpdatedAt, &deviceID)
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
	n.PlanName = planName.String
	n.SecondaryNumbers = secondary.String
	if n.SimType == "" {
		n.SimType = "physical"
	}
	n.LPAString = lpa.String
	n.ConfirmCode = confirm.String
	n.DeviceID = deviceID.Int64
	n.decryptESIMSecrets()
	return &n, nil
}

// encryptESIMSecrets 将 eSIM 激活信息就地加密为 enc:v1: 密文（与渠道凭据同策略：
// 加密失败返回错误、不降级明文——LPA 泄露等于 eSIM 可被他人下载）。已加密或空值跳过。
func encryptESIMSecrets(n *PhoneNumber) error {
	for _, p := range []*string{&n.LPAString, &n.ConfirmCode} {
		if *p == "" || secret.IsEncrypted(*p) {
			continue
		}
		out, err := secret.Encrypt(*p)
		if err != nil {
			return err
		}
		*p = out
	}
	return nil
}

// decryptESIMSecrets 就地解密读出的 eSIM 激活信息。无前缀的历史明文原样放行；
// 解密失败（密钥更换等）时置空——重新保存即可修复。
func (n *PhoneNumber) decryptESIMSecrets() {
	for _, p := range []*string{&n.LPAString, &n.ConfirmCode} {
		if *p == "" || !secret.IsEncrypted(*p) {
			continue
		}
		out, err := secret.Decrypt(*p)
		if err != nil {
			log.Printf("[store] 解密 eSIM 激活信息失败（已置空，重新保存号码即可修复）: %v", err)
			*p = ""
			continue
		}
		*p = out
	}
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

// Create 新增号码。eSIM 激活信息先加密再落库。
func (r *NumberRepo) Create(n *PhoneNumber) (int64, error) {
	if n.SimType != "esim" {
		n.SimType = "physical"
	}
	if err := encryptESIMSecrets(n); err != nil {
		return 0, err
	}
	return r.DB.InsertID(
		`INSERT INTO phone_numbers
		 (user_id, phone_number, country_code, country_name, carrier, expiry_date,
		  recharge_amount, recharge_currency, renewal_days_before, usage_days_before,
		  auto_expiry_enabled, auto_start_date, auto_expiry_period, auto_calculated_expiry,
		  no_keepalive, plan_name, secondary_numbers, sim_type, lpa_string, confirm_code,
		  device_id, status, notes)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.UserID, n.PhoneNumber, n.CountryCode, n.CountryName, nullStr(n.Carrier), n.ExpiryDate,
		nullFloat(n.RechargeAmount), n.RechargeCurrency, n.RenewalDaysBefore, n.UsageDaysBefore,
		boolInt(n.AutoExpiryEnabled), nullStr(n.AutoStartDate), nullInt(n.AutoExpiryPeriod),
		nullStr(n.AutoCalculatedExpiry), boolInt(n.NoKeepalive),
		nullStr(n.PlanName), nullStr(n.SecondaryNumbers), n.SimType, nullStr(n.LPAString), nullStr(n.ConfirmCode),
		nullInt64(n.DeviceID), n.Status, nullStr(n.Notes),
	)
}

// Update 全量更新号码字段（归属校验由调用方保证，语句仍带 user_id 条件兜底）。
// eSIM 激活信息先加密再落库。
func (r *NumberRepo) Update(n *PhoneNumber) error {
	if n.SimType != "esim" {
		n.SimType = "physical"
	}
	if err := encryptESIMSecrets(n); err != nil {
		return err
	}
	_, err := r.DB.Exec(
		`UPDATE phone_numbers SET
		 phone_number=?, country_code=?, country_name=?, carrier=?, expiry_date=?,
		 recharge_amount=?, recharge_currency=?, renewal_days_before=?, usage_days_before=?,
		 auto_expiry_enabled=?, auto_start_date=?, auto_expiry_period=?, auto_calculated_expiry=?,
		 no_keepalive=?, plan_name=?, secondary_numbers=?, sim_type=?, lpa_string=?, confirm_code=?,
		 device_id=?, status=?, notes=?, updated_at=?
		 WHERE id=? AND user_id=?`,
		n.PhoneNumber, n.CountryCode, n.CountryName, nullStr(n.Carrier), n.ExpiryDate,
		nullFloat(n.RechargeAmount), n.RechargeCurrency, n.RenewalDaysBefore, n.UsageDaysBefore,
		boolInt(n.AutoExpiryEnabled), nullStr(n.AutoStartDate), nullInt(n.AutoExpiryPeriod),
		nullStr(n.AutoCalculatedExpiry), boolInt(n.NoKeepalive),
		nullStr(n.PlanName), nullStr(n.SecondaryNumbers), n.SimType, nullStr(n.LPAString), nullStr(n.ConfirmCode),
		nullInt64(n.DeviceID), n.Status, nullStr(n.Notes),
		db.Touch(time.Now()), n.ID, n.UserID,
	)
	return err
}

// SetStatus 更新号码状态（active 使用中 / inactive 已终止）。
// 终止状态仅作记录：不参与提醒、不占配额，可随时撤销。
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
// （未过期锚定原到期日顺延、已过期从今天起算），auto_calculated_expiry 同步。
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
		pn.status, pn.notes, pn.created_at, pn.no_keepalive, pn.plan_name, pn.secondary_numbers,
		pn.sim_type, pn.lpa_string, pn.confirm_code, pn.updated_at, pn.device_id
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
