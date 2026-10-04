// Package cronjob 实现定时任务：自动续期滚动、到期提醒分发、数据清理。
// 语义对齐 PHP 版 cron.php：单实例部署下由进程内调度器周期触发，
// 也可由管理后台手动触发一次，无需外部 crontab。
package cronjob

import (
	"context"
	"fmt"
	"log"
	"strconv"
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

	// 1. 自动续期：把已过期的自动续期号码滚到下一周期
	updated, err := j.numbers.UpdateAutoExpiry(now)
	if err != nil {
		logf("更新自动续期号码失败: %v", err)
	} else if updated > 0 {
		logf("已更新 %d 个自动续期号码的到期时间", updated)
	} else {
		logf("没有需要更新的自动续期号码")
	}

	// 2. 到期提醒：按每号码独立的提前天数过滤，当天已发过的跳过
	numbers, err := j.numbers.ListExpiring()
	if err != nil {
		logf("读取号码失败: %v", err)
	} else {
		sent, failed = j.sendDueNotifications(numbers, now, logf)
	}

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
func (j *Jobs) sendDueNotifications(numbers []store.PhoneNumber, now time.Time, logf func(string, ...any)) (sent, failed int) {
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
			logf("发送%s通知: 用户ID=%d, 号码=%s", typeLabel(typ), n.UserID, n.PhoneNumber)
			res := j.sender.SendToEnabledChannels(n.UserID, n.ID, typ, buildMessage(typ, n, now))
			sent += res.SentCount
			failed += res.TotalChannels - res.SentCount
			for _, e := range res.Errors {
				logf("%s通知未送达部分渠道: %s", typeLabel(typ), e)
			}
			// 与 PHP 版一致：逐条间隔发送，避免短时间集中触发渠道限流
			time.Sleep(time.Second)
		}
	}
	return sent, failed
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
		log.Printf("[cron] 后台调度已禁用（仅管理后台手动触发）")
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
