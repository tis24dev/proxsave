package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type ScreenSpec struct {
	Title           string
	HeaderText      string
	NavText         string
	ConfigPath      string
	BuildSig        string
	TitleColor      tcell.Color
	BorderColor     tcell.Color
	BackgroundColor tcell.Color
}

func BuildScreen(spec ScreenSpec, content tview.Primitive) tview.Primitive {
	if content == nil {
		content = tview.NewBox()
	}
	configPath := strings.TrimSpace(spec.ConfigPath)
	buildSig := strings.TrimSpace(spec.BuildSig)
	escapedHeaderText := tview.Escape(spec.HeaderText)
	escapedConfigPath := tview.Escape(configPath)
	escapedBuildSig := tview.Escape(buildSig)

	welcomeText := tview.NewTextView().
		SetText(escapedHeaderText).
		SetTextColor(ProxmoxLight).
		SetDynamicColors(true)
	welcomeText.SetBorder(false)

	navText := strings.TrimSpace(spec.NavText)
	if navText != "" {
		navText = "\n" + navText
	}
	separator := tview.NewTextView().
		SetText(strings.Repeat("─", 80)).
		SetTextColor(ProxmoxOrange)
	separator.SetBorder(false)

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow)
	flex.AddItem(welcomeText, 5, 0, false)
	if navText != "" {
		navInstructions := tview.NewTextView().
			SetText(navText).
			SetTextColor(tcell.ColorWhite).
			SetDynamicColors(true).
			SetTextAlign(tview.AlignCenter)
		navInstructions.SetBorder(false)
		flex.AddItem(navInstructions, 2, 0, false)
	}
	flex.AddItem(separator, 1, 0, false)
	flex.AddItem(content, 0, 1, true)

	if configPath != "" {
		configPathText := tview.NewTextView().
			SetText(fmt.Sprintf("[yellow]Configuration file:[white] %s", escapedConfigPath)).
			SetTextColor(tcell.ColorWhite).
			SetDynamicColors(true).
			SetTextAlign(tview.AlignCenter)
		configPathText.SetBorder(false)
		flex.AddItem(configPathText, 1, 0, false)
	}

	if buildSig != "" {
		buildSigText := tview.NewTextView().
			SetText(fmt.Sprintf("[yellow]Build Signature:[white] %s", escapedBuildSig)).
			SetTextColor(tcell.ColorWhite).
			SetDynamicColors(true).
			SetTextAlign(tview.AlignCenter)
		buildSigText.SetBorder(false)
		flex.AddItem(buildSigText, 1, 0, false)
	}

	flex.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s ", spec.Title)).
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(spec.TitleColor).
		SetBorderColor(spec.BorderColor).
		SetBackgroundColor(spec.BackgroundColor)

	return flex
}
