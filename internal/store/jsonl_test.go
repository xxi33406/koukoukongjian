package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"koukoukongjian/internal/game/world"
)

// timeAfterSeconds 避免直接 import time 后在多个用例里重复写 time.After。
func timeAfterSeconds(n int) <-chan time.Time { return time.After(time.Duration(n) * time.Second) }

func sampleResult() world.MatchResult {
	return world.MatchResult{
		RoomID:      "room-1",
		WorldSeed:   42,
		StartedAt:   1700000000,
		EndedAt:     1700000900,
		Outcome:     world.OutcomeCleared,
		MaxWave:     5,
		DurationSec: 900,
		Players: []world.PlayerResult{
			{Slot: 0, Damage: 1200.5, Contribute: 1200.5, Kills: 30, ReflectKills: 2, Deaths: 1, Gold: 240},
		},
	}
}

// readLines 读回收到的全部非空行。
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开落盘文件失败：%v", err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("扫描落盘文件失败：%v", err)
	}
	return out
}

// TestWriterAppendsOneJSONPerMatch 钉住 JSONL 格式：一局一行。
func TestWriterAppendsOneJSONPerMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matches.jsonl")
	w, err := NewWriter(WriterOptions{Path: path})
	if err != nil {
		t.Fatalf("NewWriter 失败：%v", err)
	}

	w.Report(sampleResult())
	r2 := sampleResult()
	r2.RoomID = "room-2"
	w.Report(r2)
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("落盘行数 = %d，期望 2", len(lines))
	}
	// 每行必须是完整 JSON，且能解回原结构。
	for i, line := range lines {
		var got world.MatchResult
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON：%v\n内容：%s", i+1, err, line)
		}
		if got.WorldSeed != 42 {
			t.Fatalf("第 %d 行 WorldSeed = %d，期望 42", i+1, got.WorldSeed)
		}
	}
	if !strings.Contains(lines[0], `"room_id":"room-1"`) {
		t.Fatalf("首行缺少 room_id 字段：%s", lines[0])
	}
	if !strings.Contains(lines[1], `"room_id":"room-2"`) {
		t.Fatalf("次行顺序错（追加而非覆盖）：%s", lines[1])
	}
}

// TestWriterCreatesParentDirs 钉住父目录不存在时自动创建。
//
// 运维直接传 -match-log data/matches.jsonl 时 data/ 通常还不存在；
// 报错退出会让整个服务器起不来，而这只是日志功能。
func TestWriterCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "matches.jsonl")
	w, err := NewWriter(WriterOptions{Path: path})
	if err != nil {
		t.Fatalf("NewWriter 失败：%v", err)
	}
	w.Report(sampleResult())
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}
	if lines := readLines(t, path); len(lines) != 1 {
		t.Fatalf("落盘行数 = %d，期望 1", len(lines))
	}
}

// TestWriterFlushOnClose 钉住 Close 会把缓冲写透。
//
// 这是 §11.1.3 硬约束第 2 条的实现：房间关闭前必须 flush，而 flush
// 的唯一出口就是 Close。若 Close 不 flush，最后一局（往往就是团灭
// 或通关那局，最重要的那局）会丢。
func TestWriterFlushOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	w, err := NewWriter(WriterOptions{Path: path})
	if err != nil {
		t.Fatalf("NewWriter 失败：%v", err)
	}
	w.Report(sampleResult())
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}
	if lines := readLines(t, path); len(lines) != 1 {
		t.Fatalf("Close 后落盘行数 = %d，期望 1", len(lines))
	}
}

// TestWriterDisabledOnEmptyPath 钉住"没配置路径 = 不写盘"。
func TestWriterDisabledOnEmptyPath(t *testing.T) {
	w, err := NewWriter(WriterOptions{Path: ""})
	if err != nil {
		t.Fatalf("空路径应该返回可用的 no-op writer，实际报错：%v", err)
	}
	if w == nil {
		t.Fatal("空路径返回 nil writer，调用方会 nil panic")
	}
	w.Report(sampleResult()) // 不该 panic
	if err := w.Close(); err != nil {
		t.Fatalf("no-op Close 报错：%v", err)
	}
}

// TestWriterCloseIsIdempotent 钉住重复 Close 不 panic。
//
// shutdown 可能从 Run 的两个分支（ctx done / stop called）走到，
// 而 Close 会被调用方放在 defer 里——重复调用必须安全。
func TestWriterCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	w, err := NewWriter(WriterOptions{Path: path})
	if err != nil {
		t.Fatalf("NewWriter 失败：%v", err)
	}
	w.Report(sampleResult())
	if err := w.Close(); err != nil {
		t.Fatalf("首次 Close 失败：%v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("二次 Close 失败：%v", err)
	}
}

// TestWriterDropsWhenQueueFull 钉住队列满时丢弃而不是阻塞。
//
// §11.1.3 硬约束第 1 条：写盘绝不能出现在 tick goroutine 内，所以
// Report 必须非阻塞。满则丢——丢一局战绩好过卡死整局。
func TestWriterDropsWhenQueueFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.jsonl")
	w, err := NewWriter(WriterOptions{Path: path, QueueSize: 1, NoWorker: true})
	if err != nil {
		t.Fatalf("NewWriter 失败：%v", err)
	}
	// NoWorker=true 时没有消费者，队列容量 1：第二次 Report 必须丢弃
	// 而不是阻塞。若实现阻塞了，这个测试会挂住。
	done := make(chan struct{})
	go func() {
		w.Report(sampleResult())
		w.Report(sampleResult())
		close(done)
	}()
	select {
	case <-done:
	case <-timeAfterSeconds(2):
		t.Fatal("Report 在队列满时阻塞了超过 2 秒——必须丢弃而不是阻塞")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败：%v", err)
	}
	if w.Dropped() == 0 {
		t.Fatal("队列满时 Dropped() = 0，期望 ≥1")
	}
}
