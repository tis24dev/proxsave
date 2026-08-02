package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/installer"
)

func promptExistingConfigModeCLI(ctx context.Context, reader *bufio.Reader, configPath string) (installer.ExistingConfigAction, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	exists, err := installer.ExistingConfigPresent(configPath)
	if err != nil {
		return installer.ExistingConfigCancel, err
	}
	if !exists {
		if err := ctx.Err(); err != nil {
			return installer.ExistingConfigCancel, err
		}
		return installer.ExistingConfigOverwrite, nil
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
			return installer.ExistingConfigCancel, err
		}
		switch strings.TrimSpace(choice) {
		case "":
			fallthrough
		case "3":
			if err := ctx.Err(); err != nil {
				return installer.ExistingConfigCancel, err
			}
			return installer.ExistingConfigKeepContinue, nil
		case "1":
			if err := ctx.Err(); err != nil {
				return installer.ExistingConfigCancel, err
			}
			return installer.ExistingConfigOverwrite, nil
		case "2":
			if err := ctx.Err(); err != nil {
				return installer.ExistingConfigCancel, err
			}
			return installer.ExistingConfigEdit, nil
		case "0":
			if err := ctx.Err(); err != nil {
				return installer.ExistingConfigCancel, err
			}
			return installer.ExistingConfigCancel, nil
		default:
			fmt.Println("Please enter 1, 2, 3 or 0.")
		}
	}
}

func prepareExistingConfigDecisionCLI(ctx context.Context, reader *bufio.Reader, configPath string) (installer.ExistingConfigDecision, error) {
	action, err := promptExistingConfigModeCLI(ctx, reader, configPath)
	if err != nil {
		return installer.ExistingConfigDecision{}, err
	}
	return installer.ResolveExistingConfigDecision(action, configPath)
}
