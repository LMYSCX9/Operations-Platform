package scheduler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseCronValid(t *testing.T) {
	cases := []string{
		"* * * * *",
		"*/5 * * * *",
		"0 3 * * *",
		"0 0 1 1 *",
		"0 9-18 * * 1-5",
		"0 0,12 * * 0,6",
		"0 0 * * 7",
		"10-20/5 * * * *",
	}
	for _, expr := range cases {
		if _, err := ParseCron(expr); err != nil {
			t.Fatalf("%q 应合法，实际错误: %v", expr, err)
		}
	}
}

func TestParseCronInvalid(t *testing.T) {
	cases := []string{
		"",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"*/0 * * * *",
		"a b c d e",
		"5-1 * * * *",
		"1,,2 * * * *",
	}
	for _, expr := range cases {
		if _, err := ParseCron(expr); err == nil {
			t.Fatalf("%q 应被拒绝", expr)
		}
	}
}

func TestCronNext(t *testing.T) {
	loc := time.UTC
	base := time.Date(2026, 3, 1, 10, 2, 30, 0, loc)

	c, err := ParseCron("*/5 * * * *")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	next, err := c.Next(base, loc)
	if err != nil {
		t.Fatalf("计算失败: %v", err)
	}
	if want := time.Date(2026, 3, 1, 10, 5, 0, 0, loc); !next.Equal(want) {
		t.Fatalf("next = %s，期望 %s", next, want)
	}

	daily, _ := ParseCron("0 3 * * *")
	next, _ = daily.Next(base, loc)
	if want := time.Date(2026, 3, 2, 3, 0, 0, 0, loc); !next.Equal(want) {
		t.Fatalf("每日任务 next = %s，期望 %s", next, want)
	}

	// 周与日同时受限时按「或」匹配：3 月 1 日是周日，应命中。
	sunday, _ := ParseCron("0 0 * * 0")
	next, _ = sunday.Next(base, loc)
	if want := time.Date(2026, 3, 8, 0, 0, 0, 0, loc); !next.Equal(want) {
		t.Fatalf("周日任务 next = %s，期望 %s", next, want)
	}
}

func TestCronNextRespectsTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("缺少时区数据")
	}
	c, _ := ParseCron("0 9 * * *")
	next, err := c.Next(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), loc)
	if err != nil {
		t.Fatalf("计算失败: %v", err)
	}
	// 东八区 09:00 等于 UTC 01:00。
	if want := time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s，期望 %s", next, want)
	}
}

func TestTaskInputRejectsCredentials(t *testing.T) {
	in := TaskInput{
		Name: "x", PluginID: "demo", CapabilityID: "demo.echo",
		TriggerType: TriggerInterval, IntervalSeconds: 60, Timezone: "UTC",
		Input: json.RawMessage(`{"nested":{"password":"plain"}}`),
	}
	err := in.Validate()
	if err == nil || !strings.Contains(err.Error(), "凭据") {
		t.Fatalf("含凭据参数应被拒绝，实际错误: %v", err)
	}
}

func TestTaskInputValidateBasics(t *testing.T) {
	base := TaskInput{
		Name: "x", PluginID: "demo", CapabilityID: "demo.echo",
		TriggerType: TriggerCron, CronExpr: "*/5 * * * *", Timezone: "UTC",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("合法输入被拒绝: %v", err)
	}
	bad := base
	bad.Timezone = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("空时区应被拒绝")
	}
	bad = base
	bad.CronExpr = "bad"
	if err := bad.Validate(); err == nil {
		t.Fatal("非法 cron 应被拒绝")
	}
	bad = base
	bad.TriggerType = TriggerInterval
	if err := bad.Validate(); err == nil {
		t.Fatal("interval 缺少秒数应被拒绝")
	}
}
