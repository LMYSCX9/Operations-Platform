// Package observability 提供底座的结构化日志基线。
//
// 日志格式与字段清单尚未最终定稿（OQ-016），M0 采用 slog 的 JSON Handler：
// 输出可被机器解析，且不引入第三方依赖。
package observability

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// ParseLevel 将配置中的日志级别转换为 slog.Level。
// 级别集合取自 NFR-LOG-03，不允许扩展别名，避免配置文件出现同义异名。
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("不支持的日志级别 %q（可选：debug、info、warn、error）", name)
	}
}

// NewLogger 创建结构化日志器。NFR-LOG-02 要求默认输出到标准输出/标准错误。
func NewLogger(level string, out io.Writer) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: lvl})), nil
}
