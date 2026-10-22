package cronjob

import (
	"testing"
	"time"
)

// TestReminderDue 重复提醒间隔按自然日计算：默认 1 天等价于原来的
// 「当天已发则跳过」，N 天则满 N 个自然日后再次提醒。
func TestReminderDue(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 30, 0, 0, time.Local)
	cases := []struct {
		name     string
		last     string
		interval int
		want     bool
	}{
		{"从未提醒", "", 1, true},
		{"每天：今天已发", "2026-10-22 00:05:00", 1, false},
		{"每天：昨天已发", "2026-10-21 23:55:00", 1, true},
		{"三天：不足三天", "2026-10-20 12:00:00", 3, false},
		{"三天：满三天", "2026-10-19 23:00:00", 3, true},
		{"间隔 0 按 1 天处理", "2026-10-22 08:00:00", 0, false},
		{"时间损坏时按可提醒处理", "not-a-time", 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reminderDue(c.last, c.interval, now); got != c.want {
				t.Errorf("reminderDue(%q, %d) = %v, want %v", c.last, c.interval, got, c.want)
			}
		})
	}
}
