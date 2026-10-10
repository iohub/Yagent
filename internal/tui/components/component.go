// Package components provides the foundational interfaces for the TUI
// component architecture. Components can be composed together to build
// the full user interface.
//
// The core interface is:
//   - Component: a composable UI unit with focus, bounds, and visibility management
package components

import tea "charm.land/bubbletea/v2"

// Component is a composable UI unit.
// It manages focus state, bounds, and visibility.
type Component interface {
	// Init initializes the component and returns an optional Cmd.
	Init() tea.Cmd

	// Update processes an incoming message and returns the updated component
	// (or the same instance) along with an optional Cmd.
	Update(msg tea.Msg) (Component, tea.Cmd)

	// View renders the component's current state as a string.
	View() string

	// IsFocused reports whether this component currently has keyboard focus.
	IsFocused() bool

	// Focus sets the component as focused and returns an optional Cmd.
	Focus() tea.Cmd

	// Blur removes focus from this component.
	Blur()

	// SetBounds sets the component's dimensions.
	SetBounds(width, height int)

	// Bounds returns the component's current width and height.
	Bounds() (int, int)

	// IsVisible reports whether this component is currently visible.
	IsVisible() bool

	// SetVisible sets the visibility of this component.
	SetVisible(bool)
}
