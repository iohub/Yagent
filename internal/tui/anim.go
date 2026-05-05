package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// cyclingChars is the set of characters used in the running animation.
const cyclingChars = "0123456789abcdefABCDEF~!@#$%^&*()+=_"

// grayColors provides gradient grayscale ANSI colors from dim to bright.
var grayColors = []string{
	"238", "240", "242", "244", "246", "248", "250", "252",
}

// Anim manages a character-cycling animation for running tools.
// Each Tick() advances the animation state. The bubble tea program
// should call Tick() on every animation frame message.
type Anim struct {
	size     int
	step     int
	ellipsis int // 0,1,2,3 for trailing dots
}

// NewAnim creates a new animation with the given cycling size.
func NewAnim(size int) *Anim {
	if size <= 0 {
		size = 10
	}
	return &Anim{size: size}
}

// Tick advances the animation by one frame.
func (a *Anim) Tick() {
	a.step = (a.step + 1) % len(cyclingChars)
	a.ellipsis = (a.ellipsis + 1) % 4
}

// Render returns the current animation frame.
// Output: gradient cycling chars + trailing dots, e.g. "0123abcd~!@...."
func (a *Anim) Render() string {
	var b strings.Builder

	// Build cycling characters with grayscale gradient
	colorsLen := len(grayColors)
	for i := 0; i < a.size; i++ {
		idx := (a.step + i) % len(cyclingChars)
		ch := string(cyclingChars[idx])

		// Map position to color index for gradient effect
		colorIdx := (i * colorsLen) / a.size
		if colorIdx >= colorsLen {
			colorIdx = colorsLen - 1
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(grayColors[colorIdx]))
		b.WriteString(style.Render(ch))
	}

	// Trailing ellipsis
	dots := strings.Repeat(".", a.ellipsis)
	dotStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	b.WriteString(dotStyle.Render(dots))

	return b.String()
}
