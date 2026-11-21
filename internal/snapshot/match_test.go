package snapshot

import (
	"testing"

	"koukoukongjian/internal/game/world"
	"koukoukongjian/internal/proto"
)

// TestMatchResultToProtoCarriesAllFields 钉住字段一一对应。
func TestMatchResultToProtoCarriesAllFields(t *testing.T) {
	rec := world.MatchResult{
		RoomID:      "room-9",
		WorldSeed:   99,
		StartedAt:   1700000000,
		EndedAt:     1700000600,
		Outcome:     world.OutcomeCleared,
		MaxWave:     6,
		DurationSec: 600,
		Players: []world.PlayerResult{
			{Slot: 0, Damage: 100, Contribute: 150.5, Kills: 12, ReflectKills: 3, Deaths: 2, Gold: 88},
			{Slot: 3, Damage: 10, Contribute: 10, Kills: 1, ReflectKills: 0, Deaths: 0, Gold: 4},
		},
	}

	got := MatchResultToProto(rec)
	if got == nil {
		t.Fatal("MatchResultToProto 返回 nil")
	}
	if got.RoomId != "room-9" || got.WorldSeed != 99 {
		t.Fatalf("房间信息 = {%q, %d}，期望 {room-9, 99}", got.RoomId, got.WorldSeed)
	}
	if got.Outcome != proto.MatchOutcome_MATCH_OUTCOME_CLEARED {
		t.Fatalf("Outcome = %v，期望 CLEARED", got.Outcome)
	}
	if got.MaxWave != 6 || got.DurationSec != 600 {
		t.Fatalf("进度 = {wave %d, dur %d}，期望 {6, 600}", got.MaxWave, got.DurationSec)
	}
	if len(got.Players) != 2 {
		t.Fatalf("玩家行数 = %d，期望 2", len(got.Players))
	}
	if got.Players[0].ReflectKills != 3 {
		t.Fatalf("首行 ReflectKills = %d，期望 3", got.Players[0].ReflectKills)
	}
	if got.Players[1].Slot != 3 {
		t.Fatalf("次行 Slot = %d，期望 3", got.Players[1].Slot)
	}
	if got.Players[0].Contribute != 150.5 {
		t.Fatalf("首行 Contribute = %v，期望 150.5", got.Players[0].Contribute)
	}
}

// TestMatchOutcomeMapping 钉住三值枚举的映射，包括零值。
func TestMatchOutcomeMapping(t *testing.T) {
	cases := []struct {
		in   world.MatchOutcome
		want proto.MatchOutcome
	}{
		{world.OutcomeUnknown, proto.MatchOutcome_MATCH_OUTCOME_UNSPECIFIED},
		{world.OutcomeWiped, proto.MatchOutcome_MATCH_OUTCOME_WIPED},
		{world.OutcomeCleared, proto.MatchOutcome_MATCH_OUTCOME_CLEARED},
	}
	for _, c := range cases {
		if got := matchOutcomeToProto(c.in); got != c.want {
			t.Fatalf("%v 映射成 %v，期望 %v", c.in, got, c.want)
		}
	}
}

// TestMatchResultToProtoEmptyPlayers 钉住没人时返回空列表而不是 nil。
func TestMatchResultToProtoEmptyPlayers(t *testing.T) {
	got := MatchResultToProto(world.MatchResult{RoomID: "r"})
	if got == nil {
		t.Fatal("空结果返回 nil")
	}
	if len(got.Players) != 0 {
		t.Fatalf("空结果的玩家行数 = %d，期望 0", len(got.Players))
	}
}
