package tui

import (
	"github.com/gdamore/tcell/v2"
)

// Proxmox color palette
var (
	// Primary Proxmox color
	ProxmoxOrange = tcell.NewRGBColor(229, 112, 0) // #E57000

	// Neutral colors
	ProxmoxDark  = tcell.NewRGBColor(40, 40, 40)   // #282828
	ProxmoxGray  = tcell.NewRGBColor(128, 128, 128) // #808080
	ProxmoxLight = tcell.NewRGBColor(200, 200, 200) // #C8C8C8

	// Status colors
	SuccessGreen  = tcell.NewRGBColor(34, 197, 94)   // #22C55E
	ErrorRed      = tcell.NewRGBColor(239, 68, 68)   // #EF4444
	WarningYellow = tcell.NewRGBColor(234, 179, 8)   // #EAB308
	InfoBlue      = tcell.NewRGBColor(59, 130, 246)  // #3B82F6

	// Additional UI colors
	White     = tcell.ColorWhite
	Black     = tcell.ColorBlack
	LightGray = tcell.ColorLightGray
	DarkGray  = tcell.ColorDarkGray
)

// Symbols and icons
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

// StatusColor returns the appropriate color for a status
func StatusColor(status string) tcell.Color {
	switch status {
	case "success", "ok", "done", "completed":
		return SuccessGreen
	case "error", "failed", "fail":
		return ErrorRed
	case "warning", "warn":
		return WarningYellow
	case "info", "pending", "running":
		return InfoBlue
	default:
		return LightGray
	}
}

// StatusSymbol returns the appropriate symbol for a status
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
