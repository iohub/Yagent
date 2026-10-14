package activity

// 暂停计时的闸门（docs/pause-resume-design.md §10 G4）。
//
// 两条都必须能失败：
//   - idle 靠心跳能续，但暂停期间循环是停住的、没人发心跳，60s 阈值照样杀；
//   - total 靠心跳**续不了**（totalElapsed 取自创建时固定的 startTime），
//     所以它只能被显式冻结。只测前一条会让"暂停中的任务在 20m 后被杀掉"
//     这个缺陷完全不可见。

import (
	"context"
	"errors"
	"testing"
	"time"
)

// G4a：暂停期间 idle 计时冻结；恢复后基准推到恢复时刻，
// 而不是立刻被"暂停那么久没动作"杀掉，也不会永远不再超时。
func TestIdleTimeoutFrozenWhilePaused(t *testing.T) {
	const idleTimeout = 50 * time.Millisecond
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 5 * time.Millisecond,
	})
	defer cancel()

	Pause(ctx)
	time.Sleep(3 * idleTimeout)
	if ctx.Err() != nil {
		t.Fatalf("paused run was killed by idle timeout: cause = %v", context.Cause(ctx))
	}

	Resume(ctx)
	// 恢复瞬间若沿用暂停前的心跳时刻，idleFor 会是 3×阈值 —— 第一个 tick 就杀。
	// 半个阈值内还活着才证明基准被推到了恢复时刻。
	time.Sleep(idleTimeout / 2)
	if ctx.Err() != nil {
		t.Fatalf("idle clock kept the paused span: cause = %v", context.Cause(ctx))
	}

	// 恢复后不再有任何活动 → 仍应在 idle 阈值后被取消（冻结不是永久豁免）。
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("resumed run never timed out (freeze never released)")
	}
	var idleErr *IdleTimeoutError
	cause := context.Cause(ctx)
	if !errors.As(cause, &idleErr) {
		t.Fatalf("cause = %v (%T), want *IdleTimeoutError", cause, cause)
	}
	// 报白里的 idleFor 只该是恢复之后那一段。
	if idleErr.IdleFor > 2*idleTimeout {
		t.Errorf("IdleFor = %v, want <= %v (paused span leaked into the report)",
			idleErr.IdleFor, 2*idleTimeout)
	}
}

// G4b：total 计时扣掉暂停时长。这条是 Heartbeat 续不了的，只能显式冻结。
func TestTotalTimeoutExcludesPaused(t *testing.T) {
	const totalTimeout = 60 * time.Millisecond
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   5 * time.Second, // 让 idle 完全不参与，只考察 total
		TotalTimeout:  totalTimeout,
		CheckInterval: 5 * time.Millisecond,
	})
	defer cancel()

	// 持续心跳：证明"能续 idle 也续不了 total"，被冻住的必须是 total。
	stopHB := make(chan struct{})
	defer close(stopHB)
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopHB:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				Heartbeat(ctx, "hb")
			}
		}
	}()

	Pause(ctx)
	time.Sleep(8 * totalTimeout)
	if ctx.Err() != nil {
		t.Fatalf("paused run was killed by total timeout: cause = %v", context.Cause(ctx))
	}

	Resume(ctx)
	time.Sleep(totalTimeout / 2)
	if ctx.Err() != nil {
		t.Fatalf("total clock kept the paused span: cause = %v", context.Cause(ctx))
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("resumed run never hit the total limit")
	}
	var totalErr *TotalTimeoutError
	cause := context.Cause(ctx)
	if !errors.As(cause, &totalErr) {
		t.Fatalf("cause = %v (%T), want *TotalTimeoutError", cause, cause)
	}
	// 触发时的 TotalElapsed 必须是从创建起扣掉暂停的那段，而不是墙钟 2s。
	if totalErr.TotalElapsed > 4*totalTimeout {
		t.Errorf("TotalElapsed = %v, want <= %v (paused span leaked into the report)",
			totalErr.TotalElapsed, 4*totalTimeout)
	}
}

// 嵌套委派：只对内层 ctx 调 Pause/Resume，外层监视器必须一起冻一起解。
// 只冻最内层 sink 的话，暂停超过外层阈值会把整条委派链杀掉。
func TestPauseFreezesWholeNestedChain(t *testing.T) {
	const idleTimeout = 60 * time.Millisecond
	outerCtx, outerCancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 5 * time.Millisecond,
	})
	defer outerCancel()
	innerCtx, innerCancel := WithIdleTimeout(outerCtx, Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 5 * time.Millisecond,
	})
	defer innerCancel()

	Pause(innerCtx)
	time.Sleep(3 * idleTimeout)
	if outerCtx.Err() != nil {
		t.Fatalf("outer ctx cancelled while inner paused: cause = %v", context.Cause(outerCtx))
	}
	if innerCtx.Err() != nil {
		t.Fatalf("inner ctx cancelled while paused: cause = %v", context.Cause(innerCtx))
	}

	Resume(innerCtx)
	// 两层都必须解冻，否则外层再也没人碰它、任务永久挂住。
	if !eventually(t, 2*time.Second, func() bool { return innerCtx.Err() != nil }) {
		t.Fatal("inner ctx never timed out after resume (freeze leaked)")
	}
	if !eventually(t, 2*time.Second, func() bool { return outerCtx.Err() != nil }) {
		t.Fatal("outer ctx never timed out after resume (chain freeze broken)")
	}
	for name, ctx := range map[string]context.Context{"inner": innerCtx, "outer": outerCtx} {
		var idleErr *IdleTimeoutError
		if cause := context.Cause(ctx); !errors.As(cause, &idleErr) {
			t.Errorf("%s cause = %v (%T), want *IdleTimeoutError", name, cause, cause)
		}
	}
}

// 未暂停时的 Resume 必须是 no-op：它一旦顺手续期 idle，闸门里多出的一次
// Resume 就成了免费心跳，暂停-恢复循环能把任务永远吊着不超时。
func TestResumeWithoutPauseDoesNotRenewIdle(t *testing.T) {
	const idleTimeout = 50 * time.Millisecond
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 5 * time.Millisecond,
	})
	defer cancel()

	Resume(ctx)
	Resume(ctx)

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("idle timeout never fired although nothing ever paused")
	}
}

// 无 sink 的普通 ctx 调用 Pause/Resume 为 no-op：与 Heartbeat 同规格。
func TestPauseNoSinkNoop(t *testing.T) {
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Pause(cctx)
	Resume(cctx)
	Pause(context.Background())
	Resume(context.Background())
	if err := cctx.Err(); err != nil {
		t.Errorf("ctx with no activity sink must stay alive, got Err()=%v", err)
	}
}
