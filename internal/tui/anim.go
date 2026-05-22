package tui

import (
	"charm.land/bubbles/v2/progress"
)

// Anim manages a progress-bar animation for running tools.
// Each Tick() advances the progress by a fixed step. The bubble tea program
// should call Tick() on every animation frame.
type Anim struct {
	prog progress.Model
	pct  float64
	step float64
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
		step: 0.025,
	}
}

// Tick advances the animation by one frame (2.5% of the progress bar).
func (a *Anim) Tick() {
	a.pct += a.step
	if a.pct >= 1.0 {
		a.pct = 0.0
	}
}

// Render returns the current progress bar frame.
func (a *Anim) Render() string {
	return a.prog.ViewAs(a.pct)
}
