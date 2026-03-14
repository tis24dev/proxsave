package wizard

import (
	"context"
	"errors"
	"fmt"

	"github.com/tis24dev/proxsave/internal/orchestrator"
)

type ageSetupUIAdapter struct {
	configPath string
	buildSig   string
}

func NewAgeSetupUI(configPath, buildSig string) orchestrator.AgeSetupUI {
	return &ageSetupUIAdapter{
		configPath: configPath,
		buildSig:   buildSig,
	}
}

func (a *ageSetupUIAdapter) ConfirmOverwriteExistingRecipient(ctx context.Context, recipientPath string) (bool, error) {
	return ConfirmRecipientOverwrite(recipientPath, a.configPath, a.buildSig)
}

func (a *ageSetupUIAdapter) CollectRecipientDraft(ctx context.Context, recipientPath string) (*orchestrator.AgeRecipientDraft, error) {
	data, err := RunAgeSetupWizard(ctx, recipientPath, a.configPath, a.buildSig)
	if err != nil {
		if errors.Is(err, ErrAgeSetupCancelled) {
			return nil, orchestrator.ErrAgeRecipientSetupAborted
		}
		return nil, err
	}
	if data == nil {
		return nil, orchestrator.ErrAgeRecipientSetupAborted
	}

	switch data.SetupType {
	case "existing":
		return &orchestrator.AgeRecipientDraft{
			Kind:      orchestrator.AgeRecipientInputExisting,
			PublicKey: data.PublicKey,
		}, nil
	case "passphrase":
		return &orchestrator.AgeRecipientDraft{
			Kind:       orchestrator.AgeRecipientInputPassphrase,
			Passphrase: data.Passphrase,
		}, nil
	case "privatekey":
		return &orchestrator.AgeRecipientDraft{
			Kind:       orchestrator.AgeRecipientInputPrivateKey,
			PrivateKey: data.PrivateKey,
		}, nil
	default:
		return nil, fmt.Errorf("unknown AGE setup type: %s", data.SetupType)
	}
}

func (a *ageSetupUIAdapter) ConfirmAddAnotherRecipient(ctx context.Context, currentCount int) (bool, error) {
	return ConfirmAddRecipient(a.configPath, a.buildSig, currentCount)
}
