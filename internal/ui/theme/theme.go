// Package theme defines the ProxSave visual identity for the Charm-based UI:
// the Proxmox palette carried over from the legacy tview theme, shared text
// styles, and status symbols.
package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Proxmox color palette (parity with the legacy tview theme).
var (
	Orange = lipgloss.Color("#E57000")
	Dark   = lipgloss.Color("#282828")
	Gray   = lipgloss.Color("#808080")
	Light  = lipgloss.Color("#C8C8C8")

	Green  = lipgloss.Color("#22C55E")
	Red    = lipgloss.Color("#EF4444")
	Yellow = lipgloss.Color("#EAB308")
	Blue   = lipgloss.Color("#3B82F6")

	White = lipgloss.Color("#FFFFFF")

	// Background is painted explicitly on every frame: the UI is
	// deterministic dark-first, with no terminal background detection
	// (serial/IPMI terminals answer background queries unreliably).
	Background = lipgloss.Color("#101010")
	// Surface is the elevated tone used for panels and blurred buttons.
	Surface = lipgloss.Color("#303030")
)

// Shared text styles.
var (
	Title    = lipgloss.NewStyle().Foreground(Orange).Bold(true)
	Text     = lipgloss.NewStyle().Foreground(Light)
	Emphasis = lipgloss.NewStyle().Foreground(White).Bold(true)
	Subtle   = lipgloss.NewStyle().Foreground(Gray)

	SuccessText = lipgloss.NewStyle().Foreground(Green)
	ErrorText   = lipgloss.NewStyle().Foreground(Red).Bold(true)
	WarningText = lipgloss.NewStyle().Foreground(Yellow)
	InfoText    = lipgloss.NewStyle().Foreground(Blue)

	// Selection bar for lists (parity with the tview orange selection).
	Selected = lipgloss.NewStyle().Foreground(White).Background(Orange).Bold(true)

	ButtonFocused = lipgloss.NewStyle().Foreground(White).Background(Orange).Bold(true).Padding(0, 2)
	ButtonBlurred = lipgloss.NewStyle().Foreground(Light).Background(Surface).Padding(0, 2)
)

// Symbols and icons (parity with the legacy tview theme).
const (
	SymbolSuccess  = "✓"
	SymbolError    = "✗"
	SymbolWarning  = "⚠"
	SymbolInfo     = "ℹ"
	SymbolSelected = "▸"
	SymbolArrow    = "→"
	SymbolBullet   = "•"
	SymbolCheck    = "☑"
	SymbolUncheck  = "☐"
)

// StatusColor returns the appropriate color for a status keyword.
func StatusColor(status string) color.Color {
	switch status {
	case "success", "ok", "done", "completed":
		return Green
	case "error", "failed", "fail":
		return Red
	case "warning", "warn":
		return Yellow
	case "info", "pending", "running":
		return Blue
	default:
		return Light
	}
}

// StatusSymbol returns the appropriate symbol for a status keyword.
func StatusSymbol(status string) string {
	switch status {
	case "success", "ok", "done", "completed":
		return SymbolSuccess
	case "error", "failed", "fail":
		return SymbolError
	case "warning", "warn":
		return SymbolWarning
	case "info", "pending", "running":
		return SymbolInfo
	default:
		return SymbolBullet
	}
}
