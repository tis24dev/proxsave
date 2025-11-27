package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestStatusColorMappings(t *testing.T) {
	tests := []struct {
		status string
		color  tcell.Color
	}{
		{"success", SuccessGreen},
		{"ok", SuccessGreen},
		{"error", ErrorRed},
		{"warning", WarningYellow},
		{"info", InfoBlue},
		{"pending", InfoBlue},
		{"unknown", LightGray},
	}

	for _, tt := range tests {
		if got := StatusColor(tt.status); got != tt.color {
			t.Fatalf("StatusColor(%q) = %v, want %v", tt.status, got, tt.color)
		}
	}
}

func TestStatusSymbolMappings(t *testing.T) {
	tests := []struct {
		status string
		symbol string
	}{
		{"success", SymbolSuccess},
		{"fail", SymbolError},
		{"warning", SymbolWarning},
		{"running", SymbolInfo},
		{"unknown", SymbolBullet},
	}

	for _, tt := range tests {
		if got := StatusSymbol(tt.status); got != tt.symbol {
			t.Fatalf("StatusSymbol(%q) = %q, want %q", tt.status, got, tt.symbol)
		}
	}
}
