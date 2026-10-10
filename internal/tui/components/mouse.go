package components

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// MouseAction represents the type of mouse action detected.
type MouseAction int

const (
	MouseNone        MouseAction = iota // No action
	MouseClick                          // Single click
	MouseDoubleClick                    // Double click
	MouseTripleClick                    // Triple click
	MouseScrollUp                       // Scroll wheel up
	MouseScrollDown                     // Scroll wheel down
	MouseDragStart                      // Drag start
	MouseDragMove                       // Drag move
	MouseDragEnd                        // Drag end
)

const (
	doubleClickThreshold = 400 * time.Millisecond
	dragThreshold        = 2 // px
)

// ClickDetector detects single-click, double-click, and triple-click events.
type ClickDetector struct {
	lastClickTime time.Time
	clickCount    int
	lastClickX    int
	lastClickY    int
	pendingAction MouseAction
	pendingCoordX int
	pendingCoordY int
}

// NewClickDetector creates a new ClickDetector.
func NewClickDetector() *ClickDetector {
	return &ClickDetector{
		clickCount: 0,
	}
}

// Detect analyzes a mouse message and returns the detected action along with
// the coordinates (x, y).
//
// Detection logic:
//   - Wheel events immediately return MouseScrollUp/Down
//   - Left button down returns MouseDragStart
//   - Middle/right button down returns MouseNone
//   - Clicks within 400ms and displacement <= 2px are considered double/triple clicks
//   - Clicks after 400ms are treated as new single clicks
func (cd *ClickDetector) Detect(msg interface{}) (MouseAction, int, int) {
	// Try to extract mouse event from various message types
	var m tea.Mouse
	var x, y int

	switch mm := msg.(type) {
	case tea.MouseClickMsg:
		m = tea.Mouse(mm)
		x, y = m.X, m.Y
		// Click is handled as release event
		return cd.handleRelease(x, y)
	case tea.MouseReleaseMsg:
		m = tea.Mouse(mm)
		x, y = m.X, m.Y
		return cd.handleRelease(x, y)
	case tea.MouseWheelMsg:
		m = tea.Mouse(mm)
		x, y = m.X, m.Y
		switch m.Button {
		case tea.MouseWheelUp:
			return MouseScrollUp, x, y
		case tea.MouseWheelDown:
			return MouseScrollDown, x, y
		default:
			return MouseNone, x, y
		}
	case tea.MouseMotionMsg:
		m = tea.Mouse(mm)
		x, y = m.X, m.Y
		return cd.handleMotion(x, y)
	default:
		return MouseNone, 0, 0
	}
}

// handleRelease processes a button release event.
func (cd *ClickDetector) handleRelease(x, y int) (MouseAction, int, int) {
	now := time.Now()
	elapsed := now.Sub(cd.lastClickTime)
	dx := x - cd.lastClickX
	dy := y - cd.lastClickY
	distance := absInt(dx) + absInt(dy) // Manhattan distance

	if elapsed > doubleClickThreshold || distance > dragThreshold || cd.clickCount == 0 {
		// New click sequence - reset
		cd.clickCount = 1
		cd.lastClickTime = now
		cd.lastClickX = x
		cd.lastClickY = y
		cd.pendingAction = MouseClick
		cd.pendingCoordX = x
		cd.pendingCoordY = y
		return MouseClick, x, y
	}

	// Within threshold - increment click count
	cd.clickCount++
	cd.lastClickTime = now
	cd.lastClickX = x
	cd.lastClickY = y

	switch cd.clickCount {
	case 2:
		return MouseDoubleClick, x, y
	case 3:
		cd.clickCount = 0 // Reset after triple click
		return MouseTripleClick, x, y
	default:
		// More than triple - treat as triple
		cd.clickCount = 0
		return MouseTripleClick, x, y
	}
}

// handleMotion processes a mouse motion event.
func (cd *ClickDetector) handleMotion(x, y int) (MouseAction, int, int) {
	// Check for drag move
	if cd.clickCount > 0 {
		dx := x - cd.lastClickX
		dy := y - cd.lastClickY
		distance := absInt(dx) + absInt(dy)

		if distance > dragThreshold {
			cd.pendingAction = MouseDragMove
			cd.pendingCoordX = x
			cd.pendingCoordY = y
			return MouseDragMove, x, y
		}
	}
	return MouseNone, x, y
}

// absInt returns the absolute value of an integer.
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
