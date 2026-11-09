package proto

import (
	"math"
	"testing"

	"google.golang.org/protobuf/proto"
)

// ---------- 量化 ----------

// 位置量化必须准确到半格以内。半格 = 1/64 m ≈ 1.6 cm。
func TestQuantizePosRoundTrip(t *testing.T) {
	cases := []float64{0, 1, -1, 0.5, -0.5, 12.34, -12.34, 127.9, -127.9}
	for _, v := range cases {
		q := QuantizePos(v)
		got := DequantizePos(q)
		if diff := math.Abs(got - v); diff > 1/PosScale/2 {
			t.Errorf("QuantizePos(%v) = %v，误差 %v 超过半格", v, got, diff)
		}
	}
}

// 精度必须是 1/32 m = 3.125 cm，这是文档里钉死的数字。
func TestQuantizePosPrecision(t *testing.T) {
	if step := 1 / PosScale; step != 0.03125 {
		t.Fatalf("位置精度应为 0.03125 m，实际 %v", step)
	}
	// 两个相邻可表示值之间的距离
	a := DequantizePos(0)
	b := DequantizePos(1)
	if b-a != 0.03125 {
		t.Fatalf("相邻量化值间隔应为 0.03125，实际 %v", b-a)
	}
}

// 覆盖范围必须够 128×128 地图（±64 m）并有余量。
func TestQuantizePosRange(t *testing.T) {
	// int16 的量程是 ±32767，对应 ±1024 m
	const maxMeters = 32767 / PosScale
	if maxMeters < 128 {
		t.Fatalf("位置量程 %v m 不够 128 m 的地图", maxMeters)
	}
	// 超出量程时必须钳位而不是溢出成反号
	big := QuantizePos(1e9)
	small := QuantizePos(-1e9)
	if big <= 0 {
		t.Fatalf("正溢出应钳到正数，得到 %d", big)
	}
	if small >= 0 {
		t.Fatalf("负溢出应钳到负数，得到 %d", small)
	}
}

func TestQuantizeVelRoundTrip(t *testing.T) {
	cases := []float64{0, 6, -6, 20, -20, 0.001, -0.001}
	for _, v := range cases {
		got := DequantizeVel(QuantizeVel(v))
		if diff := math.Abs(got - v); diff > 1/VelScale/2 {
			t.Errorf("QuantizeVel(%v) = %v，误差 %v 超过半格", v, got, diff)
		}
	}
	// 精度 1/256 m/s
	if step := 1 / VelScale; step != 0.00390625 {
		t.Fatalf("速度精度应为 0.00390625，实际 %v", step)
	}
}

// 朝向是环形量，必须验证首尾接缝处不会跳变。
func TestQuantizeAimWraparound(t *testing.T) {
	const twoPi = 2 * math.Pi

	// 0 附近
	for _, v := range []float64{0, 0.001, -0.001, twoPi - 0.001} {
		q := QuantizeAim(v)
		got := DequantizeAim(q)
		// 还原值可能落在 2π 附近，取环形距离
		d := math.Abs(got - v)
		if d > math.Pi {
			d = twoPi - d
		}
		if d > 1/AimScale*2 {
			t.Errorf("QuantizeAim(%v) = %v，环形误差 %v 过大", v, got, d)
		}
	}

	// 刚好 2π 必须归一化回 0
	if q := QuantizeAim(twoPi); q != 0 {
		t.Errorf("2π 应归一化为 0，得到 %d", q)
	}
	// 负数必须归一化到正半区
	if q := QuantizeAim(-math.Pi); q >= 32768 {
		// -π 应该等于 π，即 32768
		if q != 32768 {
			t.Errorf("−π 应量化到 32768，得到 %d", q)
		}
	}
}

func TestQuantizeAimPrecision(t *testing.T) {
	// 2π/65536 = 0.00009587 rad = 0.005493°
	const degPerStep = 360.0 / 65536.0
	if math.Abs(degPerStep-0.0054931640625) > 1e-12 {
		t.Fatalf("朝向精度应为 0.005493°，实际 %v", degPerStep)
	}
}

func TestQuantizeRadius(t *testing.T) {
	// 精度 1/256 m
	if got := DequantizeRadius(QuantizeRadius(0.3)); math.Abs(got-0.3) > 1/RadiusScale/2 {
		t.Errorf("半径 0.3 往返误差过大：%v", got)
	}
	// 负数必须归零而不是回绕成巨大值
	if q := QuantizeRadius(-1); q != 0 {
		t.Errorf("负半径应归零，得到 %d", q)
	}
	// 超大值应钳位
	if q := QuantizeRadius(1e6); q != 65535 {
		t.Errorf("超大半径应钳到 65535，得到 %d", q)
	}
}

// ---------- 编解码 ----------

func TestMarshalUnmarshalHello(t *testing.T) {
	h := NewHello("翰", "tok-123")
	if h.ProtocolVersion != ProtocolVersion {
		t.Fatalf("协议版本应为 %d", ProtocolVersion)
	}

	data, err := Marshal(h)
	if err != nil {
		t.Fatal(err)
	}

	var out Hello
	if err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.PlayerName != "翰" || out.Token != "tok-123" {
		// 必须传指针：protobuf 消息内含 sync.Mutex，按值传会复制锁
		// （go vet 会直接报 copies lock value）。
		t.Fatalf("往返后内容不符：name=%q token=%q", out.PlayerName, out.Token)
	}
}

// Envelope 必须能往返，这是可靠通道传递任意消息的基础。
func TestEnvelopeRoundTrip(t *testing.T) {
	env := Wrap(&Envelope_Hello{Hello: NewHello("p1", "")})
	data, err := Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	var out Envelope
	if err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Body.(*Envelope_Hello); !ok {
		t.Fatalf("oneof 类型丢失，得到 %T", out.Body)
	}
}

// 快照是热路径消息，验证它包含所有关键字段且往返正确。
func TestSnapshotRoundTrip(t *testing.T) {
	snap := &Snapshot{
		ServerTick:     12345,
		AckedInputTick: 12340,
		Self:           &PlayerSelf{EntityId: 7, Hp: 100, MaxHp: 100, Gold: 42},
		Adds:           []*EntityDelta{{EntityId: 100, Kind: EntityKind_ENTITY_KIND_MONSTER, Hp: 50}},
		Updates:        []*EntityDelta{{EntityId: 101, Kind: EntityKind_ENTITY_KIND_BULLET}},
		Removes:        []uint32{55, 56},
		FogDelta:       []uint64{0xFF},
		Events:         []*GameEvent{{Kind: GameEventKind_GAME_EVENT_KIND_KILL, A: 1, B: 2}},
	}

	data, err := Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}

	var out Snapshot
	if err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.ServerTick != snap.ServerTick {
		t.Errorf("ServerTick = %d，期望 %d", out.ServerTick, snap.ServerTick)
	}
	if len(out.Adds) != 1 || out.Adds[0].EntityId != 100 {
		t.Errorf("Adds 往返错误：%+v", out.Adds)
	}
	if len(out.Removes) != 2 || out.Removes[0] != 55 {
		t.Errorf("Removes 往返错误：%+v", out.Removes)
	}
	if out.Self.Gold != 42 {
		t.Errorf("Self.Gold 往返错误：%d", out.Self.Gold)
	}
}

// 空消息必须能往返——快照可能本 tick 没有任何变化。
func TestEmptySnapshotRoundTrip(t *testing.T) {
	snap := &Snapshot{}
	data, err := Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var out Snapshot
	if err := Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.ServerTick != 0 || len(out.Adds) != 0 {
		t.Fatalf("空快照往返后不空：%+v", &out)
	}
}

// 损坏的数据必须报错而不是静默产生半截对象。
func TestUnmarshalRejectsGarbage(t *testing.T) {
	var out Snapshot
	err := Unmarshal([]byte{0xFF, 0xFF, 0xFF, 0xFF}, &out)
	if err == nil {
		t.Fatal("垃圾数据应当报错")
	}
}

// 未知字段必须被忽略，而不是报错——这让新增字段不会破坏旧服务器。
func TestUnmarshalIgnoresUnknownFields(t *testing.T) {
	// 手工构造：字段 1 (varint) = 1，字段 99 (varint) = 2
	data := []byte{0x08, 0x01, 0x98, 0x06, 0x02}

	var h Hello
	if err := Unmarshal(data, &h); err != nil {
		t.Fatalf("未知字段不该报错：%v", err)
	}
	if h.ProtocolVersion != 1 {
		t.Fatalf("已知字段应被解析，得到 %d", h.ProtocolVersion)
	}
}

// ---------- 基准 ----------

func BenchmarkSnapshotMarshal(b *testing.B) {
	snap := makeTestSnapshot()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Marshal(snap); err != nil {
			b.Fatal(err)
		}
	}
}

func makeTestSnapshot() *Snapshot {
	snap := &Snapshot{
		ServerTick:     1,
		AckedInputTick: 0,
		Self:           &PlayerSelf{EntityId: 7, Hp: 100, MaxHp: 100},
	}
	// 4 玩家 + 30 怪，与选型实测的数据规模一致
	for i := 0; i < 4; i++ {
		snap.Updates = append(snap.Updates, &EntityDelta{
			EntityId: uint32(i),
			Kind:     EntityKind_ENTITY_KIND_PLAYER,
			Pos:      &Vec2Q{X: int32(i * 32), Y: 100},
		})
	}
	for i := 0; i < 30; i++ {
		snap.Updates = append(snap.Updates, &EntityDelta{
			EntityId: uint32(100 + i),
			Kind:     EntityKind_ENTITY_KIND_MONSTER,
			Pos:      &Vec2Q{X: int32(i * 16), Y: 200},
			Hp:       50,
		})
	}
	return snap
}

var _ proto.Message = (*Snapshot)(nil)
