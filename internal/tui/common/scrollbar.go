package common

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Scrollbar renders a vertical scrollbar based on content and viewport size.
// Returns an empty string if content fits within viewport (no scrolling needed).
//
// height: total height of the scrollbar in lines
// contentSize: total number of lines in the content
// viewportSize: number of visible lines
// offset: current scroll offset (lines scrolled past top)
func Scrollbar(thumbStyle, trackStyle lipgloss.Style, height, contentSize, viewportSize, offset int) string {
	if height <= 0 || contentSize <= viewportSize {
		return ""
	}

	// Calculate thumb size (minimum 1 character)
	thumbSize := max(1, height*viewportSize/contentSize)

	// Calculate thumb position
	maxOffset := contentSize - viewportSize
	if maxOffset <= 0 {
		return ""
	}

	trackSpace := height - thumbSize
	thumbPos := 0
	if trackSpace > 0 && maxOffset > 0 {
		thumbPos = min(trackSpace, offset*trackSpace/maxOffset)
	}

	// Build the scrollbar
	var sb strings.Builder
	for i := range height {
		if i > 0 {
			sb.WriteString("\n")
		}
		if i >= thumbPos && i < thumbPos+thumbSize {
			sb.WriteString(thumbStyle.Render("┃"))
		} else {
			sb.WriteString(trackStyle.Render("│"))
		}
	}

	return sb.String()
}

// ScrollbarThumbChar is the character used for the scrollbar thumb.
const ScrollbarThumbChar = "┃"

// ScrollbarTrackChar is the character used for the scrollbar track.
const ScrollbarTrackChar = "│"
