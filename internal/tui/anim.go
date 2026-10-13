package tui

import (
	"time"

	"charm.land/bubbles/v2/progress"
)

// animCycleDuration 是进度条从 0% 走到 100% 所需的墙钟时长，决定动画速度。
// 可调：调大动画更慢、调小更快。
const animCycleDuration = 1500 * time.Millisecond

// animFrameFallback 是首次 Tick（lastTick 为零值）时使用的名义推进间隔，
// 与 tickCmd 的 100ms 间隔保持一致，使首帧推进量符合预期。
const animFrameFallback = 100 * time.Millisecond

// Anim manages a progress-bar animation for running tools.
// 动画由墙钟时间驱动：每次 Tick() 按距上次 Tick 经过的真实时长推进进度条，
// 因此速度恒定，与 Tick() 被调用的次数无关（避免 tick 链堆积导致"忽快"）。
type Anim struct {
	prog       progress.Model
	pct        float64
	lastTick   time.Time // 上次推进的墙钟时间；零值表示尚未推进过
	lastRender string    // cached render output for visual change detection
}

// NewAnim creates a new progress-bar animation with the given size.
// The size is mapped to the progress bar width (w = size * 2, min 20).
func NewAnim(size int) *Anim {
	if size <= 0 {
		size = 10
	}
	w := size * 2
	if w < 20 {
		w = 20
	}
	prog := progress.New(
		progress.WithDefaultBlend(),
		progress.WithWidth(w),
		progress.WithoutPercentage(),
	)
	return &Anim{
		prog: prog,
		pct:  0.0,
	}
}

// Tick advances the animation according to the wall-clock time elapsed since
// the previous Tick. Returns true if the visual output changed (the rendered
// string differs from the last frame).
func (a *Anim) Tick() bool {
	return a.tickAt(time.Now())
}

// tickAt 是 Tick 的可注入时钟版本：便于单元测试用固定时间点驱动动画，
// 验证推进量与真实经过时长成比例（匀速）。
func (a *Anim) tickAt(now time.Time) bool {
	var dt time.Duration
	if a.lastTick.IsZero() {
		// 首次调用：没有可用的时间基准，按名义间隔推进一帧。
		dt = animFrameFallback
		a.lastTick = now
	} else {
		dt = now.Sub(a.lastTick)
		a.lastTick = now
	}
	// 对 dt 设上限：进程长时间挂起/恢复或 tick 链被饿死后，
	// 避免 pct 一次跳变太多造成视觉顿挫。
	if dt > animCycleDuration {
		dt = animCycleDuration
	}
	if dt < 0 {
		dt = 0
	}

	a.pct += float64(dt) / float64(animCycleDuration)
	// 用取模方式回绕（而非直接归零），保持匀速连续，
	// 不出现"到 100% 突然重置"的顿挫。
	for a.pct >= 1.0 {
		a.pct -= 1.0
	}

	// Compare the new render with the last one to detect visual changes.
	// The progress bar advances in small steps, but the rendered output
	// (ANSI characters at fixed width) may not change every frame.
	newRender := a.prog.ViewAs(a.pct)
	if newRender == a.lastRender {
		return false
	}
	a.lastRender = newRender
	return true
}

// Render returns the current progress bar frame.
// Uses the cached render from the last Tick() when available.
func (a *Anim) Render() string {
	if a.lastRender != "" {
		return a.lastRender
	}
	return a.prog.ViewAs(a.pct)
}

// Reset resets the animation to its initial state (0% progress).
func (a *Anim) Reset() {
	a.pct = 0.0
	a.lastTick = time.Time{}
	a.lastRender = ""
}
