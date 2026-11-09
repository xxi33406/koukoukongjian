package obs

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestSetupRejectsUnknownFormat(t *testing.T) {
	if _, _, err := Setup(&bytes.Buffer{}, "info", "yaml"); err == nil {
		t.Fatal("未知格式必须报错")
	}
}

func TestSetupRejectsUnknownLevel(t *testing.T) {
	if _, _, err := Setup(&bytes.Buffer{}, "verbose", "text"); err == nil {
		t.Fatal("未知级别必须报错")
	}
}

// writer 为 nil 时 slog 会在第一次写日志时 panic，必须提前挡住。
func TestSetupRejectsNilWriter(t *testing.T) {
	if _, _, err := Setup(nil, "info", "text"); err == nil {
		t.Fatal("nil writer 必须报错")
	}
}

func TestSetupTextFormat(t *testing.T) {
	var buf bytes.Buffer
	log, _, err := Setup(&buf, "info", "text")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "k", "v")

	out := buf.String()
	if !strings.Contains(out, "msg=hello") {
		t.Fatalf("text 输出里没有消息：%q", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Fatalf("text 输出里没有键值：%q", out)
	}
}

func TestSetupJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	log, _, err := Setup(&buf, "info", "json")
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "k", "v")

	out := buf.String()
	if !strings.Contains(out, `"msg":"hello"`) {
		t.Fatalf("json 输出格式不对：%q", out)
	}
	if !strings.Contains(out, `"k":"v"`) {
		t.Fatalf("json 输出里没有键值：%q", out)
	}
}

func TestSetupEmptyFormatDefaultsToText(t *testing.T) {
	var buf bytes.Buffer
	log, _, err := Setup(&buf, "", "")
	if err != nil {
		t.Fatalf("空参数应回落到默认：%v", err)
	}
	log.Info("x")
	if strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Fatalf("空格式应回落为 text，实际是 json：%q", buf.String())
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log, _, err := Setup(&buf, "warn", "text")
	if err != nil {
		t.Fatal(err)
	}

	log.Debug("dbg")
	log.Info("inf")
	log.Warn("wrn")
	log.Error("err")

	out := buf.String()
	if strings.Contains(out, "dbg") || strings.Contains(out, "inf") {
		t.Fatalf("warn 级别不应输出 debug/info：%q", out)
	}
	if !strings.Contains(out, "wrn") || !strings.Contains(out, "err") {
		t.Fatalf("warn 级别应输出 warn/error：%q", out)
	}
}

// LevelVar 是给运行期调级别用的，改完必须立刻生效。
func TestLevelVarTakesEffectImmediately(t *testing.T) {
	var buf bytes.Buffer
	log, lv, err := Setup(&buf, "error", "text")
	if err != nil {
		t.Fatal(err)
	}

	log.Info("before")
	if strings.Contains(buf.String(), "before") {
		t.Fatal("error 级别不该输出 info")
	}

	lv.Set(slog.LevelDebug)
	log.Info("after")
	if !strings.Contains(buf.String(), "after") {
		t.Fatal("改级别后 info 应立即输出")
	}
}

// 时间戳必须压成时分秒毫秒——50ms 级的 tick 用秒级精度看不出东西。
func TestCompactTimeFormat(t *testing.T) {
	a := compactTime(nil, slog.Time(slog.TimeKey, time.Date(2026, 11, 8, 21, 3, 4, 123456789, time.Local)))
	s, ok := a.Value.Any().(string)
	if !ok {
		t.Fatalf("时间应变成字符串，实际 %T", a.Value.Any())
	}
	if s != "21:03:04.123" {
		t.Fatalf("格式 = %q，期望 21:03:04.123", s)
	}
}

// 分组内的 time 属性不是 slog 自己的时间戳，不能动。
func TestCompactTimeSkipsGrouped(t *testing.T) {
	a := slog.Time(slog.TimeKey, time.Now())
	got := compactTime([]string{"sub"}, a)
	if _, ok := got.Value.Any().(time.Time); !ok {
		t.Fatal("分组内的 time 属性不应被改写")
	}
}

// 非 time 类型的同名属性也不能动。
func TestCompactTimeSkipsNonTime(t *testing.T) {
	a := slog.String(slog.TimeKey, "already-a-string")
	got := compactTime(nil, a)
	if got.Value.String() != "already-a-string" {
		t.Fatalf("非 time 值被改写：%v", got.Value)
	}
}
