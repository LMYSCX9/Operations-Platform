package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrNoNextRun 表示在可搜索范围内找不到下一次触发时间（如 2 月 30 日）。
var ErrNoNextRun = errors.New("在可搜索范围内找不到下一次触发时间")

// Cron 是解析后的 5 段 cron 表达式（分钟级，OQ-042）。
type Cron struct {
	minute field
	hour   field
	dom    field
	month  field
	dow    field
}

type field struct {
	any    bool
	values map[int]bool
}

func (f field) match(v int) bool {
	if f.any {
		return true
	}
	return f.values[v]
}

// ParseCron 解析 `分 时 日 月 周` 五段表达式。
// 支持 `*`、`n`、`a-b`、`*/n`、`a-b/n` 与逗号列表；周字段 0 与 7 都表示周日。
func ParseCron(expr string) (Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Cron{}, fmt.Errorf("cron 必须为 5 段（分 时 日 月 周），实际 %d 段", len(parts))
	}
	var c Cron
	var err error
	if c.minute, err = parseField(parts[0], 0, 59); err != nil {
		return Cron{}, fmt.Errorf("分钟字段: %w", err)
	}
	if c.hour, err = parseField(parts[1], 0, 23); err != nil {
		return Cron{}, fmt.Errorf("小时字段: %w", err)
	}
	if c.dom, err = parseField(parts[2], 1, 31); err != nil {
		return Cron{}, fmt.Errorf("日期字段: %w", err)
	}
	if c.month, err = parseField(parts[3], 1, 12); err != nil {
		return Cron{}, fmt.Errorf("月份字段: %w", err)
	}
	if c.dow, err = parseField(parts[4], 0, 7); err != nil {
		return Cron{}, fmt.Errorf("星期字段: %w", err)
	}
	if c.dow.values[7] {
		c.dow.values[0] = true
	}
	return c, nil
}

func parseField(spec string, min, max int) (field, error) {
	f := field{values: map[int]bool{}}
	if spec == "*" {
		f.any = true
		return f, nil
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return field{}, errors.New("包含空项")
		}
		if err := parsePart(f.values, part, min, max); err != nil {
			return field{}, err
		}
	}
	if len(f.values) == 0 {
		return field{}, errors.New("没有有效取值")
	}
	return f, nil
}

func parsePart(values map[int]bool, part string, min, max int) error {
	step := 1
	base := part
	if idx := strings.Index(part, "/"); idx >= 0 {
		base = part[:idx]
		stepStr := part[idx+1:]
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return fmt.Errorf("步长 %q 非法", stepStr)
		}
		step = n
	}

	start, end := min, max
	switch {
	case base == "*":
		// 保持默认范围
	case strings.Contains(base, "-"):
		bounds := strings.Split(base, "-")
		if len(bounds) != 2 {
			return fmt.Errorf("区间 %q 非法", base)
		}
		a, err := strconv.Atoi(bounds[0])
		if err != nil {
			return fmt.Errorf("区间起点 %q 非法", bounds[0])
		}
		b, err := strconv.Atoi(bounds[1])
		if err != nil {
			return fmt.Errorf("区间终点 %q 非法", bounds[1])
		}
		start, end = a, b
	default:
		n, err := strconv.Atoi(base)
		if err != nil {
			return fmt.Errorf("取值 %q 非法", base)
		}
		start, end = n, n
	}
	if start < min || end > max || start > end {
		return fmt.Errorf("取值 %d-%d 超出范围 %d-%d", start, end, min, max)
	}
	for v := start; v <= end; v += step {
		values[v] = true
	}
	return nil
}

// Next 返回严格晚于 from 的下一次触发时刻（分钟精度）。
func (c Cron) Next(from time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	t := from.In(loc).Truncate(time.Minute).Add(time.Minute)
	// 上限 5 年，足以覆盖闰日等低频表达式，同时避免无限循环。
	const maxMinutes = 5 * 366 * 24 * 60
	for i := 0; i < maxMinutes; i++ {
		if c.matchLocal(t) {
			return t.UTC(), nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, ErrNoNextRun
}

func (c Cron) matchLocal(t time.Time) bool {
	dayMatch := c.dom.match(t.Day())
	weekMatch := c.dow.match(int(t.Weekday()))
	// 标准 cron：日与周都受限时取「或」，否则取「与」。
	dayOK := dayMatch && weekMatch
	if !c.dom.any && !c.dow.any {
		dayOK = dayMatch || weekMatch
	}
	return dayOK &&
		c.minute.match(t.Minute()) &&
		c.hour.match(t.Hour()) &&
		c.month.match(int(t.Month()))
}
