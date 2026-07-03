package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/huh/v2"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// runUIDemoMode drives every Charm UI component once, for manual validation
// on real terminals (SSH, console, degraded TERM). Temporary developer mode
// behind the hidden --ui-demo flag; removed when the UI migration completes.
func runUIDemoMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, toolVersion string) (int, bool) {
	if !args.UIDemo {
		return types.ExitSuccess.Int(), false
	}
	if err := runUIDemo(ctx, toolVersion); err != nil && !errors.Is(err, shell.ErrAborted) && !errors.Is(err, context.Canceled) {
		bootstrap.Error("ERROR: ui demo: %v", err)
		return types.ExitGenericError.Int(), true
	}
	return types.ExitSuccess.Int(), true
}

func runUIDemo(ctx context.Context, toolVersion string) error {
	s := shell.Start(ctx, shell.Config{
		AppName:    "ProxSave",
		Subtitle:   "UI demo",
		Version:    toolVersion,
		ConfigPath: "/opt/proxsave/env/backup.env",
		BuildSig:   "demo-build-signature",
		UseColor:   true,
	})
	var results []string
	defer func() {
		closeErr := s.Close()
		for _, r := range results {
			fmt.Println(r)
		}
		if closeErr != nil {
			fmt.Printf("session close error: %v\n", closeErr)
		}
	}()

	// 1. Selector.
	mode, err := shell.Ask(ctx, s, components.NewSelector(
		"Select restore mode",
		[]components.SelectorItem[string]{
			{Label: "Full", Description: "restore every category", Value: "full"},
			{Label: "Storage", Description: "storage.cfg and related files", Value: "storage"},
			{Label: "Base", Description: "base configuration only", Value: "base"},
			{Label: "Custom", Description: "pick categories manually", Value: "custom"},
		},
		components.WithSelectorPrompt[string]("Use arrows or digits, Enter to confirm."),
	))
	if err != nil {
		return err
	}
	results = append(results, "selector: "+mode)

	// 2. Confirm with countdown and safe default (netcommit-style).
	confirm, err := shell.Ask(ctx, s, components.NewConfirm(
		"Network commit",
		"The new network configuration has been applied.\nCommit it now, or let the automatic rollback run?",
		components.WithLabels("COMMIT", "Let rollback run"),
		components.WithDefaultYes(false),
		components.WithCountdown(30*time.Second),
		components.WithDanger(),
	))
	if err != nil {
		return err
	}
	results = append(results, fmt.Sprintf("confirm: answer=%v timedOut=%v", confirm.Answer, confirm.TimedOut))

	// 3. Secret input with validation.
	secret, err := shell.Ask(ctx, s, components.NewInput(
		"Decrypt backup",
		"Enter the AGE passphrase",
		components.WithSecret(),
		components.WithNote("Input is masked; validation rejects short values."),
		components.WithValidate(func(v string) error {
			if len(v) < 4 {
				return fmt.Errorf("passphrase must be at least 4 characters")
			}
			return nil
		}),
	))
	if err != nil {
		if errors.Is(err, shell.ErrAborted) {
			results = append(results, "input: aborted")
		} else {
			return err
		}
	} else {
		results = append(results, fmt.Sprintf("input: %d characters", len(secret)))
	}

	// 4. huh form (the Phase-0 embedding spike, exercised live).
	var hostname string
	var encrypt bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Hostname").Description("Name of this node").Value(&hostname),
		huh.NewConfirm().Title("Enable encryption?").Value(&encrypt),
	))
	if _, err := shell.Ask(ctx, s, components.NewFormScreen("Install settings", form)); err != nil {
		if errors.Is(err, shell.ErrAborted) {
			results = append(results, "form: aborted")
		} else {
			return err
		}
	} else {
		results = append(results, fmt.Sprintf("form: hostname=%q encrypt=%v", hostname, encrypt))
	}

	// 5. Task progress.
	err = components.RunTask(ctx, s, "Scanning backups", "Contacting storage...", func(taskCtx context.Context, report func(string)) error {
		steps := []string{"Listing local backups...", "Listing cloud backups...", "Reading manifests...", "Sorting candidates..."}
		for i, step := range steps {
			select {
			case <-taskCtx.Done():
				return taskCtx.Err()
			case <-time.After(700 * time.Millisecond):
			}
			report(fmt.Sprintf("[%d/%d] %s", i+1, len(steps), step))
		}
		return nil
	})
	if err != nil {
		results = append(results, "task: "+err.Error())
	} else {
		results = append(results, "task: completed")
	}

	// 6. Pager with a long plan.
	var plan strings.Builder
	plan.WriteString("RESTORE PLAN\n\n")
	for i := 1; i <= 60; i++ {
		plan.WriteString(fmt.Sprintf("  %2d. category-%02d: 12 files, 3.4 MiB\n", i, i))
	}
	if _, err := shell.Ask(ctx, s, components.NewPager("Restore plan", plan.String())); err != nil {
		return err
	}
	results = append(results, "pager: viewed")

	// 7. Notice.
	if _, err := shell.Ask(ctx, s, components.NewNotice(components.NoticeSuccess,
		"Demo complete", "All components rendered. Results are printed after the UI closes.")); err != nil {
		return err
	}
	results = append(results, "notice: acknowledged")
	return nil
}
