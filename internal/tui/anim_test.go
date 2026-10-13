package tui

import (
	"math"
	"testing"
	"time"
)

// TestAnimTimeBasedUniformSpeed 验证动画由"真实经过时间"驱动而非"调用次数"：
//   - 相同的时间间隔产生相同的推进量（匀速）；
//   - 推进量与经过时长成比例；
//   - 在同一时间点重复调用不会额外推进（这是"忽快"根因的直接回归测试）。
func TestAnimTimeBasedUniformSpeed(t *testing.T) {
	a := NewAnim(10)
	base := time.Unix(1700000000, 0)

	// 首次调用没有时间基准，按 animFrameFallback 名义间隔推进一帧。
	a.tickAt(base)
	p0 := a.pct

	// 经过 100ms。
	a.tickAt(base.Add(100 * time.Millisecond))
	p1 := a.pct

	// 再经过 100ms，推进量应与上一段完全一致。
	a.tickAt(base.Add(200 * time.Millisecond))
	p2 := a.pct

	d1 := p1 - p0
	d2 := p2 - p1
	if math.Abs(d1-d2) > 1e-9 {
		t.Fatalf("推进量不匀速: 第一段 %.12f, 第二段 %.12f", d1, d2)
	}

	want := float64(100*time.Millisecond) / float64(animCycleDuration)
	if math.Abs(d1-want) > 1e-9 {
		t.Fatalf("推进量与真实时间不成比例: got %.12f, want %.12f", d1, want)
	}

	// 同一时间点重复调用：pct 不应额外前进（速度只取决于时间）。
	before := a.pct
	for i := 0; i < 50; i++ {
		a.tickAt(base.Add(200 * time.Millisecond))
	}
	if math.Abs(a.pct-before) > 1e-12 {
		t.Fatalf("同一时间点重复调用导致额外推进: before %.12f, after %.12f", before, a.pct)
	}
}

// TestAnimWrapContinuous 验证 pct 跨过 1.0 时按取模回绕，
// 不出现"到 100% 直接归零"的顿挫。
func TestAnimWrapContinuous(t *testing.T) {
	a := NewAnim(10)
	base := time.Unix(1700000000, 0)
	a.pct = 0.95
	a.lastTick = base

	// 前进 10% → 0.95 + 0.10 = 1.05 → 回绕为 0.05。
	a.tickAt(base.Add(animCycleDuration / 10))
	if math.Abs(a.pct-0.05) > 1e-9 {
		t.Fatalf("回绕不正确: got %.9f, want 0.05", a.pct)
	}
}

// TestAnimClampLongGap 验证长时间挂起/恢复后 dt 被截断到一整圈，
// 不会因为一次巨大的 dt 而让进度条跳变。
func TestAnimClampLongGap(t *testing.T) {
	a := NewAnim(10)
	base := time.Unix(1700000000, 0)
	a.tickAt(base)
	before := a.pct

	// 挂起 10 分钟后恢复：dt 被截断为 animCycleDuration → 恰好回绕一整圈，
	// pct 回到原值（而不会溢出成任意大数）。
	a.tickAt(base.Add(10 * time.Minute))
	if a.pct < 0 || a.pct >= 1.0 {
		t.Fatalf("pct 越界: %.12f", a.pct)
	}
	if math.Abs(a.pct-before) > 1e-9 {
		t.Fatalf("长时间间隔未被截断为一整圈: before %.12f, after %.12f", before, a.pct)
	}
}

// TestEnsureTickIdempotent 验证 tick 链守卫的幂等性：
// tickStarted 为 true 时 ensureTick 返回 nil（链已在跑），否则返回非 nil 并置位。
func TestEnsureTickIdempotent(t *testing.T) {
	m := &model{}

	if m.tickStarted {
		t.Fatal("新 model 的 tickStarted 应为 false")
	}

	cmd := m.ensureTick()
	if cmd == nil {
		t.Fatal("tickStarted=false 时 ensureTick 应返回非 nil cmd")
	}
	if !m.tickStarted {
		t.Fatal("ensureTick 应把 tickStarted 置为 true")
	}

	// 第二次调用不应再新建链。
	if again := m.ensureTick(); again != nil {
		t.Fatal("tickStarted=true 时 ensureTick 应返回 nil（链已在跑）")
	}

	// stopTick 之后应可再次补链。
	m.stopTick()
	if m.tickStarted {
		t.Fatal("stopTick 应把 tickStarted 置为 false")
	}
	if cmd := m.ensureTick(); cmd == nil {
		t.Fatal("stopTick 之后 ensureTick 应能重新补链")
	}
}
