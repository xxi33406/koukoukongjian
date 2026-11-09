// Package obs 提供可观测性基础设施：日志、指标。
//
// 目前只有日志。指标（tick 耗时、带宽、实体数）留到 M1 有真实负载时再加，
// 现在加只有自欺欺人的空计数器。
package obs

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// Setup 按配置创建 logger。
//
// 返回的 LevelVar 可以在运行期调整级别（比如收到信号后临时开到 debug），
// 这个变量被 handler 持有，改它的值立刻生效。
func Setup(w io.Writer, level, format string) (*slog.Logger, *slog.LevelVar, error) {
	var lv slog.LevelVar
	if err := setLevel(&lv, level); err != nil {
		return nil, nil, err
	}
	if w == nil {
		return nil, nil, fmt.Errorf("obs: writer 不能为 nil")
	}

	opts := &slog.HandlerOptions{
		Level:       &lv,
		ReplaceAttr: compactTime,
	}

	var h slog.Handler
	switch strings.ToLower(format) {
	case "text", "":
		h = slog.NewTextHandler(w, opts)
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		return nil, nil, fmt.Errorf("obs: 未知日志格式 %q（可选 text / json）", format)
	}

	return slog.New(h), &lv, nil
}

func setLevel(lv *slog.LevelVar, level string) error {
	switch strings.ToLower(level) {
	case "debug":
		lv.Set(slog.LevelDebug)
	case "info", "":
		lv.Set(slog.LevelInfo)
	case "warn", "warning":
		lv.Set(slog.LevelWarn)
	case "error":
		lv.Set(slog.LevelError)
	default:
		return fmt.Errorf("obs: 未知日志级别 %q（可选 debug / info / warn / error）", level)
	}
	return nil
}

// compactTime 把 slog 默认的完整时间戳压成时分秒毫秒。
//
// 服务器日志通常同一天内连续阅读，日期是噪声；但毫秒必须留——tick 是 50 ms
// 级的，秒级精度看不出任何东西。
func compactTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		if t, ok := a.Value.Any().(time.Time); ok {
			return slog.String(slog.TimeKey, t.Format("15:04:05.000"))
		}
	}
	return a
}
