package tui

import (
	"charm.land/bubbles/v2/progress"
)

// Anim manages a progress-bar animation for running tools.
// Each Tick() advances the progress by a fixed step. The bubble tea program
// should call Tick() on every animation frame.
type Anim struct {
	prog       progress.Model
	pct        float64
	step       float64
	lastRender string // cached render output for visual change detection
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
		step: 0.015,
	}
}

// Tick advances the animation by one frame (2.5% of the progress bar).
// Returns true if the visual output changed (the rendered string differs from last frame).
func (a *Anim) Tick() bool {
	a.pct += a.step
	if a.pct >= 1.0 {
		a.pct = 0.0
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
	a.lastRender = ""
}
