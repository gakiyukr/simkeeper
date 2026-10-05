// Package cronjob 实现定时任务：到期提醒分发、失败重投、数据清理。
// 周期号码的滚动不由 cron 自动完成——由用户在号码页点「已续费」触发，
// 未续费的号码到期后进入「已过期」分类等待复活或删除。
// 语义对齐 PHP 版 cron.php：单实例部署下由进程内调度器周期触发，
// 也可由管理后台手动触发一次，无需外部 crontab。
package cronjob

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"simkeeper/internal/auth"
	"simkeeper/internal/db"
	"simkeeper/internal/notify"
	"simkeeper/internal/store"
)

// Jobs 聚合定时任务依赖。每次 New 出来的实例无状态，可安全并发触发
// （SQLite 下写操作由单连接串行化兜底；MySQL/PG 依赖各自的行锁）。
type Jobs struct {
	numbers  *store.NumberRepo
	notify   *store.NotifyRepo
	settings *store.SettingRepo
	sessions *auth.SessionStore
	attempts *store.LoginAttemptRepo
	sender   *notify.Sender
}

// New 基于 *db.DB 构造任务集。
func New(database *db.DB) *Jobs {
	return &Jobs{
		numbers:  &store.NumberRepo{DB: database},
		notify:   &store.NotifyRepo{DB: database},
		settings: &store.SettingRepo{DB: database},
		sessions: &auth.SessionStore{DB: database},
		attempts: &store.LoginAttemptRepo{DB: database},
		sender: &notify.Sender{
			Notify: &store.NotifyRepo{DB: database},
			Users:  &store.UserRepo{DB: database},
		},
	}
}

// RunOnce 执行一轮完整任务，返回发送统计与逐行日志（管理页可展示）。
// 任何一步失败只记录日志继续执行，保证清理与统计不会被单点故障卡死。
func (j *Jobs) RunOnce(now time.Time) (sent, failed int, lines []string) {
	logf := func(format string, args ...any) {
		line := now.Format("2006-01-02 15:04:05") + " " + fmt.Sprintf(format, args...)
		lines = append(lines, line)
		log.Printf("[cron] %s", line)
	}
	// 手动触发走 HTTP 请求，兜底 recover 避免一个 panic 拖垮整个连接
	defer func() {
		if r := recover(); r != nil {
			failed++
			logf("任务异常终止: %v", r)
		}
	}()

	logf("=== 定时任务开始执行 ===")

	// 1. 到期提醒：按每号码独立的提前天数过滤，当天已发过的跳过。
	// 注意：周期号码的滚动不再由 cron 自动完成——未续费的号码到期后
	// 进入「已过期」分类，由用户点「已续费」顺延/复活。
	numbers, err := j.numbers.ListExpiring()
	if err != nil {
		logf("读取号码失败: %v", err)
	} else {
		var outcomes map[int64]*userOutcome
		outcomes, sent, failed = j.sendDueNotifications(numbers, now, logf)
		j.alertChannelFailures(outcomes, logf)
	}
	// 2. 重投：24 小时内失败的提醒通知，按失败后 1/2/3 小时的节奏重试，最多 3 次
	j.retryFailed(now, &sent, &failed, logf)

	// 3. 清理：通知记录按保留天数、登录失败记录按 7 天、会话按过期时间
	j.cleanup(logf)

	// 4. 统计与最后执行时间
	j.recordStats(sent, failed, now, logf)

	logf("=== 定时任务执行结束 ===")
	return sent, failed, lines
}

// sendDueNotifications 找出进入提醒窗口的号码并分发，返回 (成功, 失败) 条数。
// 续费与使用两类提醒相互独立：同一号码可能同时进入两个窗口，各发一条
// （与 PHP 版两条独立查询的行为一致），去重按（号码, 类型, 当天）维度。
// userOutcome 汇总单个用户本轮的渠道投递结果。
type userOutcome struct {
	succeeded, failed []string
}

func (j *Jobs) sendDueNotifications(numbers []store.PhoneNumber, now time.Time, logf func(string, ...any)) (map[int64]*userOutcome, int, int) {
	outcomes := map[int64]*userOutcome{}
	sent, failed := 0, 0
	for _, n := range numbers {
		days := n.DaysLeft(now)
		if days < 0 {
			// 已过期的号码不再提醒（与 PHP 版 DATEDIFF >= 0 一致），需手动更新到期日
			continue
		}
		for _, typ := range []string{"renewal", "usage"} {
			daysBefore := n.RenewalDaysBefore
			if typ == "usage" {
				daysBefore = n.UsageDaysBefore
			}
			if days > daysBefore {
				continue
			}
			done, err := j.notify.HasNotificationToday(n.ID, typ, now)
			if err != nil {
				logf("查询今日是否已发送失败(号码 %s): %v", n.PhoneNumber, err)
				continue
			}
			if done {
				continue
			}
			logf("发送%s通知: 用户ID=%d, 号码ID=%d", typeLabel(typ), n.UserID, n.ID)
			res := j.sender.SendToEnabledChannels(n.UserID, n.ID, typ, buildMessage(typ, n, now))
			sent += res.SentCount
			failed += res.TotalChannels - res.SentCount
			oc, ok := outcomes[n.UserID]
			if !ok {
				oc = &userOutcome{}
				outcomes[n.UserID] = oc
			}
			oc.succeeded = append(oc.succeeded, res.SucceededChannels...)
			oc.failed = append(oc.failed, res.FailedChannels...)
			for _, e := range res.Errors {
				logf("%s通知未送达部分渠道: %s", typeLabel(typ), e)
			}
			// 与 PHP 版一致：逐条间隔发送，避免短时间集中触发渠道限流
			time.Sleep(time.Second)
		}
	}
	return outcomes, sent, failed
}

// alertChannelFailures 同一用户本轮「有渠道失败且仍有渠道成功」时，
// 通过成功渠道发一条系统告警——防止渠道静默失效数月无人发现。
// 全部渠道都失败时无法告警（已知限制），由日志与管理端失败统计兜底。
func (j *Jobs) alertChannelFailures(outcomes map[int64]*userOutcome, logf func(string, ...any)) {
	for uid, oc := range outcomes {
		if len(oc.failed) == 0 || len(oc.succeeded) == 0 {
			continue
		}
		names := make([]string, len(oc.failed))
		for i, c := range oc.failed {
			names[i] = channelLabel(c)
		}
		msg := "⚠️ 以下通知渠道在最近一轮提醒中投递失败：\n\n- " +
			strings.Join(names, "\n- ") +
			"\n\n请到「通知配置」检查对应渠道（测试发送可即时验证）。\n本告警通过仍正常的渠道发送；详情见通知历史。"
		if err := j.sender.SendSystemAlert(uid, oc.succeeded, msg); err != nil {
			logf("渠道失败告警发送失败(用户ID=%d): %v", uid, err)
		} else {
			logf("已向用户ID=%d 发送渠道失败告警（失败渠道: %s）", uid, strings.Join(names, ", "))
		}
	}
}

// channelLabel 渠道标识的用户可读名称（告警文案用）。
func channelLabel(c string) string {
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
	return c
}

// retryFailed 重投 24 小时内失败的提醒通知：
// 第 1/2/3 次重试分别发生在失败后约 1/2/3 小时（按记录创建时间推算），
// 超过窗口或渠道已停用则自然终止。test 类型与已删除号码的记录跳过。
func (j *Jobs) retryFailed(now time.Time, sent, failed *int, logf func(string, ...any)) {
	recs, err := j.notify.FailedForRetry(now, 24*time.Hour, 3)
	if err != nil {
		logf("读取待重投通知失败: %v", err)
		return
	}
	retried := 0
	for _, rec := range recs {
		created, err := time.ParseInLocation(time.DateTime, rec.CreatedAt, now.Location())
		if err != nil {
			continue
		}
		age := now.Sub(created)
		if age < time.Duration(rec.RetryCount+1)*time.Hour {
			continue
		}
		if !rec.PhoneNumberID.Valid {
			continue
		}
		logf("重投%s通知: 用户ID=%d, 号码ID=%d (第 %d/%d 次)",
			typeLabel(rec.Type), rec.UserID, rec.PhoneNumberID.Int64, rec.RetryCount+1, 3)
		if sendErr := j.sender.SendOneChannel(rec.UserID, rec.Type, rec.Channel, rec.Message); sendErr != nil {
			_ = j.notify.MarkFailedWithRetry(rec.ID, sendErr.Error())
			*failed++
			logf("重投失败: %v", sendErr)
		} else {
			_ = j.notify.MarkSent(rec.ID)
			*sent++
			retried++
			logf("重投成功")
		}
		time.Sleep(time.Second)
	}
	if retried > 0 {
		logf("重投成功 %d 条", retried)
	}
}

// cleanup 清理过期数据，失败只记日志。
func (j *Jobs) cleanup(logf func(string, ...any)) {
	retention := j.settings.GetInt("log_retention_days", 90)
	if n, err := j.notify.CleanupExpired(retention); err != nil {
		logf("清理过期通知记录失败: %v", err)
	} else if n > 0 {
		logf("清理了 %d 条过期通知记录（保留 %d 天）", n, retention)
	}
	if err := j.attempts.GC(); err != nil {
		logf("清理登录失败记录失败: %v", err)
	}
	if err := j.sessions.GC(); err != nil {
		logf("清理过期会话失败: %v", err)
	}
}

// recordStats 落盘累计发送数与最后执行时间。
func (j *Jobs) recordStats(sent, failed int, now time.Time, logf func(string, ...any)) {
	total := j.settings.GetInt("total_sent", 0)
	if err := j.settings.Set("total_sent", strconv.Itoa(total+sent)); err != nil {
		logf("更新统计失败: %v", err)
	}
	if err := j.settings.Set("last_cron_run", now.Format(time.DateTime)); err != nil {
		logf("更新最后执行时间失败: %v", err)
	}
	logf("任务执行完成: 成功发送 %d 条通知, 失败 %d 条", sent, failed)
}

func typeLabel(typ string) string {
	if typ == "renewal" {
		return "续费"
	}
	return "使用"
}

// buildMessage 生成通知正文，文案与 PHP 版 generateNotificationMessage 一致。
func buildMessage(typ string, n store.PhoneNumber, now time.Time) string {
	days := n.DaysLeft(now)
	expireCN := n.ExpiryDate // 解析失败时保底展示原值
	if t, err := time.ParseInLocation("2006-01-02", n.ExpiryDate, now.Location()); err == nil {
		expireCN = t.Format("2006年01月02日")
	}
	head := fmt.Sprintf(
		"号码：%s\n国家：%s\n运营商：%s\n到期时间：%s\n剩余天数：%d天\n",
		n.PhoneNumber, n.CountryName, n.Carrier, expireCN, days,
	)
	if typ == "renewal" {
		amount := "未填写"
		if n.RechargeAmount > 0 {
			amount = strconv.FormatFloat(n.RechargeAmount, 'f', -1, 64) + " " + n.RechargeCurrency
		}
		return "📱 续费提醒\n\n" + head +
			"建议充值金额：" + amount + "\n\n" +
			"请及时为您的号码充值，避免因欠费导致号码失效。"
	}
	return "📞 使用提醒\n\n" + head +
		"请记得使用您的号码（发送短信或拨打电话），以保持号码活跃状态。"
}

// Start 启动进程内周期调度：立即执行一轮，之后每隔 interval 执行一次。
// interval <= 0 表示禁用后台调度（只保留管理后台手动触发）。
// 停止条件为 ctx 取消；间隔取值默认 1 小时，由 main 传入。
func (j *Jobs) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("[cron] 提醒调度已禁用（仅管理后台手动触发）；数据清理仍每日运行")
		go j.gcLoop(ctx, 24*time.Hour)
		return
	}
	go func() {
		log.Printf("[cron] 后台调度已启动，间隔 %s", interval)
		j.RunOnce(time.Now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Printf("[cron] 后台调度已停止")
				return
			case t := <-ticker.C:
				j.RunOnce(t)
			}
		}
	}()
}

// gcLoop 独立于提醒调度的清理循环：会话与登录失败记录不会因
// 关闭调度而无限增长。
func (j *Jobs) gcLoop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = j.sessions.GC()
			_ = j.attempts.GC()
			_, _ = j.notify.CleanupExpired(j.settings.GetInt("log_retention_days", 90))
			log.Printf("[cron] 每日清理完成")
		}
	}
}
