// Package common provides gradient text utilities adapted from crush's styles/grad.go.
package common

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/rivo/uniseg"
)

// ForegroundGrad returns a slice of strings representing the input string
// rendered with a horizontal gradient foreground from color1 to color2.
// Each string in the returned slice corresponds to a grapheme cluster.
func ForegroundGrad(base lipgloss.Style, input string, bold bool, color1, color2 color.Color) []string {
	if input == "" {
		return []string{""}
	}
	if len(input) == 1 {
		style := base.Foreground(color1)
		if bold {
			style = style.Bold(true)
		}
		return []string{style.Render(input)}
	}
	var clusters []string
	gr := uniseg.NewGraphemes(input)
	for gr.Next() {
		clusters = append(clusters, string(gr.Runes()))
	}

	ramp := makeGradientRamp(len(clusters), color1, color2)
	for i, c := range ramp {
		style := base.Foreground(c)
		if bold {
			style = style.Bold(true)
		}
		clusters[i] = style.Render(clusters[i])
	}
	return clusters
}

// ApplyForegroundGrad renders a string with a horizontal gradient foreground.
func ApplyForegroundGrad(base lipgloss.Style, input string, color1, color2 color.Color) string {
	if input == "" {
		return ""
	}
	var o strings.Builder
	clusters := ForegroundGrad(base, input, false, color1, color2)
	for _, c := range clusters {
		fmt.Fprint(&o, c)
	}
	return o.String()
}

// ApplyBoldForegroundGrad renders a string with a bold horizontal gradient foreground.
func ApplyBoldForegroundGrad(base lipgloss.Style, input string, color1, color2 color.Color) string {
	if input == "" {
		return ""
	}
	var o strings.Builder
	clusters := ForegroundGrad(base, input, true, color1, color2)
	for _, c := range clusters {
		fmt.Fprint(&o, c)
	}
	return o.String()
}

// makeGradientRamp returns a slice of colors blended between the given keys.
// Blending uses HCL to stay in gamut (adapted from crush).
func makeGradientRamp(size int, stops ...color.Color) []color.Color {
	if len(stops) < 2 {
		return nil
	}

	points := make([]colorful.Color, len(stops))
	for i, k := range stops {
		points[i], _ = colorful.MakeColor(k)
	}

	numSegments := len(stops) - 1
	if numSegments == 0 {
		return nil
	}
	blended := make([]color.Color, 0, size)

	baseSize := size / numSegments
	remainder := size % numSegments

	for i := range numSegments {
		c1 := points[i]
		c2 := points[i+1]
		segmentSize := baseSize
		if i < remainder {
			segmentSize++
		}

		for j := range segmentSize {
			if segmentSize == 0 {
				continue
			}
			t := float64(j) / float64(segmentSize)
			c := c1.BlendHcl(c2, t)
			blended = append(blended, c)
		}
	}

	return blended
}

// DefaultGradFrom is the default gradient start color (blue).
var DefaultGradFrom = color.RGBA{R: 0x4A, G: 0x90, B: 0xD9, A: 0xFF}

// DefaultGradTo is the default gradient end color (cyan).
var DefaultGradTo = color.RGBA{R: 0x36, G: 0xB9, B: 0xB9, A: 0xFF}

// ApplyGrad renders text with the default blue-to-cyan gradient.
func ApplyGrad(input string) string {
	return ApplyBoldForegroundGrad(
		lipgloss.NewStyle(),
		input,
		DefaultGradFrom,
		DefaultGradTo,
	)
}
