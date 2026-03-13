package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
)

type existingConfigMode int

const (
	existingConfigOverwrite existingConfigMode = iota
	existingConfigEdit
	existingConfigKeepContinue
	existingConfigCancel
)

type existingConfigDecision struct {
	BaseTemplate     string
	SkipConfigWizard bool
	AbortInstall     bool
}

func promptExistingConfigModeCLI(ctx context.Context, reader *bufio.Reader, configPath string) (existingConfigMode, error) {
	info, err := os.Stat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return existingConfigOverwrite, nil
		}
		return existingConfigCancel, fmt.Errorf("failed to access configuration file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return existingConfigCancel, fmt.Errorf("configuration file path is not a regular file: %s", configPath)
	}

	fmt.Printf("%s already exists.\n", configPath)
	fmt.Println("Choose how to proceed:")
	fmt.Println("  [1] Overwrite (start from embedded template)")
	fmt.Println("  [2] Edit existing (use current file as base)")
	fmt.Println("  [3] Keep existing & continue (skip configuration wizard)")
	fmt.Println("  [0] Cancel installation")

	for {
		choice, err := promptOptional(ctx, reader, "Choice [3]: ")
		if err != nil {
			return existingConfigCancel, err
		}
		switch strings.TrimSpace(choice) {
		case "":
			fallthrough
		case "3":
			return existingConfigKeepContinue, nil
		case "1":
			return existingConfigOverwrite, nil
		case "2":
			return existingConfigEdit, nil
		case "0":
			return existingConfigCancel, nil
		default:
			fmt.Println("Please enter 1, 2, 3 or 0.")
		}
	}
}

func resolveExistingConfigDecision(mode existingConfigMode, configPath string) (existingConfigDecision, error) {
	switch mode {
	case existingConfigOverwrite:
		return existingConfigDecision{
			BaseTemplate:     config.DefaultEnvTemplate(),
			SkipConfigWizard: false,
			AbortInstall:     false,
		}, nil
	case existingConfigEdit:
		content, err := os.ReadFile(configPath)
		if err != nil {
			return existingConfigDecision{}, fmt.Errorf("read existing configuration: %w", err)
		}
		return existingConfigDecision{
			BaseTemplate:     string(content),
			SkipConfigWizard: false,
			AbortInstall:     false,
		}, nil
	case existingConfigKeepContinue:
		return existingConfigDecision{
			BaseTemplate:     "",
			SkipConfigWizard: true,
			AbortInstall:     false,
		}, nil
	case existingConfigCancel:
		return existingConfigDecision{
			BaseTemplate:     "",
			SkipConfigWizard: false,
			AbortInstall:     true,
		}, nil
	default:
		return existingConfigDecision{}, fmt.Errorf("unsupported existing configuration mode: %d", mode)
	}
}

func prepareExistingConfigDecisionCLI(ctx context.Context, reader *bufio.Reader, configPath string) (existingConfigDecision, error) {
	mode, err := promptExistingConfigModeCLI(ctx, reader, configPath)
	if err != nil {
		return existingConfigDecision{}, err
	}
	return resolveExistingConfigDecision(mode, configPath)
}
