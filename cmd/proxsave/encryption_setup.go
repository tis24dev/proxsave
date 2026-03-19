package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

type encryptionSetupResult struct {
	Config                   *config.Config
	RecipientPath            string
	WroteRecipientFile       bool
	ReusedExistingRecipients bool
}

func runInitialEncryptionSetupWithUI(ctx context.Context, configPath string, ui orchestrator.AgeSetupUI) (*encryptionSetupResult, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to reload configuration after install: %w", err)
	}

	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	orch := orchestrator.New(logger, false)
	orch.SetConfig(cfg)

	var setupResult *orchestrator.AgeRecipientSetupResult
	if ui != nil {
		setupResult, err = orch.EnsureAgeRecipientsReadyWithUIDetails(ctx, ui)
	} else {
		setupResult, err = orch.EnsureAgeRecipientsReadyWithDetails(ctx)
	}
	if err != nil {
		if errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
			return nil, fmt.Errorf("encryption setup aborted by user: %w", errInteractiveAborted)
		}
		return nil, fmt.Errorf("encryption setup failed: %w", err)
	}

	result := &encryptionSetupResult{Config: cfg}
	if setupResult != nil {
		result.RecipientPath = setupResult.RecipientPath
		result.WroteRecipientFile = setupResult.WroteRecipientFile
		result.ReusedExistingRecipients = setupResult.ReusedExistingRecipients
	}

	return result, nil
}

func runInitialEncryptionSetup(ctx context.Context, configPath string) error {
	_, err := runInitialEncryptionSetupWithUI(ctx, configPath, nil)
	return err
}
