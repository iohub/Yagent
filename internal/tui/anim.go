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

// preStyledChars stores pre-computed lipgloss-styled animation characters.
// Indexed as [position][cyclingCharIndex] → styled string.
// Pre-computed once to avoid per-frame allocations in the hot render path.
var preStyledChars []preStyledPos

type preStyledPos struct {
	chars []string // styled chars for each cyclingChar, index matches cyclingChars
}

// preStyledDots stores pre-styled ellipsis dots [0..3].
var preStyledDots [4]string

func init() {
	colorsLen := len(grayColors)
	maxSize := 20
	preStyledChars = make([]preStyledPos, maxSize)
	for pos := 0; pos < maxSize; pos++ {
		colorIdx := (pos * colorsLen) / maxSize
		if colorIdx >= colorsLen {
			colorIdx = colorsLen - 1
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(grayColors[colorIdx]))
		p := preStyledPos{chars: make([]string, len(cyclingChars))}
		for ci := range cyclingChars {
			p.chars[ci] = style.Render(string(cyclingChars[ci]))
		}
		preStyledChars[pos] = p
	}

	dotStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	for i := 0; i < 4; i++ {
		preStyledDots[i] = dotStyle.Render(strings.Repeat(".", i))
	}
}

// Anim manages a character-cycling animation for running tools.
// Each Tick() advances the animation state. The bubble tea program
// should call Tick() on every animation frame message.
type Anim struct {
	size     int
	step     int
	ellipsis int // 0,1,2,3 for trailing dots

	// Cached last render — avoids rebuilding identical output
	lastRender string
	lastStep   int
	lastEllips int
}

// NewAnim creates a new animation with the given cycling size.
func NewAnim(size int) *Anim {
	if size <= 0 {
		size = 10
	}
	return &Anim{
		size:     size,
		lastStep: -1,
	}
}

// Tick advances the animation by one frame.
func (a *Anim) Tick() {
	a.step = (a.step + 1) % len(cyclingChars)
	a.ellipsis = (a.ellipsis + 1) % 4
}

// Render returns the current animation frame using pre-computed styled chars.
// Output: gradient cycling chars + trailing dots, e.g. "0123abcd~!@...."
func (a *Anim) Render() string {
	if a.step == a.lastStep && a.ellipsis == a.lastEllips && a.lastRender != "" {
		return a.lastRender
	}
	a.lastStep = a.step
	a.lastEllips = a.ellipsis

	// Use pre-computed styled chars — no allocations in the hot path
	size := a.size
	if size > len(preStyledChars) {
		size = len(preStyledChars)
	}

	var b strings.Builder
	// Pre-allocate: each styled char ~= ANSI escape + char ≈ 20 bytes
	b.Grow(size*20 + 20)

	for i := 0; i < size; i++ {
		ci := (a.step + i) % len(cyclingChars)
		b.WriteString(preStyledChars[i].chars[ci])
	}

	// Trailing ellipsis
	b.WriteString(preStyledDots[a.ellipsis])

	a.lastRender = b.String()
	return a.lastRender
}
